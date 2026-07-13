package lethe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a strict, read-only adapter to the Lethe API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	maxBody    int64
}

// NewClient creates a Lethe client.
func NewClient(baseURL, apiKey string, timeout time.Duration, maxBody int64) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		maxBody: maxBody,
	}
}

// Event represents a Lethe memory event.
type Event struct {
	ID        string                 `json:"id"`
	Project   string                 `json:"project"`
	Type      string                 `json:"type"`
	Kind      string                 `json:"kind,omitempty"`
	Content   string                 `json:"content"`
	Tags      interface{}            `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
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
	Project string   `json:"project"`
	Type    string   `json:"type"`
	Kind    string   `json:"kind,omitempty"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
	AgentID string   `json:"agent_id,omitempty"` // Attribution for external agents
}

// SearchEvents queries Lethe for events.
func (c *Client) SearchEvents(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	// Lethe v0.4 uses GET /api/events/search with query params
	params := url.Values{}
	params.Set("q", req.Query)
	if req.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", req.Limit))
	}
	for _, et := range req.EventTypes {
		params.Add("eventType", et)
	}
	for _, p := range req.Projects {
		params.Add("projectId", p)
	}
	for _, t := range req.Tags {
		params.Add("tag", t)
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
	path := fmt.Sprintf("/api/events/%s", id)
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

	var event Event
	if err := c.decodeJSON(resp, &event); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	return &event, nil
}

// RecentEvents retrieves recent events.
func (c *Client) RecentEvents(ctx context.Context, req RecentRequest) (*RecentResult, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/events/recent", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lethe returned %d", resp.StatusCode)
	}

	var result RecentResult
	if err := c.decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode recent: %w", err)
	}
	return &result, nil
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

func (c *Client) postSearch(ctx context.Context, path string, reqBody interface{}) (*SearchResult, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
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
