package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/obol"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/server"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: charon <serve|version|obol|principal|proposal|ledger>")
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "version":
		fmt.Println(versionInfo())
	case "serve":
		if err := runServe(); err != nil {
			log.Fatal(err)
		}
	case "obol":
		if err := runObol(); err != nil {
			log.Fatal(err)
		}
	case "principal":
		if err := runPrincipal(); err != nil {
			log.Fatal(err)
		}
	case "proposal":
		if err := runProposal(); err != nil {
			log.Fatal(err)
		}
	case "ledger":
		if err := runLedger(); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

func versionInfo() string {
	return fmt.Sprintf("charon %s (commit %s, built %s, go %s)", version, commit, buildDate, runtime.Version())
}

func runServe() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	writeOAuthAuthorizationKeyBanner(os.Stderr, cfg)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer db.Close()

	logger.Info("starting charon", "mode", cfg.Mode, "upstream", cfg.Upstream)

	logOAuthConfiguration(logger, cfg)

	srv, err := server.NewServer(cfg, db, logger)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Start(ctx)
}

func logOAuthConfiguration(logger *slog.Logger, cfg *config.Config) {
	if cfg.AuthMode != "oauth" {
		return
	}

	logger.Info("oauth configuration",
		"client_id", cfg.OAuthClientID,
		"metadata_url", cfg.PublicURL+"/.well-known/oauth-protected-resource",
		"authorize_url", cfg.PublicURL+"/oauth/authorize",
		"token_url", cfg.PublicURL+"/oauth/token",
	)
	if cfg.OAuthPairingSecretGenerated {
		logger.Warn("oauth authorization key generated; treat container logs as sensitive")
		return
	}
	logger.Info("oauth authorization key loaded from configuration; value not logged")
}

func writeOAuthAuthorizationKeyBanner(w io.Writer, cfg *config.Config) {
	if cfg.AuthMode != "oauth" || !cfg.OAuthPairingSecretGenerated {
		return
	}

	const border = "+------------------------------------------------------------------+"
	fmt.Fprintln(w, border)
	fmt.Fprintln(w, "|  CHARON OAUTH AUTHORIZATION KEY                                  |")
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "|  %-64s|\n", cfg.OAuthPairingSecret)
	fmt.Fprintln(w, border)
	fmt.Fprintln(w, "|  Paste this key into the browser authorization page.             |")
	fmt.Fprintln(w, "|  A new key is generated whenever this container restarts.        |")
	fmt.Fprintln(w, border)
}

func runObol() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: charon obol <mint|ls|revoke>")
		os.Exit(1)
	}

	sub := os.Args[2]
	ctx := context.Background()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	obolSvc := obol.NewService(db, cfg.ObolKey())

	switch sub {
	case "mint":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon obol mint [flags] <principal-id>")
			fmt.Fprintln(os.Stderr, "  --expires <duration>  Token expiry duration (e.g., 7d, 1h, 30m). Default: 30d")
			os.Exit(1)
		}

		// Parse flags - flags can be anywhere, principal is first non-flag
		expires := 30 * 24 * time.Hour // default 30 days
		var principalID string

		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--expires" {
				if i+1 >= len(os.Args) {
					fmt.Fprintln(os.Stderr, "Missing value for --expires")
					os.Exit(1)
				}
				i++
				d, err := parseDuration(os.Args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Invalid duration: %v\n", err)
					os.Exit(1)
				}
				if d <= 0 {
					fmt.Fprintln(os.Stderr, "Expiry must be positive")
					os.Exit(1)
				}
				expires = d
			} else if !strings.HasPrefix(os.Args[i], "-") && principalID == "" {
				principalID = os.Args[i]
			} else if strings.HasPrefix(os.Args[i], "-") {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", os.Args[i])
				os.Exit(1)
			}
		}

		if principalID == "" {
			fmt.Fprintln(os.Stderr, "Missing principal ID")
			os.Exit(1)
		}

		o, err := obolSvc.Mint(ctx, principalID, cfg.PublicURL, expires)
		if err != nil {
			return err
		}
		fmt.Printf("Obol minted:\n")
		fmt.Printf("  ID:    %s\n", o.ID)
		fmt.Printf("  Token: %s\n", o.FullToken)
		fmt.Printf("  Exp:   %s\n", o.ExpiresAt.Format(time.RFC3339))
	case "ls":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon obol ls <principal-id>")
			os.Exit(1)
		}
		principalID := os.Args[3]
		obols, err := obolSvc.List(ctx, principalID)
		if err != nil {
			return err
		}
		for _, o := range obols {
			status := "active"
			if o.RevokedAt != nil {
				status = "revoked"
			} else if o.ExpiresAt != nil && time.Now().UTC().After(*o.ExpiresAt) {
				status = "expired"
			}
			fmt.Printf("%s  %s  %s\n", o.ID, o.CreatedAt.Format("2006-01-02"), status)
		}
	case "revoke":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon obol revoke <obol-id>")
			os.Exit(1)
		}
		id := os.Args[3]
		if err := obolSvc.Revoke(ctx, id); err != nil {
			return err
		}
		fmt.Println("Obol revoked")
	default:
		fmt.Fprintf(os.Stderr, "Unknown obol subcommand: %s\n", sub)
		os.Exit(1)
	}

	return nil
}

func runPrincipal() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: charon principal <create|grant-project|grant-scope|reconcile|disable|enable>")
		os.Exit(1)
	}

	sub := os.Args[2]
	ctx := context.Background()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	switch sub {
	case "reconcile":
		if len(os.Args) < 6 {
			fmt.Fprintln(os.Stderr, "Usage: charon principal reconcile <name> <readonly|propose|review|write> <project[,project...]> ")
			os.Exit(1)
		}
		name := os.Args[3]
		permission := os.Args[4]
		projects := splitNonEmpty(os.Args[5])
		if len(projects) == 0 {
			return fmt.Errorf("at least one project grant is required")
		}
		scopes, err := scopesForPermission(permission)
		if err != nil {
			return err
		}
		principalID, created, err := db.ReconcilePrincipal(ctx, name, projects, scopes)
		if err != nil {
			return err
		}
		action := "updated"
		if created {
			action = "created"
		}
		fmt.Printf("Reconciled principal: %s (%s)\n", principalID, action)

	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon principal create <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		randomID, err := generateID()
		if err != nil {
			return err
		}
		id := fmt.Sprintf("principal_%s", randomID)
		_, err = db.ExecContext(ctx,
			"INSERT INTO principals (id, name, agent_id, created_at) VALUES (?, ?, ?, ?)",
			id, name, id, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return err
		}
		fmt.Printf("Created principal: %s\n", id)

	case "grant-project":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Usage: charon principal grant-project <principal-id> <project-id>")
			os.Exit(1)
		}
		principalID := os.Args[3]
		projectID := os.Args[4]
		_, err := db.ExecContext(ctx,
			"INSERT INTO principal_projects (principal_id, project_id) VALUES (?, ?)",
			principalID, projectID,
		)
		if err != nil {
			return err
		}
		fmt.Printf("Granted project %s to %s\n", projectID, principalID)

	case "grant-scope":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Usage: charon principal grant-scope <principal-id> <scope>")
			os.Exit(1)
		}
		principalID := os.Args[3]
		scope := os.Args[4]
		_, err := db.ExecContext(ctx,
			"INSERT INTO principal_scopes (principal_id, scope) VALUES (?, ?)",
			principalID, scope,
		)
		if err != nil {
			return err
		}
		fmt.Printf("Granted scope %s to %s\n", scope, principalID)

	case "disable", "enable":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: charon principal %s <principal-id>\n", sub)
			os.Exit(1)
		}
		principalID := os.Args[3]
		var disabledAt any
		if sub == "disable" {
			disabledAt = time.Now().UTC().Format(time.RFC3339)
		}
		result, err := db.ExecContext(ctx, "UPDATE principals SET disabled_at = ? WHERE id = ?", disabledAt, principalID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return fmt.Errorf("principal not found: %s", principalID)
		}
		fmt.Printf("Principal %s: %s\n", sub, principalID)

	default:
		fmt.Fprintf(os.Stderr, "Unknown principal subcommand: %s\n", sub)
		os.Exit(1)
	}

	return nil
}

func splitNonEmpty(value string) []string {
	var result []string
	seen := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func scopesForPermission(permission string) ([]string, error) {
	readonly := []string{"memory.search", "memory.read", "thread.read"}
	author := append(append([]string{}, readonly...),
		"memory.branch", "memory.commit", "memory.propose")
	switch permission {
	case "readonly":
		return readonly, nil
	case "propose":
		return author, nil
	case "review":
		return append(append([]string{}, readonly...),
			"memory.review", "memory.merge", "proposal.review"), nil
	case "write":
		return append(author,
			"memory.review", "memory.merge", "memory.write", "proposal.review"), nil
	default:
		return nil, fmt.Errorf("unknown permission %q: expected readonly, propose, review, or write", permission)
	}
}

func runLedger() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: charon ledger <tail|verify>")
		os.Exit(1)
	}

	sub := os.Args[2]
	ctx := context.Background()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	ledgerSvc := ledger.NewService(db)

	switch sub {
	case "tail":
		limit, err := optionalLimit(os.Args, 3, 20)
		if err != nil {
			return err
		}
		entries, err := ledgerSvc.Tail(ctx, limit)
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Printf("[%s] seq=%d op=%s principal=%s result=%s latency=%dms\n",
				e.CreatedAt.Format("2006-01-02 15:04:05"),
				e.Sequence, e.Operation, e.PrincipalID, e.Result, e.Latency.Milliseconds(),
			)
		}
	case "verify":
		count, err := ledgerSvc.Verify(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Ledger verified: %d entries\n", count)
	default:
		fmt.Fprintf(os.Stderr, "Unknown ledger subcommand: %s\n", sub)
		os.Exit(1)
	}

	return nil
}

func runProposal() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: charon proposal <ls|approve|reject>")
		os.Exit(1)
	}

	sub := os.Args[2]
	ctx := context.Background()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	letheClient := lethe.NewClient(cfg.Upstream, cfg.LetheAPIKey, cfg.RequestTimeout, int64(cfg.MaxResultBytes))
	proposalSvc := proposal.NewService(db, letheClient)
	if _, _, err := proposalSvc.MigrateStateDigests(ctx); err != nil {
		return fmt.Errorf("proposal state digest migration: %w", err)
	}

	switch sub {
	case "ls":
		limit, err := optionalLimit(os.Args, 3, 20)
		if err != nil {
			return err
		}
		proposals, err := proposalSvc.List(ctx, proposal.StatusPending, limit)
		if err != nil {
			return err
		}
		for _, p := range proposals {
			fmt.Printf("%s  %s  %s  %.2f  %s\n", p.ID, p.ProjectID, p.Kind, p.Confidence, p.CreatedAt.Format("2006-01-02 15:04"))
		}
	case "approve":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon proposal approve <proposal-id>")
			os.Exit(1)
		}
		id := os.Args[3]
		p, err := proposalSvc.Approve(ctx, proposal.ApproveRequest{ProposalID: id, DecidedBy: "admin-cli"})
		if err != nil {
			return err
		}
		fmt.Printf("Approved proposal %s (event: %s)\n", p.ID, p.ResultingLetheEventID)
	case "reject":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon proposal reject <proposal-id>")
			os.Exit(1)
		}
		id := os.Args[3]
		p, err := proposalSvc.Reject(ctx, proposal.RejectRequest{ProposalID: id, DecidedBy: "admin-cli"})
		if err != nil {
			return err
		}
		fmt.Printf("Rejected proposal %s\n", p.ID)
	default:
		fmt.Fprintf(os.Stderr, "Unknown proposal subcommand: %s\n", sub)
		os.Exit(1)
	}

	return nil
}

func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// parseDuration parses a duration string like "7d", "1h", "30m"
func optionalLimit(args []string, index, fallback int) (int, error) {
	if len(args) <= index {
		return fallback, nil
	}
	limit, err := strconv.Atoi(args[index])
	if err != nil || limit <= 0 || limit > 100 {
		return 0, fmt.Errorf("invalid limit %q: expected an integer from 1 to 100", args[index])
	}
	return limit, nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0, fmt.Errorf("empty duration")
	}

	// Check for suffix
	var unit time.Duration
	var numStr string

	switch {
	case strings.HasSuffix(s, "d"):
		unit = 24 * time.Hour
		numStr = s[:len(s)-1]
	case strings.HasSuffix(s, "h"):
		unit = time.Hour
		numStr = s[:len(s)-1]
	case strings.HasSuffix(s, "m"):
		unit = time.Minute
		numStr = s[:len(s)-1]
	default:
		// Try standard Go duration parsing
		return time.ParseDuration(s)
	}

	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %w", err)
	}

	return time.Duration(num) * unit, nil
}
