package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/store"
)

const testOAuthPairingSecret = "pairing-secret-at-least-32-characters"

func TestOAuthMCPReturnsV04LetheMemory(t *testing.T) {
	lethe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-private-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/events/search" || r.URL.Query().Get("q") != "Charon" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"count":1,"events":[{"event_id":"evt-contract","session_id":"sess-1","project_id":"future-project","event_type":"record","content":"Charon can see canonical Lethe memory","created_at":"2026-07-14T05:00:00Z"}]}`)
	}))
	defer lethe.Close()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Charon store: %v", err)
	}
	defer db.Close()
	if _, _, err := db.ReconcilePrincipal(context.Background(), "chatgpt", []string{"*"}, []string{"memory.search", "memory.read", "thread.read"}); err != nil {
		t.Fatalf("reconcile principal: %v", err)
	}

	cfg := &config.Config{
		Mode:               config.ModeDirect,
		Upstream:           lethe.URL,
		PublicURL:          "https://charon.example.test",
		OAuthClientID:      "chatgpt-mcp",
		OAuthDefaultUserID: "chatgpt",
		OAuthRedirectURIs:  []string{"http://127.0.0.1/callback"},
		OAuthPairingSecret: testOAuthPairingSecret,
		LetheAPIKey:        "upstream-private-key",
		HMACKey:            "0123456789abcdef0123456789abcdef",
		MaxResultBytes:     1 << 20,
		MaxResultCount:     100,
		RequestTimeout:     time.Second,
		AuthMode:           "oauth",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	charon, err := NewServer(cfg, db, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	handler, err := charon.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	public := httptest.NewServer(handler)
	defer public.Close()

	token := exchangeOAuthToken(t, public.URL)
	claims, err := charon.oauthSvc.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("validate OAuth token: %v", err)
	}
	if claims.UserID == "chatgpt" || !strings.HasPrefix(claims.UserID, "principal_") {
		t.Fatalf("OAuth subject is not the canonical principal ID: %q", claims.UserID)
	}
	mcpClient, err := client.NewStreamableHttpClient(public.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + token,
	}))
	if err != nil {
		t.Fatalf("new MCP client: %v", err)
	}
	defer mcpClient.Close()

	ctx := context.Background()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "charon-e2e", Version: "1.0.0"}
	if _, err := mcpClient.Initialize(ctx, initReq); err != nil {
		t.Fatalf("MCP initialize: %v", err)
	}
	tools, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("MCP list tools: %v", err)
	}
	if len(tools.Tools) != 23 {
		t.Fatalf("tool count = %d, want 23", len(tools.Tools))
	}

	request := mcp.CallToolRequest{}
	request.Params.Name = "memory_search"
	request.Params.Arguments = map[string]interface{}{"query": "Charon", "limit": 10}
	result, err := mcpClient.CallTool(ctx, request)
	if err != nil {
		t.Fatalf("MCP memory_search: %v", err)
	}
	payload, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal MCP result: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "evt-contract") || !strings.Contains(text, `\"count\": 1`) {
		t.Fatalf("MCP result did not contain the upstream memory: %s", text)
	}
}

func TestOAuthMemoryGitAuthorScopesOptInIsWiredEndToEnd(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Charon store: %v", err)
	}
	defer db.Close()
	if _, _, err := db.ReconcilePrincipal(context.Background(), "chatgpt-memory-git", []string{"Archimedes"}, []string{
		"memory.search", "memory.read", "thread.read", "memory.propose", "memory.branch", "memory.commit", "memory.write",
	}); err != nil {
		t.Fatalf("reconcile principal: %v", err)
	}

	cfg := &config.Config{
		Mode:                   config.ModeMemoryGit,
		Upstream:               "http://127.0.0.1:1",
		PublicURL:              "https://charon.example.test",
		OAuthClientID:          "chatgpt-mcp",
		OAuthDefaultUserID:     "chatgpt-memory-git",
		OAuthRedirectURIs:      []string{"http://127.0.0.1/callback"},
		OAuthAllowAuthorScopes: true,
		OAuthPairingSecret:     testOAuthPairingSecret,
		LetheAPIKey:            "upstream-test-key",
		HMACKey:                "0123456789abcdef0123456789abcdef",
		MaxResultBytes:         1 << 20,
		MaxResultCount:         100,
		RequestTimeout:         time.Second,
		AuthMode:               "oauth",
	}
	charon, err := NewServer(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	handler, err := charon.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	public := httptest.NewServer(handler)
	defer public.Close()

	token := exchangeOAuthToken(t, public.URL)
	claims, err := charon.oauthSvc.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("validate OAuth token: %v", err)
	}
	got := scopeSet(claims.Scopes)
	for _, scope := range []string{"memory.branch", "memory.commit", "memory.propose", "memory.read"} {
		if _, ok := got[scope]; !ok {
			t.Fatalf("OAuth token missing %s: %v", scope, claims.Scopes)
		}
	}
	if _, ok := got["memory.write"]; ok {
		t.Fatalf("OAuth token retained direct-write scope: %v", claims.Scopes)
	}
}

func TestOAuthMissingPrincipalFailsClosed(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Charon store: %v", err)
	}
	defer db.Close()
	cfg := &config.Config{
		Mode:               config.ModeDirect,
		Upstream:           "http://127.0.0.1:1",
		PublicURL:          "https://charon.example.test",
		OAuthClientID:      "chatgpt-mcp",
		OAuthDefaultUserID: "chatgpt",
		OAuthRedirectURIs:  []string{"http://127.0.0.1/callback"},
		OAuthPairingSecret: testOAuthPairingSecret,
		LetheAPIKey:        "upstream-test-key",
		HMACKey:            "0123456789abcdef0123456789abcdef",
		MaxResultBytes:     1 << 20,
		MaxResultCount:     100,
		RequestTimeout:     time.Second,
		AuthMode:           "oauth",
	}
	if _, err := NewServer(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "no principal found") {
		t.Fatalf("expected missing principal to block OAuth server startup, got %v", err)
	}
}

func TestScopeIntersectionHonorsCurrentGrantRevocation(t *testing.T) {
	current := map[string]struct{}{
		"memory.read":   {},
		"memory.search": {},
	}
	effective := intersectScopes(current, []string{"memory.read", "memory.search", "memory.propose", "memory.write"})
	if len(effective) != 2 {
		t.Fatalf("effective scopes = %v", effective)
	}
	if _, ok := effective["memory.write"]; ok {
		t.Fatal("revoked memory.write scope remained effective")
	}
	if _, ok := effective["memory.propose"]; ok {
		t.Fatal("revoked memory.propose scope remained effective")
	}
}

func exchangeOAuthToken(t *testing.T, baseURL string) string {
	t.Helper()
	verifier := strings.Repeat("v", 64)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	redirectURI := "http://127.0.0.1/callback"
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {"chatgpt-mcp"},
		"redirect_uri":          {redirectURI},
		"state":                 {"test-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := httpClient.Get(baseURL + "/oauth/authorize?" + params.Encode())
	if err != nil {
		t.Fatalf("authorize page: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Authorization key") {
		t.Fatalf("authorize page status = %d: %s", resp.StatusCode, body)
	}

	params.Set("pairing_secret", testOAuthPairingSecret)
	resp, err = httpClient.PostForm(baseURL+"/oauth/authorize", params)
	if err != nil {
		t.Fatalf("authorize pairing: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize pairing status = %d", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	code := location.Query().Get("code")
	if code == "" || location.Query().Get("state") != "test-state" {
		t.Fatalf("invalid authorize redirect: %s", location)
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"chatgpt-mcp"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	resp, err = http.PostForm(baseURL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("token exchange: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("token status = %d: %s", resp.StatusCode, body)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if tokenResponse.AccessToken == "" {
		t.Fatal("empty access token")
	}
	return tokenResponse.AccessToken
}
