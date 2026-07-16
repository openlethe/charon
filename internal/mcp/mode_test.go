package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	markmcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/audit"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
)

func TestMemoryGitModeRegistersOnlyVersionedTools(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	srv := NewServer(
		&config.Config{Mode: config.ModeMemoryGit, MaxResultCount: 100},
		nil,
		ledger.NewService(db),
		proposal.NewService(db, nil),
		idempotency.NewService(db),
		audit.NewService(db),
	)
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()

	client, err := mcpclient.NewStreamableHttpClient(httpServer.URL)
	if err != nil {
		t.Fatalf("new MCP client: %v", err)
	}
	defer client.Close()

	initReq := markmcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = markmcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = markmcp.Implementation{Name: "mode-test", Version: "1.0.0"}
	if _, err := client.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	list, err := client.ListTools(context.Background(), markmcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) != 13 {
		t.Fatalf("tool count = %d, want 13", len(list.Tools))
	}

	names := make(map[string]bool, len(list.Tools))
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.Name == "memory_merge_review" {
			schemaBytes, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal review schema: %v", err)
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(schemaBytes, &schema); err != nil {
				t.Fatalf("decode review schema: %v", err)
			}
			if _, ok := schema.Properties["verdict"]; !ok {
				t.Fatal("memory_merge_review schema does not advertise verdict")
			}
			required := false
			for _, name := range schema.Required {
				if name == "verdict" {
					required = true
					break
				}
			}
			if !required {
				t.Fatal("memory_merge_review schema does not require verdict")
			}
		}
	}
	for _, required := range []string{
		"memory_repo_init", "memory_status", "memory_log", "memory_show", "memory_branch_create",
		"memory_diff", "memory_context_at", "memory_changeset_create",
		"memory_merge_propose", "memory_merge_review", "memory_merge",
		"memory_merge_cancel", "memory_ref_list",
	} {
		if !names[required] {
			t.Errorf("missing Memory Git tool %q", required)
		}
	}
	for _, forbidden := range []string{
		"memory_search", "memory_get", "memory_recent", "memory_propose",
		"memory_checkpoint", "memory_write", "thread_list",
	} {
		if names[forbidden] {
			t.Errorf("legacy compatibility tool %q registered in memory-git mode", forbidden)
		}
	}
}
