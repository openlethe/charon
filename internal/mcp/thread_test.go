package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	markmcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

func TestThreadListDenialDoesNotLeakProjectID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/known-session" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"session_id":"known-session","project_id":"secret-project-name"}`)
	}))
	defer upstream.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	client := lethe.NewClient(upstream.URL, "test-key", time.Second, 1<<20)
	server := NewServer(
		&config.Config{MaxResultCount: 100},
		client,
		ledger.NewService(db),
		proposal.NewService(db, client),
		idempotency.NewService(db),
	)
	request := markmcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"session_id": "known-session"}
	ctx := WithAuth(context.Background(), auth.Principal{
		ID:       "limited-reader",
		Projects: map[string]struct{}{"allowed-project": {}},
		Scopes:   map[string]struct{}{"thread.read": {}},
	}, "request-1")

	_, err = server.handleThreadList(ctx, request)
	if err == nil {
		t.Fatal("expected thread access denial")
	}
	if strings.Contains(err.Error(), "secret-project-name") {
		t.Fatalf("authorization error leaked project ID: %v", err)
	}
	if !strings.Contains(err.Error(), "thread unavailable") {
		t.Fatalf("unexpected denial: %v", err)
	}
	deniedMessage := err.Error()

	request.Params.Arguments = map[string]interface{}{"session_id": "missing-session"}
	_, err = server.handleThreadList(ctx, request)
	if err == nil {
		t.Fatal("expected missing thread session error")
	}
	if err.Error() != deniedMessage {
		t.Fatalf("thread existence oracle: unauthorized=%q missing=%q", deniedMessage, err.Error())
	}
}
