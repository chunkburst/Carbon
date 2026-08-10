package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"carbon/internal/repo"
)

func TestSSEStreamsTaskChangeEvent(t *testing.T) {
	root := t.TempDir()
	if err := repo.Init(root, "WEB"); err != nil {
		t.Fatal(err)
	}

	s := New(root, "human:test")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events?path="+root, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Headers are sent only after the handler has subscribed, so the watcher is live now.
	// Create two tasks within one debounce window: touching multiple task files coalesces
	// into a list-level tasks-changed (a single create is the more precise task-changed,
	// covered below). Creates no longer bump a config counter, so the list signal now comes
	// from the task files themselves.
	var a, b taskDTO
	call(t, s.Handler(), "POST", "/api/tasks?path="+root, `{"title":"realtime one"}`, &a)
	call(t, s.Handler(), "POST", "/api/tasks?path="+root, `{"title":"realtime two"}`, &b)

	line := readSSEData(t, resp.Body)
	if !strings.Contains(line, "tasks-changed") {
		t.Fatalf("event line = %q, want a tasks-changed signal", line)
	}
}

// A task-file-only write (a note bumps no config counter) must stream as task-changed with
// the task's id — proving the SSE layer carries single-task signals, not just list churn.
func TestSSEStreamsTaskChangedForSingleTask(t *testing.T) {
	root := t.TempDir()
	if err := repo.Init(root, "WEB"); err != nil {
		t.Fatal(err)
	}
	s := New(root, "human:test")
	var created taskDTO
	call(t, s.Handler(), "POST", "/api/tasks?path="+root, `{"title":"note me"}`, &created)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events?path="+root, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = resp.Header.Get("Content-Type") // stream open => watcher subscribed

	// A note writes only the task file (no config counter bump).
	var noted taskDTO
	call(t, s.Handler(), "POST", "/api/tasks/"+created.ID+"/note?path="+root, `{"text":"hi"}`, &noted)

	line := readSSEData(t, resp.Body)
	if !strings.Contains(line, `"task-changed"`) || !strings.Contains(line, created.ID) {
		t.Fatalf("event = %q, want task-changed for %s", line, created.ID)
	}
}

// On open, the stream must emit an immediate keepalive comment so it doesn't look dead
// while idle (before any change or the periodic heartbeat).
func TestSSESendsImmediateCommentOnOpen(t *testing.T) {
	root := t.TempDir()
	if err := repo.Init(root, "WEB"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(root, "human:test").Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events?path="+root, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("reading first line: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Fatalf("first line = %q, want an immediate comment (\":\"-prefixed)", line)
	}
}

// readSSEData reads stream lines until the first `data:` line, failing on timeout/EOF.
func readSSEData(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	br := bufio.NewReader(r)
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE stream: %v (last: %q)", err, l)
		}
		if data, ok := strings.CutPrefix(l, "data:"); ok {
			return strings.TrimSpace(data)
		}
	}
}
