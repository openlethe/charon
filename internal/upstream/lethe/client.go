package lethe

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Client is a strict, read-only adapter to the Lethe API.
type Client struct {
	baseURL         string
	apiKey          string
	mergeSigningKey []byte
	httpClient      *http.Client
	maxBody         int64
}

// SetMergeSigningKey configures Charon's separate protected-ref merge key.
// It is intentionally distinct from the Lethe bearer API token.
func (c *Client) SetMergeSigningKey(key string) {
	c.mergeSigningKey = []byte(key)
}

func (c *Client) signMergeAuthorization(project, refName, expectedHead, newHead, proposalID, reviewer string) (string, error) {
	if len(c.mergeSigningKey) < 32 {
		return "", fmt.Errorf("merge signing key is not configured")
	}
	message := fmt.Sprintf("memory-git-merge/v1\n%s\n%s\n%s\n%s\n%s\n%s", project, refName, expectedHead, newHead, proposalID, reviewer)
	mac := hmac.New(sha256.New, c.mergeSigningKey)
	_, _ = mac.Write([]byte(message))
	return fmt.Sprintf("%x", mac.Sum(nil)), nil
}

// NewClient creates a Lethe client.
func NewClient(baseURL, apiKey string, timeout time.Duration, maxBody int64) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxBody: maxBody,
	}
}

// Event represents a Lethe memory event.
type Event struct {
	ID        string                 `json:"event_id"`
	SessionID *string                `json:"session_id,omitempty"`
	Project   string                 `json:"project_id"`
	Type      string                 `json:"event_type"`
	Kind      string                 `json:"kind,omitempty"`
	Content   string                 `json:"content"`
	Tags      interface{}            `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	ThreadID  string                 `json:"thread_id,omitempty"`
	CreatedAt string                 `json:"created_at"`
}

// SearchRequest is a Lethe search query.
type SearchRequest struct {
	Query      string   `json:"query,omitempty"`
	Projects   []string `json:"projects,omitempty"`
	EventTypes []string `json:"event_types,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Offset     int      `json:"offset,omitempty"`
}

// SearchResult is the response from a Lethe search.
type SearchResult struct {
	Events []Event `json:"events"`
	Total  int     `json:"total"`
}

// RecentRequest queries recent events.
type RecentRequest struct {
	Projects []string `json:"projects,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// RecentResult contains recent events.
type RecentResult struct {
	Events []Event `json:"events"`
}

// StatsResult contains Lethe statistics.
type StatsResult struct {
	TotalEvents int64 `json:"total_events"`
}

// CreateEvent writes a new event to Lethe.
func (c *Client) CreateEvent(ctx context.Context, req CreateEventRequest) (*Event, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/events", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}

	var event Event
	if err := c.decodeJSON(resp, &event); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	return &event, nil
}

// CreateEventRequest is used to create a new Lethe event.
type CreateEventRequest struct {
	Project string   `json:"project_id"`
	Type    string   `json:"event_type"`
	Kind    string   `json:"kind,omitempty"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
	AgentID string   `json:"agent_id,omitempty"` // Attribution for external agents
}

// SearchEvents queries Lethe for events.
func (c *Client) SearchEvents(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	if len(req.Tags) > 1 {
		return nil, fmt.Errorf("Lethe v0.4 supports at most one tag filter")
	}
	projects := req.Projects
	if len(projects) == 0 {
		projects = []string{""}
	}
	eventTypes := req.EventTypes
	if len(eventTypes) == 0 {
		eventTypes = []string{""}
	}

	// Lethe v0.4 accepts one projectId and one eventType per request. Fan out
	// explicit allowlists, then merge deterministically. A bare empty-query
	// search is rejected upstream, so wildcard recent reads fan out over the
	// four public event types.
	if req.Query == "" && len(req.Projects) == 0 && len(req.EventTypes) == 0 {
		eventTypes = []string{"record", "log", "flag", "task"}
	}

	merged := make(map[string]Event)
	for _, project := range projects {
		for _, eventType := range eventTypes {
			result, err := c.searchEventsOnce(ctx, req, project, eventType)
			if err != nil {
				return nil, err
			}
			for _, event := range result.Events {
				merged[event.ID] = event
			}
		}
	}

	events := make([]Event, 0, len(merged))
	for _, event := range merged {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt == events[j].CreatedAt {
			return events[i].ID > events[j].ID
		}
		return events[i].CreatedAt > events[j].CreatedAt
	})
	if req.Limit > 0 && len(events) > req.Limit {
		events = events[:req.Limit]
	}

	return &SearchResult{Events: events, Total: len(events)}, nil
}

func (c *Client) searchEventsOnce(ctx context.Context, req SearchRequest, project, eventType string) (*SearchResult, error) {
	params := url.Values{}
	params.Set("q", req.Query)
	if req.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", req.Limit))
	}
	if eventType != "" {
		params.Set("eventType", eventType)
	}
	if project != "" {
		params.Set("projectId", project)
	}
	if len(req.Tags) > 0 {
		params.Set("tag", req.Tags[0])
	}
	url := "/api/events/search?" + params.Encode()

	resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}

	var result SearchResult
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return &result, nil
}

// GetEvent retrieves a single event by ID.
func (c *Client) GetEvent(ctx context.Context, id string) (*Event, error) {
	path := fmt.Sprintf("/api/events/%s/chain", url.PathEscape(id))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("event not found: %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}

	var result struct {
		Chain []Event `json:"chain"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	if len(result.Chain) == 0 {
		return nil, fmt.Errorf("event not found: %s", id)
	}
	for i := range result.Chain {
		if result.Chain[i].ID == id {
			return &result.Chain[i], nil
		}
	}
	return nil, fmt.Errorf("event not found in returned chain: %s", id)
}

// RecentEvents retrieves recent events by using search with empty query.
func (c *Client) RecentEvents(ctx context.Context, req RecentRequest) (*RecentResult, error) {
	// Lethe v0.4 doesn't have /api/events/recent; use search with empty query
	searchReq := SearchRequest{
		Projects: req.Projects,
		Limit:    req.Limit,
	}
	result, err := c.SearchEvents(ctx, searchReq)
	if err != nil {
		return nil, err
	}
	return &RecentResult{Events: result.Events}, nil
}

// GetStats retrieves Lethe statistics.
func (c *Client) GetStats(ctx context.Context) (*StatsResult, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/stats", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}

	var stats StatsResult
	if err := c.decodeJSON(resp, &stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}

// Health verifies that the configured canonical Lethe API is reachable and
// accepts Charon's private upstream credential.
func (c *Client) Health(ctx context.Context) error {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	return nil
}

// Thread represents a Lethe v0.4 thread.
type Thread struct {
	ID         string  `json:"thread_id"`
	SessionID  string  `json:"session_id"`
	Name       string  `json:"name"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	ResolvedAt *string `json:"resolved_at,omitempty"`
}

// Session contains the fields Charon needs to authorize thread reads.
type Session struct {
	ID        string `json:"session_id"`
	ProjectID string `json:"project_id"`
}

func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var session Session
	if err := c.decodeJSON(resp, &session); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &session, nil
}

func (c *Client) ListThreads(ctx context.Context, sessionID, status string) ([]Thread, error) {
	params := url.Values{}
	if status != "" {
		params.Set("status", status)
	}
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/threads"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result struct {
		Threads []Thread `json:"threads"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode threads: %w", err)
	}
	return result.Threads, nil
}

func (c *Client) GetThread(ctx context.Context, id string) (*Thread, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/threads/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("thread not found: %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result struct {
		Thread Thread `json:"thread"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode thread: %w", err)
	}
	return &result.Thread, nil
}

func (c *Client) GetThreadEvents(ctx context.Context, id string) (*Thread, []Event, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/threads/"+url.PathEscape(id)+"/events", nil)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("thread not found: %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}
	var result struct {
		Thread Thread  `json:"thread"`
		Events []Event `json:"events"`
	}
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, nil, fmt.Errorf("decode thread events: %w", err)
	}
	return &result.Thread, result.Events, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

func (c *Client) decodeJSON(resp *http.Response, v interface{}) error {
	reader := io.LimitReader(resp.Body, c.maxBody)
	return json.NewDecoder(reader).Decode(v)
}
