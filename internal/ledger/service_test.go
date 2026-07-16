package ledger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/store"
)

func TestLedgerDigestUsesStructuredFraming(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	first := digestEntry(Entry{RequestID: "ab", PrincipalID: "c", Operation: "op", Result: "ok"}, now, []byte("[]"), nil)
	second := digestEntry(Entry{RequestID: "a", PrincipalID: "bc", Operation: "op", Result: "ok"}, now, []byte("[]"), nil)
	if string(first) == string(second) {
		t.Fatal("field-boundary ambiguity produced the same ledger digest")
	}
}

func TestLedgerRecordAndVerify(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewService(db)
	for i := 0; i < 3; i++ {
		if err := svc.Record(context.Background(), Entry{
			RequestID:   fmt.Sprintf("req-%d", i),
			PrincipalID: "principal-1",
			Operation:   "memory_status",
			Projects:    []string{"project-1"},
			Result:      "success",
			Latency:     time.Duration(i+1) * time.Millisecond,
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	count, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if count != 3 {
		t.Fatalf("verified count = %d, want 3", count)
	}
}

func TestLedgerConcurrentAppendsStayLinear(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewService(db)
	const writers = 64
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- svc.Record(context.Background(), Entry{
				RequestID:   fmt.Sprintf("req-%d", i),
				PrincipalID: "principal-concurrent",
				Operation:   "memory_log",
				Projects:    []string{"project-1"},
				Result:      "success",
				Latency:     time.Millisecond,
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}

	count, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatalf("verify concurrent chain: %v", err)
	}
	if count != writers {
		t.Fatalf("verified count = %d, want %d", count, writers)
	}
}

func TestLedgerConcurrentServiceInstancesStayLinear(t *testing.T) {
	dataDir := t.TempDir()
	firstDB, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()

	services := []*Service{NewService(firstDB), NewService(secondDB)}
	const writers = 64
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- services[i%len(services)].Record(context.Background(), Entry{
				RequestID:   fmt.Sprintf("req-multi-%d", i),
				PrincipalID: "principal-multi-process",
				Operation:   "memory_log",
				Projects:    []string{"project-1"},
				Result:      "success",
				Latency:     time.Millisecond,
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent multi-instance append: %v", err)
		}
	}

	count, err := services[0].Verify(context.Background())
	if err != nil {
		t.Fatalf("verify multi-instance chain: %v", err)
	}
	if count != writers {
		t.Fatalf("verified count = %d, want %d", count, writers)
	}
}

func TestLedgerVerifyAcceptsLegacyRowsBeforeStructuredRows(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	legacy := Entry{
		RequestID:   "legacy-req",
		PrincipalID: "legacy-principal",
		Operation:   "memory_status",
		Projects:    []string{"project-1"},
		Result:      "success",
		Latency:     time.Millisecond,
	}
	createdAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	projectsJSON := []byte(`["project-1"]`)
	legacyHash := legacyDigestEntry(legacy, createdAt, projectsJSON, nil)
	if _, err := db.Exec(`
		INSERT INTO ledger_entries (
			created_at, request_id, principal_id, operation,
			authorized_projects_json, result, latency_ms,
			previous_hash, entry_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, createdAt.Format(time.RFC3339Nano), legacy.RequestID, legacy.PrincipalID,
		legacy.Operation, string(projectsJSON), legacy.Result,
		legacy.Latency.Milliseconds(), nil, legacyHash); err != nil {
		t.Fatal(err)
	}

	svc := NewService(db)
	if err := svc.Record(context.Background(), Entry{
		RequestID:   "structured-req",
		PrincipalID: "structured-principal",
		Operation:   "memory_log",
		Projects:    []string{"project-1"},
		Result:      "success",
		Latency:     2 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	count, err := svc.Verify(context.Background())
	if err != nil {
		t.Fatalf("verify mixed-format chain: %v", err)
	}
	if count != 2 {
		t.Fatalf("verified count = %d, want 2", count)
	}
}

func TestLedgerVerifyDetectsTampering(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewService(db)
	if err := svc.Record(context.Background(), Entry{
		RequestID: "req-1", PrincipalID: "principal-1", Operation: "memory_status",
		Projects: []string{"project-1"}, Result: "success", Latency: time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE ledger_entries SET result = 'altered' WHERE sequence = 1"); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid entry hash") {
		t.Fatalf("expected tamper detection, got %v", err)
	}
}
