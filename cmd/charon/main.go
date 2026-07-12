package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/ledger"
	"github.com/openlethe/charon/internal/obol"
	"github.com/openlethe/charon/internal/proposal"
	"github.com/openlethe/charon/internal/server"
	"github.com/openlethe/charon/internal/store"
	"github.com/openlethe/charon/internal/upstream/lethe"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: charon <serve|obol|principal|proposal|ledger>")
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
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

func runServe() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer db.Close()

	logger.Info("starting charon", "mode", cfg.Mode, "upstream", cfg.Upstream)

	srv, err := server.NewServer(cfg, db, logger)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	return srv.Start()
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

	obolSvc := obol.NewService(db, cfg.HMACKey)

	switch sub {
	case "mint":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon obol mint <principal-id>")
			os.Exit(1)
		}
		principalID := os.Args[3]
		o, err := obolSvc.Mint(ctx, principalID, cfg.PublicURL, 30*24*time.Hour)
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
		fmt.Fprintln(os.Stderr, "Usage: charon principal <create|grant-project|grant-scope>")
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
	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: charon principal create <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		id := fmt.Sprintf("principal_%s", generateID())
		_, err := db.ExecContext(ctx,
			"INSERT INTO principals (id, name, created_at) VALUES (?, ?, ?)",
			id, name, time.Now().UTC().Format(time.RFC3339),
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

	default:
		fmt.Fprintf(os.Stderr, "Unknown principal subcommand: %s\n", sub)
		os.Exit(1)
	}

	return nil
}

func runLedger() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: charon ledger <tail>")
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
		limit := 20
		if len(os.Args) > 3 {
			fmt.Sscanf(os.Args[3], "%d", &limit)
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

	switch sub {
	case "ls":
		limit := 20
		if len(os.Args) > 3 {
			fmt.Sscanf(os.Args[3], "%d", &limit)
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

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
