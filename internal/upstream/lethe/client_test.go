package lethe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSearchEventsDecodesV04Contract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer private-key" {
			t.Fatalf("authorization header = %q", got)
		}
		if r.URL.Path != "/api/events/search" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"events":[{"event_id":"evt-1","session_id":"sess-1","project_id":"lethe","event_type":"record","content":"Charon contract","tags":"[\"gateway\"]","created_at":"2026-07-14T05:00:00Z"}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "private-key", time.Second, 1<<20)
	result, err := client.SearchEvents(context.Background(), SearchRequest{Query: "Charon", Limit: 10})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("event count = %d, want 1", len(result.Events))
	}
	event := result.Events[0]
	if event.ID != "evt-1" || event.Project != "lethe" || event.Type != "record" {
		t.Fatalf("decoded event = %#v", event)
	}
}

func TestSearchEventsRejectsMultipleTags(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "private-key", time.Second, 1<<20)
	_, err := client.SearchEvents(context.Background(), SearchRequest{Query: "memory", Tags: []string{"one", "two"}})
	if err == nil || !strings.Contains(err.Error(), "at most one tag") {
		t.Fatalf("expected explicit multi-tag error, got %v", err)
	}
}

func TestSearchEventsFansOutWildcardRecent(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventType := r.URL.Query().Get("eventType")
		if r.URL.Query().Get("projectId") != "" {
			t.Fatalf("wildcard search unexpectedly sent projectId")
		}
		mu.Lock()
		seen[eventType] = true
		mu.Unlock()
		fmt.Fprintf(w, `{"count":1,"events":[{"event_id":"%s-1","project_id":"project-%s","event_type":"%s","content":"recent","created_at":"2026-07-14T05:00:00Z"}]}`, eventType, eventType, eventType)
	}))
	defer server.Close()

	client := NewClient(server.URL, "private-key", time.Second, 1<<20)
	result, err := client.SearchEvents(context.Background(), SearchRequest{Limit: 10})
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(result.Events) != 4 {
		t.Fatalf("event count = %d, want 4", len(result.Events))
	}
	for _, eventType := range []string{"record", "log", "flag", "task"} {
		if !seen[eventType] {
			t.Errorf("missing request for %s", eventType)
		}
	}
}

func TestGetEventUsesV04TaskChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/evt-1/chain" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"chain":[{"event_id":"ancestor","project_id":"default","event_type":"task","content":"parent","created_at":"2026-07-13T05:00:00Z"},{"event_id":"evt-1","project_id":"default","event_type":"task","content":"child","created_at":"2026-07-14T05:00:00Z"}],"total":2}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "private-key", time.Second, 1<<20)
	event, err := client.GetEvent(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if event.ID != "evt-1" || event.Project != "default" {
		t.Fatalf("event = %#v", event)
	}
}

func TestThreadReadContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sessions/sess-1":
			fmt.Fprint(w, `{"session_id":"sess-1","project_id":"lethe"}`)
		case r.URL.Path == "/api/sessions/sess-1/threads":
			if got := r.URL.Query().Get("status"); got != "open" {
				t.Fatalf("status = %q", got)
			}
			fmt.Fprint(w, `{"threads":[{"thread_id":"thread-1","session_id":"sess-1","name":"auth","title":"Auth","status":"open","created_at":"2026-07-14T05:00:00Z","updated_at":"2026-07-14T05:00:00Z"}]}`)
		case r.URL.Path == "/api/threads/thread-1/events":
			fmt.Fprint(w, `{"thread":{"thread_id":"thread-1","session_id":"sess-1","name":"auth","title":"Auth","status":"open","created_at":"2026-07-14T05:00:00Z","updated_at":"2026-07-14T05:00:00Z"},"events":[{"event_id":"evt-1","project_id":"lethe","event_type":"record","content":"decision","created_at":"2026-07-14T05:00:00Z"}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/threads/thread-1"):
			fmt.Fprint(w, `{"thread":{"thread_id":"thread-1","session_id":"sess-1","name":"auth","title":"Auth","status":"open","created_at":"2026-07-14T05:00:00Z","updated_at":"2026-07-14T05:00:00Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "private-key", time.Second, 1<<20)
	session, err := client.GetSession(context.Background(), "sess-1")
	if err != nil || session.ProjectID != "lethe" {
		t.Fatalf("GetSession = %#v, %v", session, err)
	}
	threads, err := client.ListThreads(context.Background(), "sess-1", "open")
	if err != nil || len(threads) != 1 || threads[0].ID != "thread-1" {
		t.Fatalf("ListThreads = %#v, %v", threads, err)
	}
	thread, err := client.GetThread(context.Background(), "thread-1")
	if err != nil || thread.SessionID != "sess-1" {
		t.Fatalf("GetThread = %#v, %v", thread, err)
	}
	_, events, err := client.GetThreadEvents(context.Background(), "thread-1")
	if err != nil || len(events) != 1 || events[0].Project != "lethe" {
		t.Fatalf("GetThreadEvents = %#v, %v", events, err)
	}
}

func TestHealthRequiresSuccessfulUpstream(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	client := NewClient(server.URL, "private-key", time.Second, 1<<20)
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("healthy upstream: %v", err)
	}
	status = http.StatusUnauthorized
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected unhealthy upstream error")
	}
}

func TestHealthRejectsRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	client := NewClient(redirect.URL, "private-key", time.Second, 1<<20)
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("redirected health endpoint must not be considered healthy")
	}
}
