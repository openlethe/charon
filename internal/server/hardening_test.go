package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/store"
)

func TestLimitRequestBodyRejectsOversizedPayload(t *testing.T) {
	called := false
	handler := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}), 8)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("0123456789"))
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	if result.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", result.Code, http.StatusRequestEntityTooLarge)
	}
	if called {
		t.Fatal("oversized request reached the downstream handler")
	}
}

func TestDisabledPrincipalCannotAuthenticate(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	principalID, _, err := db.ReconcilePrincipal(
		context.Background(), "disabled-user", []string{"project-1"}, []string{"memory.read"},
	)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{db: db, cfg: &config.Config{PublicURL: "https://charon.example.test"}}
	if _, err := server.loadPrincipal(context.Background(), principalID); err != nil {
		t.Fatalf("active principal was unavailable: %v", err)
	}
	if _, err := db.Exec("UPDATE principals SET disabled_at = ? WHERE id = ?", time.Now().UTC().Format(time.RFC3339), principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadPrincipal(context.Background(), principalID); err == nil {
		t.Fatal("disabled principal loaded by ID")
	}
	if _, err := server.loadPrincipalByIDOrName(context.Background(), "disabled-user"); err == nil {
		t.Fatal("disabled principal loaded by name")
	}
}

func TestPrepareUnixSocketRemovesOnlyStaleSockets(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "charon-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	regularPath := filepath.Join(dir, "regular")
	if err := os.WriteFile(regularPath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareUnixSocket(regularPath); err == nil {
		t.Fatal("regular file was accepted as a stale socket")
	}
	if _, err := os.Stat(regularPath); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}

	socketPath := filepath.Join(dir, "stale.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareUnixSocket(socketPath); err != nil {
		t.Fatalf("remove stale socket: %v", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("stale socket still exists: %v", err)
	}
}

func TestLivenessDoesNotDependOnLetheReadiness(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{
		Mode: config.ModeMemoryGit, Upstream: upstream.URL, LetheAPIKey: "test-key",
		HMACKey: strings.Repeat("h", 32), MaxResultBytes: 1 << 20,
		MaxResultCount: 100, MaxRequestBytes: 1 << 20, RequestTimeout: time.Second,
		AuthMode: "none", PublicURL: "http://127.0.0.1",
	}
	server, err := NewServer(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", live.Code)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d", ready.Code)
	}
	compat := httptest.NewRecorder()
	handler.ServeHTTP(compat, httptest.NewRequest(http.MethodGet, "/health", nil))
	if compat.Code != http.StatusServiceUnavailable {
		t.Fatalf("compatibility health status = %d", compat.Code)
	}
}

func TestServerStopsWhenContextIsCancelled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := &config.Config{
		Mode:                  config.ModeMemoryGit,
		HTTP:                  "127.0.0.1:0",
		AdminHTTP:             "127.0.0.1:0",
		Upstream:              upstream.URL,
		PublicURL:             "http://127.0.0.1",
		LetheAPIKey:           "test-key",
		HMACKey:               strings.Repeat("h", 32),
		MaxResultBytes:        1 << 20,
		MaxResultCount:        100,
		MaxRequestBytes:       1 << 20,
		RequestTimeout:        time.Second,
		HTTPReadHeaderTimeout: time.Second,
		HTTPReadTimeout:       2 * time.Second,
		HTTPIdleTimeout:       2 * time.Second,
		HTTPShutdownTimeout:   2 * time.Second,
		HTTPMaxHeaderBytes:    64 << 10,
		AuthMode:              "none",
	}
	server, err := NewServer(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down after context cancellation")
	}
}
