package oauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Handlers provides HTTP handlers for OAuth 2.0 endpoints.
type Handlers struct {
	service           *Service
	rateLimiter       *RateLimiter
	globalRateLimiter *RateLimiter
	authorizeLimiter  *RateLimiter
	defaultUserID     string
	allowedRedirects  []string
	pairingSecret     []byte
}

// RateLimiter provides simple per-client rate limiting for the token endpoint.
type RateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	window      time.Duration
	maxReqs     int
	lastCleanup time.Time
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
	if rl.lastCleanup.IsZero() || now.Sub(rl.lastCleanup) >= rl.window {
		for key, attempts := range rl.attempts {
			kept := attempts[:0]
			for _, attempt := range attempts {
				if attempt.After(cutoff) {
					kept = append(kept, attempt)
				}
			}
			if len(kept) == 0 {
				delete(rl.attempts, key)
			} else {
				rl.attempts[key] = kept
			}
		}
		rl.lastCleanup = now
	}

	// Filter out old attempts for this key.
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
func NewHandlers(service *Service, defaultUserID string, allowedRedirects []string, pairingSecret string) *Handlers {
	return &Handlers{
		service:           service,
		defaultUserID:     defaultUserID,
		allowedRedirects:  append([]string(nil), allowedRedirects...),
		pairingSecret:     []byte(pairingSecret),
		rateLimiter:       NewRateLimiter(1*time.Minute, 60),
		globalRateLimiter: NewRateLimiter(1*time.Minute, 120),
		authorizeLimiter:  NewRateLimiter(1*time.Minute, 20),
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
	if err := json.NewEncoder(w).Encode(meta); err != nil {
		return
	}
}

// handleProtectedResourceMetadata serves the OAuth 2.0 Protected Resource Metadata.
func (h *Handlers) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	meta := h.service.ProtectedResourceMetadata()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(meta); err != nil {
		return
	}
}

type authorizationRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
}

type authorizationPageData struct {
	authorizationRequest
	CSPNonce string
	Error    string
}

var pairingPage = template.Must(template.New("oauth-pairing").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Authorize Charon</title>
  <style>
    :root {
      --bg: #f5f0e8;
      --surface: #faf7f2;
      --surface2: #f0ebe0;
      --border: #ddd5c4;
      --border2: #c9bfac;
      --text: #2c2520;
      --muted: #8a7d6b;
      --accent: #3d5a73;
      --accent2: #2a3f52;
      --green: #3a7d5e;
      --red: #b83232;
      --mono: "JetBrains Mono", "SFMono-Regular", Consolas, monospace;
      --sans: Syne, Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 28px 18px;
      color: var(--text);
      font-family: var(--sans);
      background-color: var(--bg);
      background-image:
        url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='400' height='400'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.75' numOctaves='4' stitchTiles='stitch'/%3E%3CfeColorMatrix type='saturate' values='0'/%3E%3C/filter%3E%3Crect width='400' height='400' filter='url(%23n)' opacity='0.045'/%3E%3C/svg%3E"),
        url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='200' height='200'%3E%3Cfilter id='n2'%3E%3CfeTurbulence type='turbulence' baseFrequency='0.35' numOctaves='2' stitchTiles='stitch'/%3E%3CfeColorMatrix type='saturate' values='0'/%3E%3C/filter%3E%3Crect width='200' height='200' filter='url(%23n2)' opacity='0.025'/%3E%3C/svg%3E");
    }
    main { width: min(100%, 680px); }
    .brand {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 14px;
      color: var(--accent);
      font-size: 15px;
      font-weight: 700;
      letter-spacing: -0.02em;
    }
    .status {
      display: inline-flex;
      align-items: center;
      gap: 7px;
      padding: 5px 10px;
      border: 1px solid rgba(58,125,94,.25);
      border-radius: 999px;
      background: rgba(58,125,94,.1);
      color: var(--green);
      font: 600 11px/1 var(--mono);
      letter-spacing: .02em;
    }
    .status::before {
      width: 6px;
      height: 6px;
      border-radius: 50%;
      background: currentColor;
      content: "";
    }
    .card {
      overflow: hidden;
      border: 1px solid var(--border);
      border-radius: 12px;
      background: rgba(250,247,242,.96);
      box-shadow: 0 20px 55px rgba(44,37,32,.11);
      backdrop-filter: blur(12px);
    }
    .card-header {
      padding: 24px 26px 20px;
      border-bottom: 1px solid var(--border);
    }
    h1 {
      margin: 0;
      color: var(--text);
      font-size: clamp(24px, 4vw, 32px);
      line-height: 1.15;
      letter-spacing: -.035em;
    }
    .lede {
      margin: 10px 0 0;
      color: var(--muted);
      font-size: 14px;
      line-height: 1.65;
    }
    .card-body { padding: 22px 26px 26px; }
    dl {
      display: grid;
      gap: 0;
      margin: 0 0 22px;
      border: 1px solid var(--border);
      border-radius: 9px;
      background: var(--surface2);
      overflow: hidden;
    }
    .detail {
      display: grid;
      grid-template-columns: 145px minmax(0, 1fr);
      gap: 16px;
      padding: 12px 14px;
      border-bottom: 1px solid var(--border);
      align-items: start;
    }
    .detail:last-child { border-bottom: 0; }
    dt {
      color: var(--muted);
      font-size: 11px;
      font-weight: 600;
      letter-spacing: .08em;
      text-transform: uppercase;
    }
    dd { min-width: 0; margin: 0; }
    code {
      color: var(--text);
      font: 12px/1.55 var(--mono);
      overflow-wrap: anywhere;
    }
    .field-label {
      display: block;
      margin-bottom: 8px;
      color: var(--text);
      font-size: 12px;
      font-weight: 600;
    }
    .field-help {
      margin: 0 0 12px;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.55;
    }
    input[type="password"] {
      width: 100%;
      padding: 12px 13px;
      border: 1px solid var(--border2);
      border-radius: 8px;
      outline: none;
      background: var(--surface);
      color: var(--text);
      font: 13px/1.3 var(--mono);
      letter-spacing: .02em;
      transition: border-color 150ms, box-shadow 150ms;
    }
    input[type="password"]:focus {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(61,90,115,.12);
    }
    .error {
      margin: 0 0 18px;
      padding: 13px 14px;
      border: 1px solid rgba(184,50,50,.32);
      border-radius: 8px;
      background: rgba(184,50,50,.09);
      color: var(--red);
      font-size: 12px;
      font-weight: 600;
      line-height: 1.55;
    }
    .feedback {
      margin: 12px 0 0;
      padding: 11px 13px;
      border: 1px solid rgba(61,90,115,.24);
      border-radius: 8px;
      background: rgba(61,90,115,.08);
      color: var(--accent2);
      font-size: 12px;
      line-height: 1.5;
    }
    .feedback[hidden] { display: none; }
    button {
      width: 100%;
      margin-top: 14px;
      padding: 11px 16px;
      border: 1px solid var(--accent2);
      border-radius: 8px;
      background: var(--accent);
      color: #fff;
      font: 600 13px/1 var(--sans);
      cursor: pointer;
      transition: transform 120ms, background 150ms, box-shadow 150ms;
    }
    button:hover { background: var(--accent2); box-shadow: 0 8px 20px rgba(42,63,82,.18); }
    button:active { transform: translateY(1px); }
    button:disabled {
      cursor: progress;
      opacity: .78;
      transform: none;
      box-shadow: none;
    }
    .footer {
      margin: 15px 0 0;
      color: var(--muted);
      font: 11px/1.55 var(--mono);
      text-align: center;
    }
    @media (max-width: 560px) {
      .card-header, .card-body { padding-left: 18px; padding-right: 18px; }
      .detail { grid-template-columns: 1fr; gap: 5px; }
    }
  </style>
</head>
<body>
  <main>
    <div class="brand">
      <span>Charon</span>
      <span class="status">OAuth pairing</span>
    </div>
    <section class="card" aria-labelledby="page-title">
      <header class="card-header">
        <h1 id="page-title">Authorize ChatGPT</h1>
        <p class="lede">Only continue when these details match the connector you are configuring.</p>
      </header>
      <div class="card-body">
        {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
        <dl>
          <div class="detail"><dt>Client ID</dt><dd><code>{{.ClientID}}</code></dd></div>
          <div class="detail"><dt>Redirect destination</dt><dd><code>{{.RedirectURI}}</code></dd></div>
          <div class="detail"><dt>Protected resource</dt><dd><code>{{.Resource}}</code></dd></div>
        </dl>
        <form id="authorization-form" method="post" action="/oauth/authorize" autocomplete="off">
          <input type="hidden" name="response_type" value="{{.ResponseType}}">
          <input type="hidden" name="client_id" value="{{.ClientID}}">
          <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
          <input type="hidden" name="state" value="{{.State}}">
          <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
          <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
          <input type="hidden" name="resource" value="{{.Resource}}">
          <label class="field-label" for="authorization-key">Authorization key</label>
          <p class="field-help">Use the active key for this Charon process. The primary Compose profile prints a fresh key once in the container startup log; static-key deployments use their configured value.</p>
          <input id="authorization-key" type="password" name="pairing_secret" required autofocus autocomplete="one-time-code" autocapitalize="none" spellcheck="false" {{if .Error}}aria-invalid="true"{{end}}>
          <button id="authorize-button" type="submit">Authorize connector</button>
          <p id="submission-feedback" class="feedback" role="status" aria-live="polite" hidden>Authorization key submitted. Waiting for ChatGPT to complete the connection…</p>
        </form>
      </div>
    </section>
    <p class="footer">No credentials are sent anywhere except this Charon authorization endpoint.</p>
  </main>
  <script nonce="{{.CSPNonce}}">
    (() => {
      const form = document.getElementById("authorization-form");
      const button = document.getElementById("authorize-button");
      const feedback = document.getElementById("submission-feedback");
      form.addEventListener("submit", () => {
        button.disabled = true;
        button.textContent = "Authorizing…";
        feedback.hidden = false;
      });
    })();
  </script>
</body>
</html>`))

// handleAuthorize validates the OAuth request, then requires an operator-held
// pairing secret before issuing a code for the configured principal.
func (h *Handlers) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	cspNonce, err := newOAuthBrowserNonce()
	if err != nil {
		http.Error(w, "authorization page unavailable", http.StatusInternalServerError)
		return
	}
	setOAuthBrowserHeaders(w, cspNonce, h.allowedRedirects)

	var values url.Values
	switch r.Method {
	case http.MethodGet:
		values = r.URL.Query()
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid authorization request", http.StatusBadRequest)
			return
		}
		values = r.PostForm
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	req := authorizationRequest{
		ResponseType:        values.Get("response_type"),
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		State:               values.Get("state"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		Resource:            values.Get("resource"),
	}
	if req.CodeChallengeMethod == "" {
		req.CodeChallengeMethod = "S256"
	}
	client, ok := h.validateAuthorizationRequest(w, r, req)
	if !ok {
		return
	}

	if r.Method == http.MethodGet {
		renderAuthorizationPage(w, http.StatusOK, authorizationPageData{authorizationRequest: req, CSPNonce: cspNonce})
		return
	}

	if !h.authorizeLimiter.Allow(rateLimitAddress(r.RemoteAddr)) {
		http.Error(w, "authorization rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	provided := []byte(strings.TrimSpace(values.Get("pairing_secret")))
	if len(provided) != len(h.pairingSecret) || subtle.ConstantTimeCompare(provided, h.pairingSecret) != 1 {
		renderAuthorizationPage(w, http.StatusUnauthorized, authorizationPageData{
			authorizationRequest: req,
			CSPNonce:             cspNonce,
			Error:                "Authorization failed: that key does not match the active Charon process. Copy the newest key from the boxed startup banner and try again.",
		})
		return
	}

	userID := h.defaultUserID
	if userID == "" {
		http.Error(w, "authorization principal is not configured", http.StatusServiceUnavailable)
		return
	}
	code, err := h.service.GenerateAuthCode(
		client.ID, userID, req.RedirectURI, req.CodeChallenge, req.CodeChallengeMethod,
	)
	if err != nil {
		h.redirectError(w, r, req.RedirectURI, "server_error", "authorization failed", req.State)
		return
	}
	h.redirectCode(w, req.RedirectURI, code, req.State)
}

func (h *Handlers) validateAuthorizationRequest(w http.ResponseWriter, r *http.Request, req authorizationRequest) (*Client, bool) {
	if req.ResponseType != "code" {
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return nil, false
	}
	if req.ClientID == "" {
		http.Error(w, "missing client_id", http.StatusBadRequest)
		return nil, false
	}
	if req.ClientID != h.service.OAuthClientID() {
		http.Error(w, "invalid client_id", http.StatusUnauthorized)
		return nil, false
	}
	client := h.service.GetClient(req.ClientID)
	if client == nil {
		http.Error(w, "invalid client_id", http.StatusUnauthorized)
		return nil, false
	}
	if req.RedirectURI == "" || !redirectAllowed(req.RedirectURI, h.allowedRedirects) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return nil, false
	}
	if len(client.RedirectURIs) > 0 {
		allowed := false
		for _, uri := range client.RedirectURIs {
			if req.RedirectURI == uri {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return nil, false
		}
	}
	if req.Resource != "" && req.Resource != h.service.issuer+"/mcp" {
		h.redirectError(w, r, req.RedirectURI, "invalid_target", "invalid resource parameter", req.State)
		return nil, false
	}
	if client.Public && req.CodeChallenge == "" {
		h.redirectError(w, r, req.RedirectURI, "invalid_request", "PKCE required for public clients", req.State)
		return nil, false
	}
	if client.Public && req.State == "" {
		h.redirectError(w, r, req.RedirectURI, "invalid_request", "state parameter required for public clients", req.State)
		return nil, false
	}
	if req.CodeChallengeMethod != "S256" {
		h.redirectError(w, r, req.RedirectURI, "invalid_request", "S256 PKCE required", req.State)
		return nil, false
	}
	return client, true
}

func (h *Handlers) redirectCode(w http.ResponseWriter, redirectURI, code, state string) {
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

func renderAuthorizationPage(w http.ResponseWriter, status int, data authorizationPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pairingPage.Execute(w, data); err != nil {
		return
	}
}

func setOAuthBrowserHeaders(w http.ResponseWriter, cspNonce string, allowedRedirects []string) {
	formActions := []string{"'self'"}
	seen := map[string]struct{}{"'self'": {}}
	for _, allowed := range allowedRedirects {
		allowedURL, err := url.Parse(strings.TrimSuffix(allowed, "*"))
		if err != nil || allowedURL.Host == "" || allowedURL.User != nil {
			continue
		}
		if allowedURL.Scheme != "https" && allowedURL.Scheme != "http" {
			continue
		}
		origin := allowedURL.Scheme + "://" + allowedURL.Host
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		formActions = append(formActions, origin)
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'nonce-"+cspNonce+"'; connect-src 'self'; form-action "+strings.Join(formActions, " ")+"; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func newOAuthBrowserNonce() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func rateLimitAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}

func redirectAllowed(candidate string, allowlist []string) bool {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || !canonicalRedirectPath(parsed) {
		return false
	}
	for _, allowed := range allowlist {
		wildcard := strings.HasSuffix(allowed, "*")
		if wildcard {
			allowed = strings.TrimSuffix(allowed, "*")
		}
		allowedURL, err := url.Parse(allowed)
		if err != nil || allowedURL.Scheme == "" || allowedURL.Host == "" || allowedURL.User != nil || !canonicalRedirectPath(allowedURL) {
			continue
		}
		if parsed.Scheme != allowedURL.Scheme || !strings.EqualFold(parsed.Host, allowedURL.Host) {
			continue
		}
		if wildcard {
			if strings.HasPrefix(parsed.EscapedPath(), allowedURL.EscapedPath()) {
				return true
			}
			continue
		}
		if candidate == allowed {
			return true
		}
	}
	return false
}

func canonicalRedirectPath(value *url.URL) bool {
	decoded := value.Path
	if decoded == "" {
		decoded = "/"
	}
	if strings.Contains(decoded, "\\") {
		return false
	}
	escaped := strings.ToLower(value.EscapedPath())
	for _, encoded := range []string{"%2e", "%2f", "%5c"} {
		if strings.Contains(escaped, encoded) {
			return false
		}
	}
	cleaned := path.Clean(decoded)
	if strings.HasSuffix(decoded, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned == decoded
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
	if !client.Public {
		if client.Secret == "" || subtle.ConstantTimeCompare([]byte(clientSecret), []byte(client.Secret)) != 1 {
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
		WriteErrorResponse(w, "invalid_grant", "authorization code exchange failed", http.StatusBadRequest)
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
		WriteErrorResponse(w, "invalid_scope", "custom client-credential scopes are not supported", http.StatusBadRequest)
		return
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
