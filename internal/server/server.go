package server

import (
	"context"
	"crypto/rand"
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

	return &Server{
		cfg:       cfg,
		db:        db,
		obolSvc:   obolSvc,
		ledgerSvc: ledgerSvc,
		lethe:     letheClient,
		mcpServer: mcpServer,
		logger:    logger,
	}, nil
}

// Start begins serving HTTP.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// MCP endpoint with auth
	mux.Handle("/mcp", s.withAuth(s.mcpServer))

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

	s.logger.Info("charon listening", "addr", s.cfg.HTTP)
	return http.ListenAndServe(s.cfg.HTTP, mux)
}

func (s *Server) withAuth(next http.Handler) http.Handler {
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

func (s *Server) loadPrincipal(ctx context.Context, id string) (auth.Principal, error) {
	var p auth.Principal
	row := s.db.QueryRowContext(ctx, "SELECT id, name, agent_id FROM principals WHERE id = ?", id)
	if err := row.Scan(&p.ID, &p.Name, &p.AgentID); err != nil {
		return p, fmt.Errorf("load principal: %w", err)
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
