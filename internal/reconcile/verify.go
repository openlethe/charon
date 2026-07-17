package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openlethe/charon/internal/audit"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
)

// VerifyLocal checks Charon's own integrity controls on a restored database:
// the ledger hash chain and MACs, the external anchor, every proposal state
// digest, and the audit outbox backlog. Findings are added to the report.
func VerifyLocal(ctx context.Context, charonDataDir, anchorPath string, report *Report) error {
	db, err := store.Open(charonDataDir)
	if err != nil {
		return fmt.Errorf("open charon store: %w", err)
	}
	defer db.Close()

	ledgerSvc := ledger.NewService(db)
	ledgerSvc.SetAnchorPath(anchorPath)
	if _, err := ledgerSvc.Verify(ctx); err != nil {
		report.add("ledger-chain", "error", fmt.Sprintf("ledger chain verification failed: %v", err))
	}
	if _, err := ledgerSvc.VerifyAnchor(ctx); err != nil {
		report.add("ledger-anchor", "error", fmt.Sprintf("ledger anchor verification failed: %v", err))
	}

	// Loading every proposal verifies its state digest (fail-closed scan).
	ids, err := proposalIDs(ctx, db)
	if err != nil {
		return err
	}
	proposalSvc := proposal.NewService(db, nil)
	for _, id := range ids {
		if _, err := proposalSvc.Get(ctx, id); err != nil {
			report.add("proposal-digest", "error", fmt.Sprintf("proposal %s fails verification: %v", id, err))
		}
	}

	auditSvc := audit.NewService(db)
	if backlog, err := auditSvc.Backlog(ctx); err != nil {
		report.add("audit-outbox", "error", fmt.Sprintf("audit outbox unreadable: %v", err))
	} else if backlog > 0 {
		report.add("audit-outbox", "warning", fmt.Sprintf("%d audit intents are ambiguous and need investigation", backlog))
	}
	return nil
}

func proposalIDs(ctx context.Context, db *store.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM proposals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// CheckBackupManifest validates that a restore candidate matches a backup-set
// manifest: file identities must hash identically and the pair must belong
// together.
func CheckBackupManifest(manifestPath, charonDBPath, letheDBPath string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath) // #nosec G304 -- operator-supplied path
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest does not decode: %w", err)
	}
	if m.BackupID == "" {
		return nil, fmt.Errorf("manifest missing backup id")
	}
	charonID, err := identifyDB(charonDBPath, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	if charonID.SHA256 != m.Charon.SHA256 {
		return nil, fmt.Errorf("charon database does not match manifest %s", m.BackupID)
	}
	letheID, err := identifyDB(letheDBPath, `SELECT COALESCE(MAX(name), '') FROM schema_versions`)
	if err != nil {
		return nil, err
	}
	if letheID.SHA256 != m.Lethe.SHA256 {
		return nil, fmt.Errorf("lethe database does not match manifest %s", m.BackupID)
	}
	return &m, nil
}
