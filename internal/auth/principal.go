package auth

// Principal represents an authenticated caller.
type Principal struct {
	ID       string
	Name     string
	AgentID  string            // Maps to Lethe's agent_id for event attribution
	Projects map[string]struct{}
	Scopes   map[string]struct{}
	Audience string
}

// HasScope checks if the principal has a given scope.
func (p Principal) HasScope(scope string) bool {
	_, ok := p.Scopes[scope]
	return ok
}

// HasProject checks if the principal has a given project grant.
func (p Principal) HasProject(projectID string) bool {
	_, ok := p.Projects[projectID]
	return ok
}

// IsEmpty returns true if the principal has no project grants.
func (p Principal) IsEmpty() bool {
	return len(p.Projects) == 0
}
