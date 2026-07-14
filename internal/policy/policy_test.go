package policy

import (
	"testing"

	"github.com/openlethe/charon/internal/auth"
)

func TestRequireScope(t *testing.T) {
	p := auth.Principal{
		ID: "test",
		Scopes: map[string]struct{}{
			"memory.search": {},
			"memory.read":   {},
		},
	}

	if err := RequireScope(p, "memory.search"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if err := RequireScope(p, "memory.write"); err == nil {
		t.Error("expected error for missing scope")
	}
}

func TestIntersectProjects(t *testing.T) {
	p := auth.Principal{
		ID: "test",
		Projects: map[string]struct{}{
			"charon": {},
			"lethe":  {},
		},
	}

	// Empty request returns all grants
	result, err := IntersectProjects(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 projects, got %d", len(result))
	}

	// Specific request
	result, err = IntersectProjects(p, []string{"charon"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0] != "charon" {
		t.Errorf("expected [charon], got %v", result)
	}

	// Unauthorized project
	_, err = IntersectProjects(p, []string{"unauthorized"})
	if err == nil {
		t.Error("expected error for unauthorized project")
	}

	// Mixed authorized and unauthorized - returns only authorized
	result, err = IntersectProjects(p, []string{"charon", "unauthorized"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0] != "charon" {
		t.Errorf("expected [charon], got %v", result)
	}
}

func TestRequireProject(t *testing.T) {
	p := auth.Principal{
		ID: "test",
		Projects: map[string]struct{}{
			"charon": {},
		},
	}

	if err := RequireProject(p, "charon"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if err := RequireProject(p, "other"); err == nil {
		t.Error("expected error for missing project")
	}
}

func TestEmptyProjects(t *testing.T) {
	p := auth.Principal{ID: "test"}

	_, err := IntersectProjects(p, nil)
	if err == nil {
		t.Error("expected error for empty project grants")
	}
}

func TestWildcardProjects(t *testing.T) {
	p := auth.Principal{ID: "test", Projects: map[string]struct{}{"*": {}}}

	projects, err := IntersectProjects(p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if projects != nil {
		t.Fatalf("wildcard without an explicit filter should omit upstream project filters, got %v", projects)
	}

	projects, err = IntersectProjects(p, []string{"default", "lethe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected both explicitly requested projects, got %v", projects)
	}

	if err := RequireReadProject(p, "future-project"); err != nil {
		t.Fatalf("wildcard should authorize future project reads: %v", err)
	}
	if err := RequireProject(p, "future-project"); err == nil {
		t.Fatal("wildcard must not authorize future project mutations")
	}
	if err := RequireProject(p, "*"); err == nil {
		t.Fatal("wildcard sentinel must never be a mutation target")
	}
}
