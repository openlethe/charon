package proposal

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openlethe/charon/internal/store"
)

// BackfillStateDigests is the v5 Go data migration. It computes state digests
// for proposals written before the integrity digest existed. A row whose
// persisted JSON decodes and validates is stamped with its digest; a row that
// does not decode keeps a NULL digest and is quarantined — every verified load
// of it fails closed from then on. Returns (migrated, quarantined).
//
// It runs at startup after store.Open and must fail the process on database
// error: silently skipping it would leave every legacy row quarantined.
func BackfillStateDigests(ctx context.Context, db *store.DB) (int, int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+proposalColumns+` FROM proposals WHERE state_digest IS NULL OR state_digest = ''
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("backfill query: %w", err)
	}
	var proposals []*Proposal
	for rows.Next() {
		p, _, err := scanProposalRaw(rows)
		if err != nil {
			// Undecodable persisted state: leave the NULL digest in place so
			// the row is quarantined on every future load.
			continue
		}
		proposals = append(proposals, p)
	}
	if err := rows.Err(); err != nil {
		// #nosec G104 -- already returning the scan error; Close failure is immaterial.
		rows.Close()
		return 0, 0, fmt.Errorf("backfill scan: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, fmt.Errorf("backfill close: %w", err)
	}

	var total int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM proposals WHERE state_digest IS NULL OR state_digest = ''
	`).Scan(&total); err != nil {
		return 0, 0, fmt.Errorf("backfill count: %w", err)
	}

	migrated := 0
	for _, p := range proposals {
		digest, err := StateDigest(p)
		if err != nil {
			return migrated, total - migrated, fmt.Errorf("backfill digest for proposal %s: %w", p.ID, err)
		}
		res, err := db.ExecContext(ctx, `
			UPDATE proposals SET state_digest = ? WHERE id = ? AND (state_digest IS NULL OR state_digest = '')
		`, digest, p.ID)
		if err != nil {
			return migrated, total - migrated, fmt.Errorf("backfill update for proposal %s: %w", p.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			migrated++
		}
	}
	return migrated, total - migrated, nil
}

// CountQuarantined reports how many proposals remain without a verifiable
// state digest. Operators alert on this after upgrade.
func CountQuarantined(ctx context.Context, db *store.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM proposals WHERE state_digest IS NULL OR state_digest = ''
	`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}
