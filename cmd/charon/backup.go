package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openlethe/charon/internal/config"
	"github.com/openlethe/charon/internal/reconcile"
)

// runBackup takes a coordinated snapshot of both databases plus the backup-set
// manifest: charon backup --charon-db <path> --lethe-db <path> --out <dir>.
func runBackup() error {
	charonDB := flagValue("--charon-db", "./data/charon.db")
	letheDB := flagValue("--lethe-db", "")
	outDir := flagValue("--out", "")
	if letheDB == "" || outDir == "" {
		return fmt.Errorf("usage: charon backup --charon-db <path> --lethe-db <path> --out <dir>")
	}
	m, err := reconcile.Backup(context.Background(), charonDB, letheDB, outDir, version)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(data))
	return nil
}

// runReconcile validates a restored database pair before mutations resume:
// charon reconcile --charon-data <dir> --lethe-db <path> [--anchor <path>] [--manifest <path>].
// Mutations must stay disabled (CHARON_RECOVERY_READONLY=1) until this passes.
func runReconcile() error {
	charonData := flagValue("--charon-data", "./data")
	letheDB := flagValue("--lethe-db", "")
	anchor := flagValue("--anchor", os.Getenv("CHARON_LEDGER_ANCHOR_PATH"))
	manifest := flagValue("--manifest", "")
	if letheDB == "" {
		return fmt.Errorf("usage: charon reconcile --charon-data <dir> --lethe-db <path> [--anchor <path>] [--manifest <path>]")
	}
	ctx := context.Background()
	charonDB := charonData + "/charon.db"

	if manifest != "" {
		if _, err := reconcile.CheckBackupManifest(manifest, charonDB, letheDB); err != nil {
			return fmt.Errorf("backup-set manifest check failed: %w", err)
		}
		fmt.Println("manifest check: OK (databases match the backup set)")
	}

	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	report, err := reconcile.Reconcile(ctx, charonDB, letheDB)
	if err != nil {
		return err
	}
	if err := reconcile.VerifyLocal(ctx, charonData, anchor, cfg.LedgerKey(), cfg.LedgerKeyID, report); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	if !report.Clean {
		return fmt.Errorf("reconciliation found errors; restore is NOT consistent — keep recovery read-only mode enabled and investigate")
	}
	fmt.Println("reconciliation: CLEAN — restore is consistent; recovery read-only mode may be lifted")
	return nil
}

// flagValue returns the value following name in os.Args, or fallback.
func flagValue(name, fallback string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return fallback
}
