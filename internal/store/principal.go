package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ReconcilePrincipal creates or updates the uniquely named principal and
// replaces its project and scope grants with the desired sets atomically.
func (db *DB) ReconcilePrincipal(ctx context.Context, name string, projects, scopes []string) (string, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin principal reconciliation: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, "SELECT id FROM principals WHERE name = ? ORDER BY id LIMIT 2", name)
	if err != nil {
		return "", false, fmt.Errorf("find principal: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", false, fmt.Errorf("scan principal: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return "", false, fmt.Errorf("close principal rows: %w", err)
	}
	if len(ids) > 1 {
		return "", false, fmt.Errorf("multiple principals named %q; reconcile by unique name is unsafe", name)
	}

	created := false
	var principalID string
	if len(ids) == 1 {
		principalID = ids[0]
	} else {
		principalID, err = newPrincipalID()
		if err != nil {
			return "", false, err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO principals (id, name, agent_id, created_at) VALUES (?, ?, ?, ?)",
			principalID, name, principalID, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return "", false, fmt.Errorf("create principal: %w", err)
		}
		created = true
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM principal_projects WHERE principal_id = ?", principalID); err != nil {
		return "", false, fmt.Errorf("clear project grants: %w", err)
	}
	for _, project := range projects {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO principal_projects (principal_id, project_id) VALUES (?, ?)", principalID, project,
		); err != nil {
			return "", false, fmt.Errorf("grant project %q: %w", project, err)
		}
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM principal_scopes WHERE principal_id = ?", principalID); err != nil {
		return "", false, fmt.Errorf("clear scope grants: %w", err)
	}
	for _, scope := range scopes {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO principal_scopes (principal_id, scope) VALUES (?, ?)", principalID, scope,
		); err != nil {
			return "", false, fmt.Errorf("grant scope %q: %w", scope, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit principal reconciliation: %w", err)
	}
	return principalID, created, nil
}

func newPrincipalID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate principal id: %w", err)
	}
	return "principal_" + hex.EncodeToString(b), nil
}
