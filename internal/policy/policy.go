package policy

import (
	"errors"
	"fmt"

	"github.com/openlethe/charon/internal/auth"
)

// Errors returned by policy checks.
var (
	ErrMissingScope   = errors.New("missing required scope")
	ErrMissingProject = errors.New("missing project grant")
	ErrNoProjects     = errors.New("principal has no project grants")
)

// RequireScope checks that the principal has the given scope.
func RequireScope(p auth.Principal, scope string) error {
	if !p.HasScope(scope) {
		return fmt.Errorf("%w: %s", ErrMissingScope, scope)
	}
	return nil
}

// IntersectProjects returns the intersection of requested projects and principal grants.
// If requested is empty, returns all granted projects.
func IntersectProjects(p auth.Principal, requested []string) ([]string, error) {
	if p.IsEmpty() {
		return nil, ErrNoProjects
	}

	if len(requested) == 0 {
		if p.HasAllProjects() {
			// No upstream project filter means all projects. Charon still applies
			// its authorization filter to every returned object.
			return nil, nil
		}
		// Return all granted projects
		result := make([]string, 0, len(p.Projects))
		for pid := range p.Projects {
			result = append(result, pid)
		}
		return result, nil
	}

	result := make([]string, 0, len(requested))
	for _, pid := range requested {
		if p.HasReadProject(pid) {
			result = append(result, pid)
		}
	}

	if len(result) == 0 {
		return nil, ErrMissingProject
	}

	return result, nil
}

// RequireReadProject accepts an exact project grant or the read-only wildcard.
func RequireReadProject(p auth.Principal, projectID string) error {
	if !p.HasReadProject(projectID) {
		return fmt.Errorf("%w: %s", ErrMissingProject, projectID)
	}
	return nil
}

// RequireProject checks that the principal has access to a specific project.
// This exact check is used by mutation paths; the read wildcard is excluded.
func RequireProject(p auth.Principal, projectID string) error {
	if projectID == "*" {
		return fmt.Errorf("%w: wildcard is read-only", ErrMissingProject)
	}
	if !p.HasProject(projectID) {
		return fmt.Errorf("%w: %s", ErrMissingProject, projectID)
	}
	return nil
}
