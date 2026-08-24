package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/session"
	"carbon/internal/store"
)

func TestProjectScopedEventsFilterForeignSharedAndUnknownRecords(t *testing.T) {
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "CAR"); err != nil {
		t.Fatal(err)
	}
	st := store.New(root)
	owned, err := st.Create(store.Draft{Title: "owned", ProjectID: "project-one", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.Create(store.Draft{Title: "foreign", ProjectID: "project-two", ProjectIDSet: true}, "human:two", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := st.Create(store.Draft{Title: "shared", ProjectIDSet: true}, "human:one", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, err := st.CreateSession(context.Background(), "agent:two", session.Session{
		ID: "ses_foreign", TaskID: foreign.Task.ID, AttemptID: "att_foreign", Actor: "agent:two",
		Status: session.StatusActive, IdempotencyKey: "foreign-event", StartedAt: time.Now(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := requestScope{Mode: "carbon", Root: root, ProjectID: "project-one"}

	if !eventVisibleToScope(scope, Event{Type: evtTaskChanged, ID: owned.Task.ID}, false) {
		t.Fatal("owned task event was filtered")
	}
	for _, event := range []Event{
		{Type: evtTaskChanged, ID: foreign.Task.ID},
		{Type: evtTaskChanged, ID: shared.Task.ID},
		{Type: evtSessionChanged, Session: foreignSession.Session.ID},
		{Type: evtTasksChanged},
		{Type: evtTaskChanged, ID: "CAR-missing"},
	} {
		if eventVisibleToScope(scope, event, false) {
			t.Fatalf("default project scope leaked %#v", event)
		}
	}
	if !eventVisibleToScope(scope, Event{Type: evtTaskChanged, ID: foreign.Task.ID}, true) {
		t.Fatal("include_cluster did not expose same-cluster foreign task event")
	}
	if _, err := st.TrashTasks(context.Background(), "human:one", []string{owned.Task.ID}, "test", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !eventVisibleToScope(scope, Event{Type: evtTaskChanged, ID: owned.Task.ID}, false) {
		t.Fatal("owned soft-deleted task event was filtered")
	}
}

func TestHubEmitsOnTaskFileWrite(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, repo.CarbonDirName, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hub := NewHub(10 * time.Millisecond)
	ch, cancel, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	if err := os.WriteFile(filepath.Join(tasksDir, "PROJ-003.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-ch:
		if e.Type != evtTaskChanged || e.ID != "PROJ-003" {
			t.Fatalf("got %+v, want task-changed PROJ-003", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after task-file write")
	}
}

func TestHubRejectsTaskDirectorySymlinkEscape(t *testing.T) {
	root := t.TempDir()
	carbon := filepath.Join(root, repo.CarbonDirName)
	if err := os.Mkdir(carbon, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(carbon, "tasks")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	hub := NewHub(10 * time.Millisecond)
	if _, _, err := hub.Subscribe(root); err == nil {
		t.Fatal("watcher accepted a tasks directory symlink that escapes the repository")
	}
}

func TestHubEmitsOnTaskFileRemove(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, repo.CarbonDirName, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tasksDir, "PROJ-005.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	hub := NewHub(10 * time.Millisecond)
	ch, cancel, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-ch:
		if e.Type != evtTaskChanged || e.ID != "PROJ-005" {
			t.Fatalf("got %+v, want task-changed PROJ-005", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after task-file remove")
	}
}

func TestHubEmitsOnSessionFileWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}

	hub := NewHub(10 * time.Millisecond)
	ch, cancel, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	path := filepath.Join(root, repo.CarbonDirName, "sessions", "ses_123.yaml")
	if err := os.WriteFile(path, []byte("id: ses_123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-ch:
		if e.Type != evtSessionChanged || e.Session != "ses_123" {
			t.Fatalf("got %+v, want session-changed ses_123", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after session-file write")
	}
}

func TestHubRefCountedTeardown(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(10 * time.Millisecond)

	_, c1, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	_, c2, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := hub.activeRoots(); n != 1 {
		t.Fatalf("two subs on one root should share one watcher, got %d", n)
	}

	c1()
	if n := hub.activeRoots(); n != 1 {
		t.Fatalf("watcher stopped while a subscriber remained, got %d", n)
	}

	c2()
	deadline := time.Now().Add(time.Second)
	for hub.activeRoots() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("watcher not torn down after last unsubscribe, roots=%d", hub.activeRoots())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The final cancel is synchronous: Windows cannot remove a TempDir while fsnotify
// still owns a handle below it. This regression test catches a return from cancel
// before the reader and debounce goroutines have both stopped.
func TestHubFinalUnsubscribeJoinsWatcherGoroutines(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(10 * time.Millisecond)
	_, cancel, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}

	hub.mu.Lock()
	rw := hub.roots[root]
	hub.mu.Unlock()
	if rw == nil {
		t.Fatal("subscription did not install a root watcher")
	}

	cancel()
	if got := hub.activeRoots(); got != 0 {
		t.Fatalf("activeRoots after final cancel = %d, want 0", got)
	}

	joined := make(chan struct{})
	go func() {
		rw.wg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("final cancel returned before watcher goroutines stopped")
	}

	// RemoveAll is specifically meaningful on Windows: it fails while an fsnotify
	// handle is live. It also proves the cancel path did not leave an async cleanup.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove watched TempDir after final cancel: %v", err)
	}
}

// A single atomic save (temp file + rename + chmod) is a burst of raw fs events for one
// task; the coalescer must collapse it to one task-changed event.
func TestCoalesceCollapsesBurstToOneTaskEvent(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/tasks/.tmp-987654" // temp file: ignored
	in <- "/x/" + repo.CarbonDirName + "/tasks/PROJ-003.md"
	in <- "/x/" + repo.CarbonDirName + "/tasks/PROJ-003.md"

	select {
	case e := <-got:
		if e.Type != evtTaskChanged || e.ID != "PROJ-003" {
			t.Fatalf("got %+v, want task-changed PROJ-003", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}

	select {
	case e := <-got:
		t.Fatalf("unexpected second event from one burst: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(in)
}

// Distinct task files changing in one window is a list-level change (membership/multiple),
// so the board refetches the whole list.
func TestCoalesceMultipleTasksIsListLevel(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/tasks/PROJ-001.md"
	in <- "/x/" + repo.CarbonDirName + "/tasks/PROJ-002.md"

	select {
	case e := <-got:
		if e.Type != evtTasksChanged || e.ID != "" {
			t.Fatalf("got %+v, want tasks-changed", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}
	close(in)
}

// One task touched alongside its own session/live writes in the same window must still
// target that task, so the open task detail refreshes live (regression: a coincident
// session write used to downgrade this to a list-only refresh, leaving the detail stale).
func TestCoalesceTaskWithSessionTargetsTask(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/tasks/PROJ-048.md"
	in <- "/x/" + repo.CarbonDirName + "/sessions/ses_123.yaml"
	in <- "/x/" + repo.CarbonDirName + "/live/ses_123.json"

	select {
	case e := <-got:
		if e.Type != evtTaskChanged || e.ID != "PROJ-048" {
			t.Fatalf("got %+v, want task-changed PROJ-048", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}
	close(in)
}

// A config change affects the board's states, so it is list-level too.
func TestCoalesceConfigIsListLevel(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/config.yaml"

	select {
	case e := <-got:
		if e.Type != evtTasksChanged {
			t.Fatalf("got %+v, want tasks-changed", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}
	close(in)
}

func TestCoalesceSessionChange(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/sessions/ses_123.yaml"
	in <- "/x/" + repo.CarbonDirName + "/live/ses_123.json"

	select {
	case e := <-got:
		if e.Type != evtSessionChanged || e.Session != "ses_123" {
			t.Fatalf("got %+v, want session-changed ses_123", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}
	close(in)
}

// Only ignored paths (temp files) must not arm the debounce timer or emit anything.
func TestCoalesceIgnoredPathsEmitNothing(t *testing.T) {
	in := make(chan string, 8)
	got := make(chan Event, 8)
	go coalesce(in, 10*time.Millisecond, func(e Event) { got <- e })

	in <- "/x/" + repo.CarbonDirName + "/tasks/.tmp-1"
	in <- "/x/" + repo.CarbonDirName + "/tasks/notes.txt" // non-task file

	select {
	case e := <-got:
		t.Fatalf("expected no event, got %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(in)
}
