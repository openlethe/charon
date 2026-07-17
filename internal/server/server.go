package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openlethe/charon/internal/audit"
	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/mcp"
	"github.com/openlethe/charon/internal/metrics"
	"github.com/openlethe/charon/internal/oauth"
	"github.com/openlethe/charon/internal/obol"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

// Server is the Charon HTTP server.
type Server struct {
	cfg                     *config.Config
	db                      *store.DB
	obolSvc                 *obol.Service
	oauthSvc                *oauth.Service
	oauthDefaultPrincipalID string
	ledgerSvc               *ledger.Service
	auditSvc                *audit.Service
	lethe                   *lethe.Client
	mcpServer               *mcp.Server
	logger                  *slog.Logger
}

// NewServer creates a Charon server.
func NewServer(cfg *config.Config, db *store.DB, logger *slog.Logger) (*Server, error) {
	obolSvc := obol.NewService(db, cfg.ObolKey())
	ledgerSvc := ledger.NewService(db)
	if key := cfg.LedgerKey(); len(key) >= 32 {
		ledgerSvc.SetIntegrityKey([]byte(key), cfg.LedgerKeyID)
	} else {
		logger.Warn("ledger integrity key missing or shorter than 32 characters; ledger entries are hash-chained but not MAC-signed")
	}
	ledgerSvc.SetAnchorPath(cfg.LedgerAnchorPath)
	auditSvc := audit.NewService(db)
	if _, err := auditSvc.RegisterInstance(context.Background()); err != nil {
		return nil, fmt.Errorf("audit instance registration: %w", err)
	}
	// Restart reconciliation is ownership-aware: only intents from dead
	// instances are retired, so a sibling gateway's in-flight mutations are
	// never mislabeled when this process restarts alongside it.
	if reconciled, err := auditSvc.ReconcileAfterRestart(context.Background(), audit.IntentStaleAfter); err != nil {
		return nil, fmt.Errorf("audit outbox reconciliation: %w", err)
	} else if reconciled > 0 {
		logger.Warn("audit intents from dead instances marked ambiguous; investigate before relying on affected resources",
			"reconciled", reconciled)
	}
	letheClient := lethe.NewClient(cfg.Upstream, cfg.LetheAPIKey, cfg.RequestTimeout, int64(cfg.MaxResultBytes))
	letheClient.SetMergeSigningKey(cfg.MergeKey())
	letheClient.SetMergeSigningKeyID(cfg.MergeHMACKeyID)
	letheClient.SetMergeAuthorizationTTL(cfg.MergeAuthorizationTTL)
	proposalSvc := proposal.NewService(db, letheClient)
	migrated, quarantined, err := proposalSvc.MigrateStateDigests(context.Background())
	if err != nil {
		return nil, fmt.Errorf("proposal state digest migration: %w", err)
	}
	if migrated > 0 || quarantined > 0 {
		logger.Info("proposal state digest migration complete",
			"migrated", migrated, "quarantined", quarantined)
	}
	if quarantined > 0 {
		logger.Warn("legacy proposals without verifiable state digest are quarantined and fail closed",
			"quarantined", quarantined)
	}
	idempotencySvc := idempotency.NewService(db)
	mcpServer := mcp.NewServer(cfg, letheClient, ledgerSvc, proposalSvc, idempotencySvc, auditSvc)

	// Initialize OAuth service with HMAC key and client ID
	var oauthSvc *oauth.Service
	if cfg.OAuthKey() != "" {
		var err error
		oauthSvc, err = oauth.NewService([]byte(cfg.OAuthKey()), cfg.PublicURL, cfg.OAuthClientID)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize OAuth service: %w", err)
		}
		oauthSvc.SetAllowAuthorScopes(cfg.OAuthAllowAuthorScopes)
	}

	s := &Server{
		cfg:       cfg,
		db:        db,
		obolSvc:   obolSvc,
		oauthSvc:  oauthSvc,
		ledgerSvc: ledgerSvc,
		auditSvc:  auditSvc,
		lethe:     letheClient,
		mcpServer: mcpServer,
		logger:    logger,
	}

	// Wire up principal scope resolution so tokens carry the principal's
	// actual grants from the database instead of a hardcoded list.
	if oauthSvc != nil {
		oauthSvc.SetScopeLookup(func(userID string) ([]string, error) {
			return s.lookupPrincipalScopes(userID)
		})
	}
	if cfg.AuthMode == "oauth" {
		principal, err := s.loadPrincipalByIDOrName(context.Background(), cfg.OAuthDefaultUserID)
		if err != nil {
			return nil, fmt.Errorf("resolve OAuth default principal: %w", err)
		}
		s.oauthDefaultPrincipalID = principal.ID
	}

	return s, nil
}

// lookupPrincipalScopes fetches a principal's scopes from the database.
// It tries principal_id first, then falls back to name lookup.
// Missing and ambiguous principals are authorization errors: OAuth must never
// mint usable read scopes without a reconciled database principal.
func (s *Server) lookupPrincipalScopes(principalID string) ([]string, error) {
	ctx := context.Background()

	p, err := s.loadPrincipalByIDOrName(ctx, principalID)
	if err != nil {
		return nil, err
	}

	scopes := make([]string, 0, len(p.Scopes))
	for sc := range p.Scopes {
		scopes = append(scopes, sc)
	}
	return scopes, nil
}

// Start serves the public and local-admin endpoints until the context is
// cancelled or either listener fails. Both servers are shut down together.
func (s *Server) Start(ctx context.Context) error {
	publicHandler, err := s.Handler()
	if err != nil {
		return err
	}

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin/livez", s.handleLiveness)
	adminMux.HandleFunc("/admin/readyz", s.handleReadiness)
	adminMux.HandleFunc("/admin/health", s.handleReadiness)
	adminMux.HandleFunc("/admin/metrics", s.handleMetrics)

	publicListener, err := net.Listen("tcp", s.cfg.HTTP)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	adminNetwork := "tcp"
	if strings.HasPrefix(s.cfg.AdminHTTP, "/") || strings.HasPrefix(s.cfg.AdminHTTP, "./") {
		adminNetwork = "unix"
		if err := prepareUnixSocket(s.cfg.AdminHTTP); err != nil {
			prepareErr := fmt.Errorf("prepare admin socket: %w", err)
			if closeErr := publicListener.Close(); closeErr != nil {
				return errors.Join(prepareErr, fmt.Errorf("close public listener: %w", closeErr))
			}
			return prepareErr
		}
	}
	adminListener, err := net.Listen(adminNetwork, s.cfg.AdminHTTP)
	if err != nil {
		listenErr := fmt.Errorf("admin listener: %w", err)
		if closeErr := publicListener.Close(); closeErr != nil {
			return errors.Join(listenErr, fmt.Errorf("close public listener: %w", closeErr))
		}
		return listenErr
	}

	publicServer := &http.Server{
		Handler:           publicHandler,
		ReadHeaderTimeout: s.cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       s.cfg.HTTPReadTimeout,
		IdleTimeout:       s.cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    s.cfg.HTTPMaxHeaderBytes,
	}
	adminServer := &http.Server{
		Handler:           adminMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	// Keep this instance's audit ownership alive so sibling reconciliation
	// never retires our in-flight intents. Periodic reconciliation runs with
	// it: a process that crashed between intent and outcome and restarted
	// before its heartbeat went stale still gets its stranded intents marked
	// ambiguous once the dead owner's lease expires.
	go func() {
		ticker := time.NewTicker(audit.HeartbeatInterval)
		defer ticker.Stop()
		reconcileEvery := int(audit.IntentStaleAfter / audit.HeartbeatInterval)
		if reconcileEvery < 1 {
			reconcileEvery = 1
		}
		ticks := 0
		for {
			select {
			case <-ticker.C:
				hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := s.auditSvc.Heartbeat(hbCtx); err != nil {
					s.logger.Warn("audit ownership heartbeat failed", "error", sanitizeHealthError(err))
				}
				cancel()
				ticks++
				if ticks%reconcileEvery == 0 {
					rcCtx, rcCancel := context.WithTimeout(context.Background(), 10*time.Second)
					if reconciled, err := s.auditSvc.ReconcileAfterRestart(rcCtx, audit.IntentStaleAfter); err != nil {
						s.logger.Warn("periodic audit reconciliation failed", "error", sanitizeHealthError(err))
					} else if reconciled > 0 {
						s.logger.Warn("periodic reconciliation marked dead-owner audit intents ambiguous",
							"reconciled", reconciled)
					}
					rcCancel()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	errCh := make(chan error, 2)
	go func() { errCh <- publicServer.Serve(publicListener) }()
	go func() { errCh <- adminServer.Serve(adminListener) }()

	s.logger.Info("charon listening", "addr", publicListener.Addr().String(), "auth_mode", s.cfg.AuthMode)
	s.logger.Info("admin listening", "network", adminNetwork, "addr", s.cfg.AdminHTTP)

	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.HTTPShutdownTimeout)
		defer cancel()
		publicErr := publicServer.Shutdown(shutdownCtx)
		adminErr := adminServer.Shutdown(shutdownCtx)
		if publicErr != nil {
			return fmt.Errorf("public shutdown: %w", publicErr)
		}
		if adminErr != nil {
			return fmt.Errorf("admin shutdown: %w", adminErr)
		}
		return nil
	}

	select {
	case <-ctx.Done():
		return shutdown()
	case serveErr := <-errCh:
		_ = shutdown()
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}

func prepareUnixSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %q", path)
	}
	return os.Remove(path)
}

// Handler builds Charon's public HTTP surface. It is exposed so integration
// tests can exercise the complete OAuth -> MCP -> Lethe path on an ephemeral
// listener without starting the admin server.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()

	// MCP endpoint — auth controlled by config
	switch s.cfg.AuthMode {
	case "none":
		s.logger.Warn("MCP endpoint running WITHOUT authentication — testing only")
		mux.Handle("/mcp", s.mcpServer)
	case "oauth":
		if s.oauthSvc == nil {
			return nil, fmt.Errorf("oauth service not initialized (HMAC key required)")
		}
		mux.Handle("/mcp", s.withOAuthAuth(s.mcpServer))
		// Register OAuth routes
		oauthHandlers := oauth.NewHandlers(
			s.oauthSvc, s.oauthDefaultPrincipalID, s.cfg.OAuthRedirectURIs, s.cfg.OAuthPairingSecret,
		)
		oauthHandlers.RegisterRoutes(mux)
		s.logger.Info("OAuth 2.0 endpoints registered")
	default:
		mux.Handle("/mcp", s.withObolAuth(s.mcpServer))
	}

	// Liveness is process-local; readiness includes the private Lethe upstream.
	mux.HandleFunc("/livez", s.handleLiveness)
	mux.HandleFunc("/readyz", s.handleReadiness)
	mux.HandleFunc("/health", s.handleReadiness)
	return limitRequestBody(mux, s.cfg.MaxRequestBytes), nil
}

func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// withObolAuth validates Obol tokens (legacy, default).
func (s *Server) withObolAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID, err := generateRequestID()
		if err != nil {
			s.logger.Error("request ID generation failed", "error", err)
			http.Error(w, `{"error": "internal error"}`, http.StatusInternalServerError)
			return
		}

		// Extract bearer token
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			s.logger.Warn("missing bearer token", "request_id", reqID)
			http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Verify obol
		principalID, err := s.obolSvc.Verify(r.Context(), token, s.cfg.PublicURL)
		if err != nil {
			s.logger.Warn("invalid obol", "request_id", reqID, "error", err)
			http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Load principal
		principal, err := s.loadPrincipal(r.Context(), principalID)
		if err != nil {
			s.logger.Error("load principal failed", "request_id", reqID, "error", err)
			http.Error(w, `{"error": "internal error"}`, http.StatusInternalServerError)
			return
		}

		s.logger.Info("authenticated request",
			"request_id", reqID,
			"principal", principal.ID,
			"operation", r.Method,
			"path", r.URL.Path,
		)

		ctx := mcp.WithAuth(r.Context(), principal, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withOAuthAuth validates OAuth 2.0 access tokens.
func (s *Server) withOAuthAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID, err := generateRequestID()
		if err != nil {
			s.logger.Error("request ID generation failed", "error", err)
			http.Error(w, `{"error": "internal error"}`, http.StatusInternalServerError)
			return
		}

		// Allow CORS preflight requests
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, MCP-Protocol-Version")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Extract bearer token
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			// Return 401 with WWW-Authenticate for OAuth discovery per MCP spec
			metadataURL := s.oauthSvc.OAuthMetadataURL()
			clientID := s.oauthSvc.OAuthClientID()
			challenge := `Bearer resource_metadata="` + metadataURL + `"`
			if clientID != "" {
				challenge += `, client_id="` + clientID + `"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			s.logger.Warn("missing bearer token", "request_id", reqID)
			http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate OAuth access token
		claims, err := s.oauthSvc.ValidateAccessToken(token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			s.logger.Warn("invalid oauth token", "request_id", reqID, "error", err)
			http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Build principal from OAuth claims. Distinguish client-credentials tokens
		// (UserID == ClientID from a confidential client) from authorization-code
		// tokens so that arbitrary client_id collisions cannot bypass DB principal
		// lookup for auth-code tokens.
		var principal auth.Principal
		client := s.oauthSvc.GetClient(claims.ClientID)
		clientCredentials := claims.UserID == claims.ClientID && client != nil && !client.Public
		if clientCredentials {
			// Client-credentials token — synthetic principal for the client
			principal = auth.Principal{
				ID:       claims.UserID,
				Name:     claims.UserID,
				AgentID:  claims.UserID,
				Audience: s.cfg.PublicURL,
				Projects: map[string]struct{}{"default": {}},
				Scopes:   make(map[string]struct{}),
			}
		} else {
			var err error
			principal, err = s.loadPrincipalByIDOrName(r.Context(), claims.UserID)
			if err != nil {
				// Reject ambiguous names outright — do not synthesize access
				if strings.Contains(err.Error(), "multiple principals") {
					s.logger.Warn("oauth ambiguous principal name", "request_id", reqID, "user", claims.UserID, "error", err)
					w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
					http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
					return
				}
				s.logger.Warn("oauth principal not found", "request_id", reqID, "user", claims.UserID, "error", err)
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				http.Error(w, `{"error": "unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}

		if clientCredentials {
			// Confidential client credentials use their token scopes because they
			// do not map to a database principal.
			principal.Scopes = scopeSet(claims.Scopes)
		} else {
			// Authorization-code tokens cannot outlive a reconciliation downgrade.
			// The effective scopes are the intersection of the token's original
			// grant and the principal's current database grant.
			principal.Scopes = intersectScopes(principal.Scopes, claims.Scopes)
		}

		s.logger.Info("authenticated request",
			"request_id", reqID,
			"principal", principal.ID,
			"operation", r.Method,
			"path", r.URL.Path,
			"auth", "oauth",
		)

		ctx := mcp.WithAuth(r.Context(), principal, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loadPrincipalByIDOrName loads a principal by ID first, then falls back to name lookup.
// Name lookup is only used for OAuth default users; if multiple principals share the
// same name, the lookup fails to prevent authorization confusion.
func (s *Server) loadPrincipalByIDOrName(ctx context.Context, idOrName string) (auth.Principal, error) {
	// Try ID first
	p, err := s.loadPrincipal(ctx, idOrName)
	if err == nil {
		return p, nil
	}

	// Fallback: check name uniqueness before resolving
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM principals WHERE name = ? AND disabled_at IS NULL", idOrName).Scan(&count); err != nil {
		return auth.Principal{}, fmt.Errorf("count principals by name: %w", err)
	}
	if count == 0 {
		return auth.Principal{}, fmt.Errorf("no principal found with id or name %q", idOrName)
	}
	if count > 1 {
		return auth.Principal{}, fmt.Errorf("multiple principals found with name %q; use a unique principal ID", idOrName)
	}

	// Exactly one match — load by name
	var p2 auth.Principal
	var agentID sql.NullString
	row := s.db.QueryRowContext(ctx, "SELECT id, name, agent_id FROM principals WHERE name = ? AND disabled_at IS NULL", idOrName)
	if err := row.Scan(&p2.ID, &p2.Name, &agentID); err != nil {
		return p2, fmt.Errorf("load principal by name: %w", err)
	}
	if agentID.Valid {
		p2.AgentID = agentID.String
	} else {
		p2.AgentID = p2.ID
	}

	// Load projects
	projRows, err := s.db.QueryContext(ctx, "SELECT project_id FROM principal_projects WHERE principal_id = ?", p2.ID)
	if err != nil {
		return p2, fmt.Errorf("load projects: %w", err)
	}
	defer projRows.Close()

	p2.Projects = make(map[string]struct{})
	for projRows.Next() {
		var pid string
		if err := projRows.Scan(&pid); err != nil {
			return p2, fmt.Errorf("scan project: %w", err)
		}
		p2.Projects[pid] = struct{}{}
	}

	// Load scopes
	scopeRows, err := s.db.QueryContext(ctx, "SELECT scope FROM principal_scopes WHERE principal_id = ?", p2.ID)
	if err != nil {
		return p2, fmt.Errorf("load scopes: %w", err)
	}
	defer scopeRows.Close()

	p2.Scopes = make(map[string]struct{})
	for scopeRows.Next() {
		var sc string
		if err := scopeRows.Scan(&sc); err != nil {
			return p2, fmt.Errorf("scan scope: %w", err)
		}
		p2.Scopes[sc] = struct{}{}
	}

	p2.Audience = s.cfg.PublicURL
	return p2, nil
}

func (s *Server) loadPrincipal(ctx context.Context, id string) (auth.Principal, error) {
	var p auth.Principal
	var agentID sql.NullString
	row := s.db.QueryRowContext(ctx, "SELECT id, name, agent_id FROM principals WHERE id = ? AND disabled_at IS NULL", id)
	if err := row.Scan(&p.ID, &p.Name, &agentID); err != nil {
		return p, fmt.Errorf("load principal: %w", err)
	}
	if agentID.Valid {
		p.AgentID = agentID.String
	} else {
		p.AgentID = p.ID
	}

	// Load projects
	projRows, err := s.db.QueryContext(ctx, "SELECT project_id FROM principal_projects WHERE principal_id = ?", id)
	if err != nil {
		return p, fmt.Errorf("load projects: %w", err)
	}
	defer projRows.Close()

	p.Projects = make(map[string]struct{})
	for projRows.Next() {
		var pid string
		if err := projRows.Scan(&pid); err != nil {
			return p, fmt.Errorf("scan project: %w", err)
		}
		p.Projects[pid] = struct{}{}
	}

	// Load scopes
	scopeRows, err := s.db.QueryContext(ctx, "SELECT scope FROM principal_scopes WHERE principal_id = ?", id)
	if err != nil {
		return p, fmt.Errorf("load scopes: %w", err)
	}
	defer scopeRows.Close()

	p.Scopes = make(map[string]struct{})
	for scopeRows.Next() {
		var sc string
		if err := scopeRows.Scan(&sc); err != nil {
			return p, fmt.Errorf("scan scope: %w", err)
		}
		p.Scopes[sc] = struct{}{}
	}

	p.Audience = s.cfg.PublicURL
	return p, nil
}

func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"alive"}`))
}

// handleReadiness distinguishes readiness from liveness: the store must
// answer and the upstream must be reachable.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := s.db.PingContext(r.Context()); err != nil {
		s.logger.Warn("store readiness check failed", "error", sanitizeHealthError(err))
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unavailable","cause":"store"}`))
		return
	}
	if err := s.lethe.Health(r.Context()); err != nil {
		s.logger.Warn("upstream Lethe readiness check failed", "error", sanitizeHealthError(err))
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unavailable","cause":"upstream"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// handleMetrics exposes the in-process registry in Prometheus text format on
// the loopback-only admin listener.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Refresh point-in-time gauges at scrape time.
	if backlog, err := s.auditSvc.Backlog(r.Context()); err == nil {
		metrics.SetGauge(metrics.AuditOutboxBacklog, int64(backlog))
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(metrics.Expose()))
}

func scopeSet(scopes []string) map[string]struct{} {
	result := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		result[scope] = struct{}{}
	}
	return result
}

func intersectScopes(current map[string]struct{}, tokenScopes []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, scope := range tokenScopes {
		if _, ok := current[scope]; ok {
			result[scope] = struct{}{}
		}
	}
	return result
}

func sanitizeHealthError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "://") {
		return "upstream unavailable"
	}
	return err.Error()
}

func generateRequestID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate request ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
