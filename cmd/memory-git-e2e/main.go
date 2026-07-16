package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

var expectedTools = []string{
	"memory_branch_create",
	"memory_changeset_create",
	"memory_context_at",
	"memory_diff",
	"memory_log",
	"memory_merge",
	"memory_merge_cancel",
	"memory_merge_propose",
	"memory_merge_review",
	"memory_ref_list",
	"memory_repo_init",
	"memory_show",
	"memory_status",
}

type options struct {
	endpoint            string
	letheURL            string
	project             string
	authorTokenFile     string
	reviewerTokenFile   string
	authorPrincipalFile string
	stateFile           string
	verifyState         bool
}

type ref struct {
	ProjectID       string `json:"project_id"`
	RefName         string `json:"ref_name"`
	HeadChangesetID string `json:"head_changeset_id"`
	Protected       bool   `json:"protected"`
}

type repoInitResponse struct {
	Ref *ref `json:"ref"`
}

type memoryRecord struct {
	MemoryID string `json:"memory_id"`
	Content  string `json:"content"`
}

type contextResponse struct {
	ProjectID       string         `json:"project_id"`
	RefName         string         `json:"ref_name"`
	HeadChangesetID string         `json:"head_changeset_id"`
	ManifestID      string         `json:"manifest_id"`
	Memories        []memoryRecord `json:"memories"`
}

type changesetResponse struct {
	ChangesetID string `json:"changeset_id"`
}

type proposalResponse struct {
	ProposalID      string `json:"proposal_id"`
	Status          string `json:"status"`
	TargetRef       string `json:"target_ref"`
	BaseChangesetID string `json:"base_changeset_id"`
	HeadChangesetID string `json:"head_changeset_id"`
}

type mergeResponse struct {
	ProposalID           string `json:"proposal_id"`
	Status               string `json:"status"`
	ResultingChangesetID string `json:"resulting_changeset_id"`
}

type refListResponse struct {
	Project string `json:"project"`
	Refs    []ref  `json:"refs"`
}

type manifestResponse struct {
	ManifestID        string   `json:"manifest_id"`
	ProjectID         string   `json:"project_id"`
	RefName           string   `json:"ref_name"`
	HeadChangesetID   string   `json:"head_changeset_id"`
	SelectedMemoryIDs []string `json:"selected_memory_ids"`
	SessionID         string   `json:"session_id"`
}

type persistedState struct {
	Project       string `json:"project"`
	RunID         string `json:"run_id"`
	BranchRef     string `json:"branch_ref"`
	MemoryID      string `json:"memory_id"`
	MemoryContent string `json:"memory_content"`
	AcceptedHead  string `json:"accepted_head"`
}

func main() {
	var opts options
	flag.StringVar(&opts.endpoint, "endpoint", "http://127.0.0.1:18487/mcp", "test Charon MCP endpoint")
	flag.StringVar(&opts.letheURL, "lethe-url", "http://127.0.0.1:18485", "Lethe Git URL used only to verify persisted manifests")
	flag.StringVar(&opts.project, "project", "lethe-git-e2e", "exact test project")
	flag.StringVar(&opts.authorTokenFile, "author-token-file", "./charon-memory-git-data/e2e-secrets/author.obol", "author Obol file")
	flag.StringVar(&opts.reviewerTokenFile, "reviewer-token-file", "./charon-memory-git-data/e2e-secrets/reviewer.obol", "reviewer Obol file")
	flag.StringVar(&opts.authorPrincipalFile, "author-principal-file", "./charon-memory-git-data/e2e-secrets/author-principal.txt", "author principal ID file")
	flag.StringVar(&opts.stateFile, "state-file", "./charon-memory-git-data/e2e-state.json", "non-secret persistence verification state")
	flag.BoolVar(&opts.verifyState, "verify-state", false, "verify a prior run after container restart")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "memory-git e2e failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	authorToken, err := readSecretFile(opts.authorTokenFile)
	if err != nil {
		return fmt.Errorf("read author Obol: %w", err)
	}
	authorPrincipal, err := readSecretFile(opts.authorPrincipalFile)
	if err != nil {
		return fmt.Errorf("read author principal: %w", err)
	}
	letheAPIKey := strings.TrimSpace(os.Getenv("LETHE_API_KEY"))
	if letheAPIKey == "" {
		return errors.New("LETHE_API_KEY must be present in the environment to verify stored manifests")
	}

	if opts.verifyState {
		state, err := readState(opts.stateFile)
		if err != nil {
			return err
		}
		if opts.project != state.Project {
			return fmt.Errorf("state project %q does not match requested project %q", state.Project, opts.project)
		}
		return verifyPersistence(ctx, opts, authorToken, letheAPIKey, state)
	}

	reviewerToken, err := readSecretFile(opts.reviewerTokenFile)
	if err != nil {
		return fmt.Errorf("read reviewer Obol: %w", err)
	}

	author, err := connect(ctx, opts.endpoint, authorToken, "memory-git-e2e-author")
	if err != nil {
		return err
	}
	defer author.Close()
	reviewer, err := connect(ctx, opts.endpoint, reviewerToken, "memory-git-e2e-reviewer")
	if err != nil {
		return err
	}
	defer reviewer.Close()

	if err := assertToolSurface(ctx, author); err != nil {
		return err
	}
	fmt.Println("PASS memory-git-only MCP tool surface")

	var initialized repoInitResponse
	if err := callJSON(ctx, author, "memory_repo_init", map[string]interface{}{
		"project": opts.project,
	}, &initialized); err != nil {
		return err
	}
	if initialized.Ref == nil || initialized.Ref.HeadChangesetID == "" || initialized.Ref.RefName != "refs/shared/main" || !initialized.Ref.Protected {
		return fmt.Errorf("unexpected repository init response: %#v", initialized)
	}
	rootHead := initialized.Ref.HeadChangesetID
	fmt.Println("PASS memory_repo_init created or reused protected refs/shared/main")

	runID := fmt.Sprintf("%x", time.Now().UTC().UnixNano())
	initialSession := "e2e-initial-" + runID
	var initialContext contextResponse
	if err := callJSON(ctx, author, "memory_context_at", map[string]interface{}{
		"project":    opts.project,
		"ref":        "refs/shared/main",
		"session_id": initialSession,
		"limit":      float64(20),
	}, &initialContext); err != nil {
		return err
	}
	if initialContext.HeadChangesetID != rootHead || initialContext.ManifestID == "" {
		return fmt.Errorf("initial context did not pin root head: %#v", initialContext)
	}
	if err := verifyManifest(ctx, opts.letheURL, letheAPIKey, initialContext.ManifestID, opts.project, rootHead, nil, initialSession); err != nil {
		return fmt.Errorf("verify initial manifest: %w", err)
	}
	fmt.Println("PASS initial memory_context_at pinned the exact shared head")

	branchRef := "refs/sessions/" + authorPrincipal + "/" + runID
	var branch ref
	if err := callJSON(ctx, author, "memory_branch_create", map[string]interface{}{
		"project":           opts.project,
		"ref_name":          branchRef,
		"head_changeset_id": rootHead,
	}, &branch); err != nil {
		return err
	}
	if branch.RefName != branchRef || branch.HeadChangesetID != rootHead || branch.Protected {
		return fmt.Errorf("unexpected branch response: %#v", branch)
	}
	fmt.Println("PASS owned session branch created from exact shared head")

	memoryID := "mem-e2e-" + runID
	memoryContent := "Lethe Git E2E accepted memory " + runID
	var commit changesetResponse
	if err := callJSON(ctx, author, "memory_changeset_create", map[string]interface{}{
		"project":         opts.project,
		"ref":             branchRef,
		"message":         "E2E semantic memory commit",
		"parent_ids":      []interface{}{rootHead},
		"expected_head":   rootHead,
		"idempotency_key": "e2e-commit-" + runID,
		"ops": []interface{}{map[string]interface{}{
			"op_type":            "add_memory",
			"resulting_event_id": memoryID,
			"payload": map[string]interface{}{
				"content":    memoryContent,
				"event_type": "record",
				"kind":       "decision",
				"scope":      opts.project,
				"tags":       []interface{}{"e2e", "memory-git"},
			},
		}},
	}, &commit); err != nil {
		return err
	}
	if commit.ChangesetID == "" || commit.ChangesetID == rootHead {
		return fmt.Errorf("invalid commit response: %#v", commit)
	}
	fmt.Println("PASS semantic changeset committed with compare-and-swap")

	var proposal proposalResponse
	if err := callJSON(ctx, author, "memory_merge_propose", map[string]interface{}{
		"project":           opts.project,
		"target_ref":        "refs/shared/main",
		"head_changeset_id": commit.ChangesetID,
		"base_changeset_id": rootHead,
		"message":           "E2E protected merge",
		"idempotency_key":   "e2e-proposal-" + runID,
	}, &proposal); err != nil {
		return err
	}
	if proposal.ProposalID == "" || proposal.Status != "pending" || proposal.HeadChangesetID != commit.ChangesetID {
		return fmt.Errorf("invalid merge proposal: %#v", proposal)
	}
	fmt.Println("PASS changeset proposed for protected merge")

	if err := expectToolError(ctx, author, "memory_merge_review", map[string]interface{}{
		"proposal_id": proposal.ProposalID,
		"verdict":     "approve",
		"comment":     "self review must fail",
	}, "missing required scope: memory.review"); err != nil {
		return err
	}
	fmt.Println("PASS self-review rejected: author lacks review authority")

	if err := expectToolError(ctx, author, "memory_merge", map[string]interface{}{
		"proposal_id":     proposal.ProposalID,
		"idempotency_key": "e2e-self-merge-" + runID,
	}, "missing required scope: memory.merge"); err != nil {
		return err
	}
	fmt.Println("PASS self-merge rejected: author lacks merge authority")

	if err := callJSON(ctx, reviewer, "memory_merge_review", map[string]interface{}{
		"proposal_id": proposal.ProposalID,
		"verdict":     "approve",
		"comment":     "independent E2E review: semantic change is scoped and expected",
		"severity":    "info",
	}, nil); err != nil {
		return err
	}
	fmt.Println("PASS separate reviewer recorded an explicit approval")

	var merged mergeResponse
	if err := callJSON(ctx, reviewer, "memory_merge", map[string]interface{}{
		"proposal_id":     proposal.ProposalID,
		"idempotency_key": "e2e-merge-" + runID,
	}, &merged); err != nil {
		return err
	}
	if merged.Status != "approved" || merged.ResultingChangesetID == "" {
		return fmt.Errorf("protected merge did not complete: %#v", merged)
	}
	fmt.Println("PASS protected merge advanced refs/shared/main")

	if err := author.Close(); err != nil {
		return fmt.Errorf("close first author session: %w", err)
	}
	author, err = connect(ctx, opts.endpoint, authorToken, "memory-git-e2e-new-session")
	if err != nil {
		return err
	}
	defer author.Close()

	acceptedSession := "e2e-accepted-" + runID
	accepted, err := pullAccepted(ctx, author, opts.project, memoryContent, acceptedSession)
	if err != nil {
		return err
	}
	if accepted.HeadChangesetID != merged.ResultingChangesetID {
		return fmt.Errorf("accepted context head %s does not match merge result %s", accepted.HeadChangesetID, merged.ResultingChangesetID)
	}
	if !containsMemory(accepted.Memories, memoryID) {
		return fmt.Errorf("accepted context did not select %s", memoryID)
	}
	if err := verifyManifest(ctx, opts.letheURL, letheAPIKey, accepted.ManifestID, opts.project, merged.ResultingChangesetID, []string{memoryID}, acceptedSession); err != nil {
		return fmt.Errorf("verify accepted manifest: %w", err)
	}
	fmt.Println("PASS new session pulled accepted memory and manifest pinned exact head + selected memory")

	state := persistedState{
		Project:       opts.project,
		RunID:         runID,
		BranchRef:     branchRef,
		MemoryID:      memoryID,
		MemoryContent: memoryContent,
		AcceptedHead:  merged.ResultingChangesetID,
	}
	if err := writeState(opts.stateFile, state); err != nil {
		return err
	}
	fmt.Printf("PASS saved non-secret restart verification state to %s\n", opts.stateFile)
	return nil
}

func verifyPersistence(ctx context.Context, opts options, authorToken, letheAPIKey string, state persistedState) error {
	author, err := connect(ctx, opts.endpoint, authorToken, "memory-git-e2e-persistence")
	if err != nil {
		return err
	}
	defer author.Close()
	if err := assertToolSurface(ctx, author); err != nil {
		return err
	}

	var refs refListResponse
	if err := callJSON(ctx, author, "memory_ref_list", map[string]interface{}{"project": state.Project}, &refs); err != nil {
		return err
	}
	if !containsRef(refs.Refs, "refs/shared/main", state.AcceptedHead) || !containsRef(refs.Refs, state.BranchRef, "") {
		return fmt.Errorf("persisted refs are missing or shared head changed: %#v", refs.Refs)
	}

	sessionID := "e2e-persisted-" + state.RunID
	accepted, err := pullAccepted(ctx, author, state.Project, state.MemoryContent, sessionID)
	if err != nil {
		return err
	}
	if accepted.HeadChangesetID != state.AcceptedHead || !containsMemory(accepted.Memories, state.MemoryID) {
		return fmt.Errorf("accepted memory did not persist: %#v", accepted)
	}
	if err := verifyManifest(ctx, opts.letheURL, letheAPIKey, accepted.ManifestID, state.Project, state.AcceptedHead, []string{state.MemoryID}, sessionID); err != nil {
		return err
	}
	fmt.Println("PASS restart persistence: refs, accepted head, selected memory, and exact manifest survived")
	return nil
}

func connect(ctx context.Context, endpoint, token, name string) (*client.Client, error) {
	c, err := client.NewStreamableHttpClient(endpoint, transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + token,
	}))
	if err != nil {
		return nil, fmt.Errorf("create MCP client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: name, Version: "1.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		initializeErr := fmt.Errorf("initialize MCP client %s: %w", name, err)
		if closeErr := c.Close(); closeErr != nil {
			return nil, errors.Join(initializeErr, fmt.Errorf("close failed MCP client: %w", closeErr))
		}
		return nil, initializeErr
	}
	return c, nil
}

func assertToolSurface(ctx context.Context, c *client.Client) error {
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	got := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), expectedTools...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("unexpected tool surface: got %v want %v", got, want)
	}
	return nil
}

func callJSON(ctx context.Context, c *client.Client, name string, args map[string]interface{}, out interface{}) error {
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = args
	result, err := c.CallTool(ctx, request)
	if err != nil {
		return fmt.Errorf("%s protocol call failed: %w", name, err)
	}
	text := resultText(result)
	if result.IsError {
		return fmt.Errorf("%s failed: %s", name, text)
	}
	if out == nil {
		return nil
	}
	if text == "" {
		return fmt.Errorf("%s returned no text content", name)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("decode %s response: %w; body=%q", name, err, text)
	}
	return nil
}

func expectToolError(ctx context.Context, c *client.Client, name string, args map[string]interface{}, contains string) error {
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = args
	result, err := c.CallTool(ctx, request)
	if err != nil {
		if strings.Contains(err.Error(), contains) {
			return nil
		}
		return fmt.Errorf("%s returned unexpected protocol error: %w", name, err)
	}
	text := resultText(result)
	if !result.IsError {
		return fmt.Errorf("%s unexpectedly succeeded: %s", name, text)
	}
	if !strings.Contains(text, contains) {
		return fmt.Errorf("%s error %q did not contain %q", name, text, contains)
	}
	return nil
}

func resultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func pullAccepted(ctx context.Context, c *client.Client, project, query, sessionID string) (contextResponse, error) {
	var accepted contextResponse
	err := callJSON(ctx, c, "memory_context_at", map[string]interface{}{
		"project":    project,
		"ref":        "refs/shared/main",
		"query":      query,
		"limit":      float64(20),
		"session_id": sessionID,
	}, &accepted)
	if err != nil {
		return contextResponse{}, err
	}
	if accepted.ManifestID == "" {
		return contextResponse{}, errors.New("memory_context_at did not create a manifest")
	}
	return accepted, nil
}

func verifyManifest(ctx context.Context, baseURL, apiKey, manifestID, project, head string, requiredMemoryIDs []string, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/memory/manifests/"+url.PathEscape(manifestID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("get manifest: %w", err)
	}
	defer resp.Body.Close()
	const maxManifestBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxManifestBody {
		return fmt.Errorf("manifest response exceeds %d bytes", maxManifestBody)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get manifest status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var manifest manifestResponse
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.ManifestID != manifestID || manifest.ProjectID != project || manifest.RefName != "refs/shared/main" || manifest.HeadChangesetID != head || manifest.SessionID != sessionID {
		return fmt.Errorf("manifest exact-state mismatch: %#v", manifest)
	}
	for _, memoryID := range requiredMemoryIDs {
		if !containsString(manifest.SelectedMemoryIDs, memoryID) {
			return fmt.Errorf("manifest %s did not select required memory %s: %v", manifestID, memoryID, manifest.SelectedMemoryIDs)
		}
	}
	return nil
}

func containsMemory(memories []memoryRecord, memoryID string) bool {
	for _, memory := range memories {
		if memory.MemoryID == memoryID {
			return true
		}
	}
	return false
}

func containsRef(refs []ref, name, head string) bool {
	for _, candidate := range refs {
		if candidate.RefName == name && (head == "" || candidate.HeadChangesetID == head) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readSecretFile(path string) (string, error) {
	// #nosec G304 -- this local E2E CLI reads only an explicit operator-supplied path.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("file is empty")
	}
	return value, nil
}

func writeState(path string, state persistedState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

func readState(path string) (persistedState, error) {
	// #nosec G304 -- this local E2E CLI reads only an explicit operator-supplied path.
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedState{}, fmt.Errorf("read state: %w", err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode state: %w", err)
	}
	if state.Project == "" || state.BranchRef == "" || state.MemoryID == "" || state.MemoryContent == "" || state.AcceptedHead == "" {
		return persistedState{}, errors.New("state file is incomplete")
	}
	return state, nil
}
