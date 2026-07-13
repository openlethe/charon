package oauth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Handlers provides HTTP handlers for OAuth 2.0 endpoints.
type Handlers struct {
	service           *Service
	rateLimiter       *RateLimiter
	globalRateLimiter *RateLimiter // Prevents auto-registration bypass
}

// RateLimiter provides simple per-client rate limiting for the token endpoint.
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	window   time.Duration
	maxReqs  int
}

// NewRateLimiter creates a rate limiter.
func NewRateLimiter(window time.Duration, maxRequests int) *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string][]time.Time),
		window:   window,
		maxReqs:  maxRequests,
	}
}

// Allow checks if a client ID is within rate limits.
func (rl *RateLimiter) Allow(clientID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Filter out old attempts
	var recent []time.Time
	for _, t := range rl.attempts[clientID] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	if len(recent) >= rl.maxReqs {
		rl.attempts[clientID] = recent
		return false
	}

	recent = append(recent, now)
	rl.attempts[clientID] = recent
	return true
}

// NewHandlers creates OAuth HTTP handlers.
func NewHandlers(service *Service) *Handlers {
	return &Handlers{
		service:           service,
		rateLimiter:       NewRateLimiter(1*time.Minute, 60),  // Per client
		globalRateLimiter: NewRateLimiter(1*time.Minute, 120), // Global cap across all clients
	}
}

// RegisterRoutes registers OAuth routes on the provided mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", h.handleServerMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", h.handleProtectedResourceMetadata)
	mux.HandleFunc("/oauth/token", h.handleToken)
	mux.HandleFunc("/oauth/authorize", h.handleAuthorize)
}

// handleServerMetadata serves the OAuth 2.0 Authorization Server Metadata.
func (h *Handlers) handleServerMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	meta := h.service.AuthorizationServerMetadata()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// handleProtectedResourceMetadata serves the OAuth 2.0 Protected Resource Metadata.
func (h *Handlers) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	meta := h.service.ProtectedResourceMetadata()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// handleAuthorize handles the OAuth 2.0 authorization endpoint.
// For simplicity, this auto-approves the request and returns the code via redirect.
func (h *Handlers) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Parse query parameters
	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	codeChallenge := r.URL.Query().Get("code_challenge")
	codeChallengeMethod := r.URL.Query().Get("code_challenge_method")
	resource := r.URL.Query().Get("resource")

	if clientID == "" {
		http.Error(w, "missing client_id", http.StatusBadRequest)
		return
	}

	client := h.service.GetClient(clientID)
	if client == nil {
		// Auto-register unknown clients as public clients (MCP dynamic registration simplified)
		// Cap at 1000 auto-registered clients to prevent unbounded memory growth
		if h.service.AutoClientCount() >= 1000 {
				http.Error(w, "client registration limit reached", http.StatusServiceUnavailable)
				return
		}
		client = &Client{
			ID:           clientID,
			Name:         clientID,
			RedirectURIs: []string{},
			Public:       true,
		}
		h.service.RegisterClient(client)
	}

	// Validate redirect_uri
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	if len(client.RedirectURIs) > 0 {
		validRedirect := false
		for _, uri := range client.RedirectURIs {
			if redirectURI == uri {
				validRedirect = true
				break
			}
		}
		if !validRedirect {
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return
		}
	}

	// Validate resource parameter if present
	if resource != "" {
		expectedResource := h.service.issuer + "/mcp"
		if resource != expectedResource {
			// Return error to redirect_uri
			h.redirectError(w, r, redirectURI, "invalid_target", "invalid resource parameter", state)
			return
		}
	}

	// For public clients, PKCE is required
	if client.Public && codeChallenge == "" {
		h.redirectError(w, r, redirectURI, "invalid_request", "PKCE required for public clients", state)
		return
	}

	// For public clients, state is required for CSRF protection
	if client.Public && state == "" {
		h.redirectError(w, r, redirectURI, "invalid_request", "state parameter required for public clients", state)
		return
	}

	// Default PKCE method
	if codeChallengeMethod == "" {
		codeChallengeMethod = "S256"
	}
	if codeChallengeMethod != "S256" && codeChallengeMethod != "plain" {
		h.redirectError(w, r, redirectURI, "invalid_request", "unsupported code_challenge_method", state)
		return
	}

	// Generate authorization code
	// For a real implementation, we'd authenticate the user here.
	// For this simplified version, we auto-approve with a default user.
	userID := "chatgpt" // Default user for ChatGPT MCP
	code, err := h.service.GenerateAuthCode(clientID, userID, redirectURI, codeChallenge, codeChallengeMethod)
	if err != nil {
		h.redirectError(w, r, redirectURI, "server_error", err.Error(), state)
		return
	}

	// Redirect with authorization code
	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	q := redirectURL.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	redirectURL.RawQuery = q.Encode()

	w.Header().Set("Location", redirectURL.String())
	w.WriteHeader(http.StatusFound)
}

// handleToken handles the OAuth 2.0 token endpoint.
func (h *Handlers) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		WriteErrorResponse(w, "invalid_request", "failed to parse form", http.StatusBadRequest)
		return
	}

	grantType := r.PostFormValue("grant_type")
	clientID := r.PostFormValue("client_id")
	clientSecret := r.PostFormValue("client_secret")

	// Try to extract from Authorization header if not in form
	if clientID == "" {
		id, secret, ok := ParseBasicAuth(r)
		if ok {
			clientID = id
			clientSecret = secret
		}
	}

	// Global rate limit first (prevents auto-registration bypass)
	if !h.globalRateLimiter.Allow("global") {
		WriteErrorResponse(w, "slow_down", "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	client := h.service.GetClient(clientID)
	if client == nil {
		WriteErrorResponse(w, "invalid_client", "unknown client", http.StatusUnauthorized)
		return
	}

	// Rate limit token endpoint per client
	if !h.rateLimiter.Allow(clientID) {
		WriteErrorResponse(w, "slow_down", "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	// For confidential clients, verify secret
	if !client.Public && client.Secret != "" {
		if subtle.ConstantTimeCompare([]byte(clientSecret), []byte(client.Secret)) != 1 {
			WriteErrorResponse(w, "invalid_client", "invalid client credentials", http.StatusUnauthorized)
			return
		}
	}

	switch grantType {
	case "authorization_code":
		h.handleAuthorizationCodeGrant(w, r, client)
	case "client_credentials":
		h.handleClientCredentialsGrant(w, r, client)
	default:
		WriteErrorResponse(w, "unsupported_grant_type", "unsupported grant type", http.StatusBadRequest)
	}
}

func (h *Handlers) handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request, client *Client) {
	code := r.PostFormValue("code")
	redirectURI := r.PostFormValue("redirect_uri")
	codeVerifier := r.PostFormValue("code_verifier")

	if code == "" {
		WriteErrorResponse(w, "invalid_request", "missing code", http.StatusBadRequest)
		return
	}

	resp, err := h.service.ExchangeAuthCode(code, client.ID, redirectURI, codeVerifier)
	if err != nil {
		WriteErrorResponse(w, "invalid_grant", err.Error(), http.StatusBadRequest)
		return
	}

	WriteTokenResponse(w, resp)
}

func (h *Handlers) handleClientCredentialsGrant(w http.ResponseWriter, r *http.Request, client *Client) {
	// Public clients cannot use client_credentials grant — they cannot authenticate
	if client.Public {
		WriteErrorResponse(w, "unauthorized_client", "public clients are not allowed to use the client_credentials grant", http.StatusUnauthorized)
		return
	}

	// For client credentials, issue a token with limited scopes
	scopes := []string{"memory.read", "memory.search"}
	requestedScope := r.PostFormValue("scope")
	if requestedScope != "" {
		scopes = strings.Split(requestedScope, " ")
	}

	token, err := h.service.GenerateAccessToken(client.ID, client.ID, scopes)
	if err != nil {
		WriteErrorResponse(w, "server_error", err.Error(), http.StatusInternalServerError)
		return
	}

	WriteTokenResponse(w, &TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(h.service.tokenExpiry.Seconds()),
	})
}

func (h *Handlers) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, errorCode, description, state string) {
	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	q := redirectURL.Query()
	q.Set("error", errorCode)
	if description != "" {
		q.Set("error_description", description)
	}
	if state != "" {
		q.Set("state", state)
	}
	redirectURL.RawQuery = q.Encode()

	w.Header().Set("Location", redirectURL.String())
	w.WriteHeader(http.StatusFound)
}
