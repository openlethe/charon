// Package reconcile implements coordinated backup and cross-database
// reconciliation for the Charon+Lethe trust boundary. Independent backups can
// restore the pair into an inconsistent state, so backups are taken as a set
// with a manifest, and restores must pass reconciliation before mutations
// resume.
package reconcile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// DBIdentity pins one database snapshot in the manifest.
type DBIdentity struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Bytes         int64  `json:"bytes"`
	SchemaVersion string `json:"schema_version"`
}

// RefState is a protected ref at backup time.
type RefState struct {
	ProjectID string `json:"project_id"`
	RefName   string `json:"ref_name"`
	Head      string `json:"head_changeset_id"`
}

// Manifest is the backup-set record: both database identities, the protected
// refs, the ledger tail, and versions, so a restore can prove the pair
// belongs together.
type Manifest struct {
	BackupID       string     `json:"backup_id"`
	CreatedAt      time.Time  `json:"created_at"`
	Charon         DBIdentity `json:"charon"`
	Lethe          DBIdentity `json:"lethe"`
	ProtectedRefs  []RefState `json:"protected_refs"`
	LedgerTailHash string     `json:"ledger_tail_hash"`
	CharonVersion  string     `json:"charon_version"`
}

// Finding is one reconciliation discrepancy.
type Finding struct {
	Check    string `json:"check"`
	Severity string `json:"severity"` // error | warning
	Detail   string `json:"detail"`
}

// Report is the reconciliation outcome. Clean is true only when there are no
// error-severity findings.
type Report struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Findings   []Finding `json:"findings"`
	Clean      bool      `json:"clean"`
}

func (r *Report) add(check, severity, detail string) {
	r.Findings = append(r.Findings, Finding{Check: check, Severity: severity, Detail: detail})
	if severity == "error" {
		r.Clean = false
	}
}

// Backup takes consistent snapshots of both databases (VACUUM INTO is
// consistent online) and writes the backup-set manifest.
func Backup(ctx context.Context, charonDBPath, letheDBPath, outDir, charonVersion string) (*Manifest, error) {
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, err
	}
	charonSnap := filepath.Join(outDir, "charon.db")
	letheSnap := filepath.Join(outDir, "lethe.db")
	if err := vacuumInto(ctx, charonDBPath, charonSnap); err != nil {
		return nil, fmt.Errorf("charon snapshot: %w", err)
	}
	if err := vacuumInto(ctx, letheDBPath, letheSnap); err != nil {
		return nil, fmt.Errorf("lethe snapshot: %w", err)
	}

	charonID, err := identifyDB(charonSnap, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	letheID, err := identifyDB(letheSnap, `SELECT COALESCE(MAX(name), '') FROM schema_versions`)
	if err != nil {
		return nil, err
	}

	refs, err := protectedRefs(ctx, letheSnap)
	if err != nil {
		return nil, err
	}
	tailHash, err := ledgerTailHash(ctx, charonSnap)
	if err != nil {
		return nil, err
	}

	m := &Manifest{
		BackupID:       uuid.Must(uuid.NewV7()).String(),
		CreatedAt:      time.Now().UTC(),
		Charon:         charonID,
		Lethe:          letheID,
		ProtectedRefs:  refs,
		LedgerTailHash: tailHash,
		CharonVersion:  charonVersion,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), data, 0600); err != nil {
		return nil, err
	}
	return m, nil
}

func vacuumInto(ctx context.Context, srcPath, dstPath string) error {
	db, err := sql.Open("sqlite", srcPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dstPath); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dstPath, err)
	}
	return nil
}

func identifyDB(path, versionQuery string) (DBIdentity, error) {
	id := DBIdentity{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		return id, err
	}
	id.Bytes = info.Size()
	f, err := os.Open(path) // #nosec G304 -- operator-supplied backup path
	if err != nil {
		return id, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return id, err
	}
	id.SHA256 = hex.EncodeToString(h.Sum(nil))

	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return id, err
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(versionQuery).Scan(&version); err != nil {
		// Schema-version tables may legitimately be absent in very old files.
		version = "unknown"
	}
	id.SchemaVersion = version
	return id, nil
}

func protectedRefs(ctx context.Context, lethePath string) ([]RefState, error) {
	db, err := sql.Open("sqlite", lethePath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		SELECT project_id, ref_name, head_changeset_id FROM memory_refs WHERE protected = 1 ORDER BY project_id, ref_name
	`)
	if err != nil {
		return nil, fmt.Errorf("read protected refs: %w", err)
	}
	defer rows.Close()
	var refs []RefState
	for rows.Next() {
		var r RefState
		if err := rows.Scan(&r.ProjectID, &r.RefName, &r.Head); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	if refs == nil {
		refs = []RefState{}
	}
	return refs, rows.Err()
}

func ledgerTailHash(ctx context.Context, charonPath string) (string, error) {
	db, err := sql.Open("sqlite", charonPath+"?mode=ro")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var hash []byte
	err = db.QueryRowContext(ctx, `SELECT entry_hash FROM ledger_entries ORDER BY sequence DESC LIMIT 1`).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read ledger tail: %w", err)
	}
	return hex.EncodeToString(hash), nil
}

// Reconcile runs every cross-database consistency check against a restored
// pair. It never mutates either database.
func Reconcile(ctx context.Context, charonDBPath, letheDBPath string) (*Report, error) {
	report := &Report{StartedAt: time.Now().UTC(), Clean: true, Findings: []Finding{}}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		sort.SliceStable(report.Findings, func(i, j int) bool {
			if report.Findings[i].Severity != report.Findings[j].Severity {
				return report.Findings[i].Severity == "error"
			}
			return report.Findings[i].Check < report.Findings[j].Check
		})
	}()

	charonDB, err := sql.Open("sqlite", charonDBPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer charonDB.Close()
	letheDB, err := sql.Open("sqlite", letheDBPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer letheDB.Close()

	if err := checkApprovedMergesHaveAdvances(ctx, charonDB, letheDB, report); err != nil {
		return nil, err
	}
	if err := checkAdvancesHaveProposals(ctx, charonDB, letheDB, report); err != nil {
		return nil, err
	}
	if err := checkPendingMergesAgainstRefs(ctx, charonDB, letheDB, report); err != nil {
		return nil, err
	}
	if err := checkIdempotencyResources(ctx, charonDB, report); err != nil {
		return nil, err
	}
	return report, nil
}

// Every approved Charon merge proposal corresponds to the expected Lethe
// protected-ref advancement with the same resulting head.
func checkApprovedMergesHaveAdvances(ctx context.Context, charonDB, letheDB *sql.DB, report *Report) error {
	rows, err := charonDB.QueryContext(ctx, `
		SELECT id, COALESCE(resulting_lethe_event_id, '') FROM proposals
		WHERE kind = 'merge_request' AND status = 'approved'
	`)
	if err != nil {
		return fmt.Errorf("list approved merges: %w", err)
	}
	defer rows.Close()
	type merge struct{ id, result string }
	var merges []merge
	for rows.Next() {
		var m merge
		if err := rows.Scan(&m.id, &m.result); err != nil {
			return err
		}
		merges = append(merges, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range merges {
		var count int
		err := letheDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM memory_protected_ref_advances
			WHERE merge_proposal_id = ? AND new_head = ?
		`, m.id, m.result).Scan(&count)
		if err != nil {
			return fmt.Errorf("check advancement for %s: %w", m.id, err)
		}
		if count == 0 {
			report.add("approved-merge-has-advancement", "error",
				fmt.Sprintf("approved merge proposal %s has no Lethe advancement record for head %s (databases are from different points in time)", m.id, m.result))
		}
	}
	return nil
}

// Every protected Lethe advancement has a Charon authorization record.
func checkAdvancesHaveProposals(ctx context.Context, charonDB, letheDB *sql.DB, report *Report) error {
	rows, err := letheDB.QueryContext(ctx, `
		SELECT advance_id, merge_proposal_id, new_head FROM memory_protected_ref_advances
	`)
	if err != nil {
		return fmt.Errorf("list advancements: %w", err)
	}
	defer rows.Close()
	type adv struct{ id, proposal, head string }
	var advs []adv
	for rows.Next() {
		var a adv
		if err := rows.Scan(&a.id, &a.proposal, &a.head); err != nil {
			return err
		}
		advs = append(advs, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range advs {
		var count int
		err := charonDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proposals WHERE id = ?`, a.proposal).Scan(&count)
		if err != nil {
			return fmt.Errorf("check proposal for %s: %w", a.proposal, err)
		}
		if count == 0 {
			report.add("advancement-has-authorization", "error",
				fmt.Sprintf("Lethe advancement %s references missing Charon proposal %s", a.id, a.proposal))
		}
	}
	return nil
}

// Pending Charon merges whose target ref has already advanced past the
// proposal base must be completed or quarantined.
func checkPendingMergesAgainstRefs(ctx context.Context, charonDB, letheDB *sql.DB, report *Report) error {
	rows, err := charonDB.QueryContext(ctx, `
		SELECT id, project_id, COALESCE(base_changeset_id, ''), COALESCE(target_ref, '')
		FROM proposals WHERE kind = 'merge_request' AND status = 'pending'
	`)
	if err != nil {
		return fmt.Errorf("list pending merges: %w", err)
	}
	defer rows.Close()
	type pending struct{ id, project, base, target string }
	var pendings []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.project, &p.base, &p.target); err != nil {
			return err
		}
		pendings = append(pendings, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pendings {
		var head string
		err := letheDB.QueryRowContext(ctx, `
			SELECT head_changeset_id FROM memory_refs WHERE project_id = ? AND ref_name = ?
		`, p.project, p.target).Scan(&head)
		if err == sql.ErrNoRows {
			report.add("pending-merge-ref", "error",
				fmt.Sprintf("pending merge %s target ref %s missing in Lethe", p.id, p.target))
			continue
		}
		if err != nil {
			return fmt.Errorf("read ref for %s: %w", p.id, err)
		}
		if head != p.base {
			report.add("pending-merge-ref", "warning",
				fmt.Sprintf("pending merge %s target ref advanced to %s past base %s; complete or quarantine", p.id, head, p.base))
		}
	}
	return nil
}

// Idempotency records point to existing resources.
func checkIdempotencyResources(ctx context.Context, charonDB *sql.DB, report *Report) error {
	rows, err := charonDB.QueryContext(ctx, `
		SELECT principal_id, operation, idempotency_key, COALESCE(resulting_resource_id, '')
		FROM idempotency_records WHERE operation = 'memory_merge_propose'
	`)
	if err != nil {
		return fmt.Errorf("list idempotency records: %w", err)
	}
	defer rows.Close()
	type rec struct{ principal, op, key, resource string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.principal, &r.op, &r.key, &r.resource); err != nil {
			return err
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range recs {
		var count int
		err := charonDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proposals WHERE id = ?`, r.resource).Scan(&count)
		if err != nil {
			return fmt.Errorf("check idempotency resource %s: %w", r.resource, err)
		}
		if count == 0 {
			report.add("idempotency-resource-exists", "error",
				fmt.Sprintf("idempotency record %s/%s points to missing proposal %s", r.principal, r.key, r.resource))
		}
	}
	return nil
}
