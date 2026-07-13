package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/openlethe/charon/internal/auth"
	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/idempotency"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/mcp"
	"github.com/openlethe/charon/internal/oauth"
	"github.com/openlethe/charon/internal/obol"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

// Server is the Charon HTTP server.
type Server struct {
	cfg       *config.Config
	db        *store.DB
	obolSvc   *obol.Service
	oauthSvc  *oauth.Service
	ledgerSvc *ledger.Service
	lethe     *lethe.Client
	mcpServer *mcp.Server
	logger    *slog.Logger
}

// NewServer creates a Charon server.
func NewServer(cfg *config.Config, db *store.DB, logger *slog.Logger) (*Server, error) {
	obolSvc := obol.NewService(db, cfg.HMACKey)
	ledgerSvc := ledger.NewService(db)
	letheClient := lethe.NewClient(cfg.Upstream, cfg.LetheAPIKey, cfg.RequestTimeout, int64(cfg.MaxResultBytes))
	proposalSvc := proposal.NewService(db, letheClient)
	idempotencySvc := idempotency.NewService(db)
	mcpServer := mcp.NewServer(cfg, letheClient, ledgerSvc, proposalSvc, idempotencySvc)

	// Initialize OAuth service with HMAC key and client ID
	var oauthSvc *oauth.Service
	if cfg.HMACKey != "" {
		var err error
		oauthSvc, err = oauth.NewService([]byte(cfg.HMACKey), cfg.PublicURL, cfg.OAuthClientID)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize OAuth service: %w", err)
		}
	}

	return &Server{
		cfg:       cfg,
		db:        db,
		obolSvc:   obolSvc,
		oauthSvc:  oauthSvc,
		ledgerSvc: ledgerSvc,
		lethe:     letheClient,
		mcpServer: mcpServer,
		logger:    logger,
	}, nil
}

// Start begins serving HTTP.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// MCP endpoint — auth controlled by config
	switch s.cfg.AuthMode {
	case "none":
		s.logger.Warn("MCP endpoint running WITHOUT authentication — testing only")
		mux.Handle("/mcp", s.mcpServer)
	case "oauth":
		if s.oauthSvc == nil {
			return fmt.Errorf("oauth service not initialized (HMAC key required)")
		}
		mux.Handle("/mcp", s.withOAuthAuth(s.mcpServer))
		// Register OAuth routes
		oauthHandlers := oauth.NewHandlers(s.oauthSvc)
		oauthHandlers.RegisterRoutes(mux)
		s.logger.Info("OAuth 2.0 endpoints registered")
	default:
		mux.Handle("/mcp", s.withObolAuth(s.mcpServer))
	}

	// Health check
	mux.HandleFunc("/health", s.handleHealth)

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin/health", s.handleHealth)

	go func() {
		addr := s.cfg.AdminHTTP
		if strings.HasPrefix(addr, "/") {
			// Unix socket
			listener, err := net.Listen("unix", addr)
			if err != nil {
				s.logger.Error("admin listener failed", "error", err)
				return
			}
			defer listener.Close()
			s.logger.Info("admin listening", "socket", addr)
			if err := http.Serve(listener, adminMux); err != nil {
				s.logger.Error("admin server error", "error", err)
			}
		} else {
			s.logger.Info("admin listening", "addr", addr)
			if err := http.ListenAndServe(addr, adminMux); err != nil {
				s.logger.Error("admin server error", "error", err)
			}
		}
	}()

	s.logger.Info("charon listening", "addr", s.cfg.HTTP, "auth_mode", s.cfg.AuthMode)
	return http.ListenAndServe(s.cfg.HTTP, mux)
}

// withObolAuth validates Obol tokens (legacy, default).
func (s *Server) withObolAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := generateRequestID()
		r = r.WithContext(context.WithValue(r.Context(), "request_id", reqID))

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
		reqID := generateRequestID()
		r = r.WithContext(context.WithValue(r.Context(), "request_id", reqID))

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

		// Build principal from OAuth claims
		principal := auth.Principal{
			ID:       claims.UserID,
			Name:     claims.UserID,
			AgentID:  claims.UserID,
			Audience: s.cfg.PublicURL,
			Projects: map[string]struct{}{"default": {}},
			Scopes:   make(map[string]struct{}),
		}

		for _, scope := range claims.Scopes {
			principal.Scopes[scope] = struct{}{}
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

func (s *Server) loadPrincipal(ctx context.Context, id string) (auth.Principal, error) {
	var p auth.Principal
	var agentID sql.NullString
	row := s.db.QueryRowContext(ctx, "SELECT id, name, agent_id FROM principals WHERE id = ?", id)
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok"}`))
}

func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
