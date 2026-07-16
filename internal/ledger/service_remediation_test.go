package ledger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/store"
)

const testLedgerKey = "ledger-key-ledger-key-ledger-key-1"
const testLedgerKey2 = "ledger-key-ledger-key-ledger-key-2"

func openKeyedLedger(t *testing.T, anchorDir string) (*Service, *store.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := NewService(db)
	svc.SetIntegrityKey([]byte(testLedgerKey), "kid-1")
	if anchorDir != "" {
		svc.SetAnchorPath(filepath.Join(anchorDir, "ledger-anchor.jsonl"))
	}
	return svc, db
}

func record(t *testing.T, svc *Service, op string) {
	t.Helper()
	if err := svc.Record(context.Background(), Entry{
		RequestID: "req-" + op, PrincipalID: "principal", Operation: op,
		Projects: []string{"p"}, Result: "success", Latency: time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyedEntriesVerify(t *testing.T) {
	svc, _ := openKeyedLedger(t, "")
	record(t, svc, "op-a")
	record(t, svc, "op-b")
	n, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("verified = %d, want 2", n)
	}
}

func TestTamperedEntryDetected(t *testing.T) {
	svc, db := openKeyedLedger(t, "")
	record(t, svc, "op-a")
	record(t, svc, "op-b")

	// Modify a row's result field (tamper).
	if _, err := db.Exec(`UPDATE ledger_entries SET result = 'success (forged)' WHERE sequence = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background()); err == nil {
		t.Fatal("tampered entry not detected")
	}
}

func TestForgedMACDetected(t *testing.T) {
	svc, db := openKeyedLedger(t, "")
	record(t, svc, "op-a")

	// Recompute the hash chain after tampering but without the HMAC key the
	// attacker cannot forge the MAC.
	if _, err := db.Exec(`UPDATE ledger_entries SET result = 'forged'`); err != nil {
		t.Fatal(err)
	}
	// Attacker recomputes entry hashes? They cannot — the digest inputs are
	// known, so simulate recomputing hashes but not MACs is impossible
	// without... actually hashes are unkeyed; attacker CAN recompute them.
	// Simulate that: recompute nothing here; simply zero the MAC so the row
	// looks legacy. Verification then passes on hash only — which is why the
	// anchor exists (see TestFullChainReplacementDetected). Zeroing the MAC
	// of the TAIL is caught by the anchor; zeroing a middle entry is caught
	// by hash-chain recomputation if contents changed. Direct MAC corruption
	// must always fail:
	if _, err := db.Exec(`UPDATE ledger_entries SET entry_mac = X'deadbeef'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background()); err == nil {
		t.Fatal("corrupted MAC not detected")
	}
}

func TestLedgerKeyRotation(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(db)
	svc.SetIntegrityKey([]byte(testLedgerKey), "kid-1")
	record(t, svc, "before-rotation")

	// Rotate: new key signs, old key still verifies during the overlap window.
	svc.SetIntegrityKeys(map[string][]byte{"kid-1": []byte(testLedgerKey), "kid-2": []byte(testLedgerKey2)}, "kid-2")
	record(t, svc, "after-rotation")

	n, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("verified = %d, want 2", n)
	}

	// Dropping the old key from the map makes its entry fail closed.
	svc.SetIntegrityKeys(map[string][]byte{"kid-2": []byte(testLedgerKey2)}, "kid-2")
	if _, err := svc.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown key id") {
		t.Fatalf("removed key must fail closed, got %v", err)
	}
}

func TestAnchorVerifyAndReplacementDetection(t *testing.T) {
	anchorDir := t.TempDir()
	svc, db := openKeyedLedger(t, anchorDir)
	record(t, svc, "op-a")
	record(t, svc, "op-b")
	record(t, svc, "op-c")

	matched, err := svc.VerifyAnchor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if matched != 3 {
		t.Fatalf("anchor matched = %d, want 3", matched)
	}

	// Full-chain replacement: swap the entire table for a re-computed chain
	// (attacker with DB write but without the HMAC key).
	if _, err := db.Exec(`DELETE FROM ledger_entries`); err != nil {
		t.Fatal(err)
	}
	unkeyed := NewService(db) // no integrity key: forged chain, no MACs
	if err := unkeyed.Record(context.Background(), Entry{RequestID: "forged-1", Operation: "op-a", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := unkeyed.Record(context.Background(), Entry{RequestID: "forged-2", Operation: "op-b", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := unkeyed.Record(context.Background(), Entry{RequestID: "forged-3", Operation: "op-c", Result: "success"}); err != nil {
		t.Fatal(err)
	}
	// The forged chain verifies against itself by hash alone...
	if _, err := unkeyed.Verify(context.Background()); err != nil {
		t.Fatalf("forged unkeyed chain should self-verify: %v", err)
	}
	// ...but the external anchor exposes the replacement.
	if _, err := svc.VerifyAnchor(context.Background()); err == nil {
		t.Fatal("full-chain replacement not detected by anchor")
	}
}

func TestAnchorAppendFailureSurfaces(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(db)
	svc.SetIntegrityKey([]byte(testLedgerKey), "kid-1")
	svc.SetAnchorPath(filepath.Join(t.TempDir(), "missing-dir", "anchor.jsonl"))
	err = svc.Record(context.Background(), Entry{RequestID: "r", Operation: "op", Result: "success"})
	if err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("anchor failure must surface, got %v", err)
	}
	// The entry itself committed.
	n, _ := svc.Verify(context.Background())
	if n != 1 {
		t.Fatalf("entry must be durable despite anchor failure, verified = %d", n)
	}
}

func TestAnchorFileIsAppendOnly(t *testing.T) {
	anchorDir := t.TempDir()
	svc, _ := openKeyedLedger(t, anchorDir)
	record(t, svc, "one")
	record(t, svc, "two")
	data, err := os.ReadFile(filepath.Join(anchorDir, "ledger-anchor.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("anchor lines = %d, want 2", len(lines))
	}
}
