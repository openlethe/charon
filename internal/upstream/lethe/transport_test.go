package lethe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Unix-socket upstream works as the supported local-IPC transport.
func TestClientOverUnixSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "lethe.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memory/project/refs" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	client := NewClient("unix://"+socketPath, "test-key", 2*time.Second, 1<<20)
	refs, err := client.ListRefs(context.Background(), "project")
	if err != nil {
		t.Fatalf("unix socket request failed: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %v", refs)
	}
}

// TLS certificate validation is on by default: an untrusted certificate fails.
func TestClientRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", 2*time.Second, 1<<20)
	_, err := client.ListRefs(context.Background(), "project")
	if err == nil {
		t.Fatal("self-signed certificate accepted")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate error, got %v", err)
	}
}

// Errors never carry the API key.
func TestErrorsDoNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	const apiKey = "super-secret-lethe-key-32chars!!"
	client := NewClient(server.URL, apiKey, 2*time.Second, 1<<20)
	_, err := client.ListRefs(context.Background(), "project")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaks API key: %v", err)
	}
	// The request itself must carry the key (the channel is the protection).
	var authHeader string
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode([]string{})
	}))
	defer server2.Close()
	client2 := NewClient(server2.URL, apiKey, 2*time.Second, 1<<20)
	if _, err := client2.ListRefs(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if authHeader != "Bearer "+apiKey {
		t.Fatalf("authorization header = %q", authHeader)
	}
}
