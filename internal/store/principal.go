package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

	rows, err := tx.QueryContext(ctx, "SELECT id, disabled_at IS NOT NULL FROM principals WHERE name = ? ORDER BY id LIMIT 2", name)
	if err != nil {
		return "", false, fmt.Errorf("find principal: %w", err)
	}
	type principalMatch struct {
		id       string
		disabled bool
	}
	var matches []principalMatch
	for rows.Next() {
		var match principalMatch
		if err := rows.Scan(&match.id, &match.disabled); err != nil {
			scanErr := fmt.Errorf("scan principal: %w", err)
			if closeErr := rows.Close(); closeErr != nil {
				return "", false, errors.Join(scanErr, fmt.Errorf("close principal rows: %w", closeErr))
			}
			return "", false, scanErr
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		if closeErr := rows.Close(); closeErr != nil {
			return "", false, errors.Join(fmt.Errorf("iterate principals: %w", err), fmt.Errorf("close principal rows: %w", closeErr))
		}
		return "", false, fmt.Errorf("iterate principals: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", false, fmt.Errorf("close principal rows: %w", err)
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("multiple principals named %q; reconcile by unique name is unsafe", name)
	}

	created := false
	var principalID string
	if len(matches) == 1 {
		if matches[0].disabled {
			return "", false, fmt.Errorf("principal %q is disabled; enable it explicitly before reconciliation", name)
		}
		principalID = matches[0].id
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
