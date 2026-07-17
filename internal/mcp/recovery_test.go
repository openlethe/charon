package mcp

import (
	"testing"

	"github.com/openlethe/charon/internal/config"
)

func TestRecoveryReadOnlyBlocksMutations(t *testing.T) {
	readOnly := &Server{cfg: &config.Config{RecoveryReadOnly: true}}
	if err := readOnly.requireMutationsEnabled(); err == nil {
		t.Fatal("mutations allowed in recovery read-only mode")
	}
	normal := &Server{cfg: &config.Config{}}
	if err := normal.requireMutationsEnabled(); err != nil {
		t.Fatalf("mutations blocked in normal mode: %v", err)
	}
}
