// Real-time board sync. The web server watches the file-based store so changes made by
// ANY actor — including MCP agents in a separate process — push to connected UIs over SSE.
// This file holds the transport-agnostic core: the Event type and the debouncing
// coalescer. The fsnotify wiring and per-root subscriber Hub live alongside it.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"carbon/internal/mcp"
	"carbon/internal/repo"
	"carbon/internal/store"
)

// sseHeartbeat is how often a comment line is sent to keep idle connections (and proxies)
// from timing out. EventSource clients ignore comment lines.
const sseHeartbeat = 15 * time.Second

// handleEvents streams store changes to one client as Server-Sent Events. It subscribes to
// the resolved root's watcher before writing headers (so no change is missed between the
// handshake and the first read) and tears the subscription down on disconnect. Uses ?path=
// like every other endpoint (the design doc's ?root= predates that convention).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	scope, err := s.resolveScope(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if !scope.hasStore() {
		writeJSON(w, http.StatusBadRequest, errBody(fmt.Errorf("events require legacy path/repo, Carbon cluster scope, or Carbon standalone project scope")))
		return
	}
	root := scope.Root
	includeAll := includeCluster(r)
	if scope.Standalone && includeAll {
		writeJSON(w, http.StatusUnprocessableEntity, errBody(mcp.ErrStandaloneClusterScope))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, cancel, err := s.hub.Subscribe(root)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Immediately prove the stream is live so an idle connection doesn't look dead while
	// waiting for the first change or heartbeat. EventSource ignores comment lines.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return // client disconnected / server shutting down
		case e, ok := <-ch:
			if !ok {
				return
			}
			if !eventVisibleToScope(scope, e, includeAll) {
				continue
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// Event is the signal sent to a subscriber. It carries no task data: the client refetches
// via the REST endpoints, reusing the existing DTOs so the stream can't drift from them.
type Event struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`      // task id for evtTaskChanged
	Session string `json:"session,omitempty"` // session id for evtSessionChanged
}

const (
	evtTaskChanged    = "task-changed"
	evtTasksChanged   = "tasks-changed"
	evtSessionChanged = "session-changed"
)

// eventVisibleToScope performs server-side authorization for an SSE signal before it is
// serialized. File watchers are shared per physical cluster root, so client-side filtering
// would leak foreign task/session activity. Unknown/deleted/racing records fail closed.
func eventVisibleToScope(scope requestScope, event Event, includeAll bool) bool {
	if scope.Standalone {
		// A standalone root is already private. It never has a valid all-projects
		// expansion, and its watcher cannot legitimately carry sibling events.
		return !includeAll
	}
	if scope.Legacy || scope.Mode != "carbon" || scope.ProjectID == "" || includeAll {
		return true
	}
	st := store.New(scope.Root)
	switch event.Type {
	case evtTaskChanged:
		return eventTaskInProject(st, event.ID, scope.ProjectID)
	case evtSessionChanged:
		if event.Session == "" {
			return false
		}
		sessionDoc, err := st.GetSession(event.Session)
		if err != nil {
			return false
		}
		return eventTaskInProject(st, sessionDoc.Session.TaskID, scope.ProjectID)
	case evtTasksChanged:
		// A list-level event carries no affected IDs (it can be a config or multiple
		// writes), so it cannot be attributed safely to one project. Suppress it rather
		// than leaking cluster activity; explicit include_cluster receives it normally.
		return false
	default:
		return false
	}
}

func eventTaskInProject(st *store.Store, id, projectID string) bool {
	if id == "" {
		return false
	}
	if doc, err := st.Get(id); err == nil {
		return doc.Task.ProjectID == projectID
	}
	// Soft deletion moves a task out of the active directory before fsnotify emits the
	// final rename/remove. Looking in trash preserves the owning project's refresh while
	// still suppressing a permanently removed or racing foreign record.
	if doc, err := st.GetTrash(id); err == nil {
		return doc.Task.ProjectID == projectID
	}
	return false
}

// classify maps a changed path to its impact. kind is one of the constants below.
const (
	kindIgnore = iota
	kindTask
	kindSession
	kindList
)

func classify(path string) (id string, kind int) {
	base := filepath.Base(path)
	switch {
	case strings.HasPrefix(base, ".tmp-"):
		return "", kindIgnore // atomic-write temp file (store.atomicWrite)
	case base == "config.yaml":
		return "", kindList // board states changed
	case filepath.Base(filepath.Dir(path)) == "tasks" && strings.HasSuffix(base, ".md"):
		return strings.TrimSuffix(base, ".md"), kindTask
	case filepath.Base(filepath.Dir(path)) == "sessions" && strings.HasSuffix(base, ".yaml"):
		return strings.TrimSuffix(base, ".yaml"), kindSession
	case filepath.Base(filepath.Dir(path)) == "live" && strings.HasSuffix(base, ".json"):
		return strings.TrimSuffix(base, ".json"), kindSession
	default:
		return "", kindIgnore
	}
}

// coalesce consumes raw changed-file paths, debounces them by d, and emits one Event per
// quiet window. A single atomic save fans out into several raw fs events (temp create,
// rename, chmod); debouncing collapses them. One task touched in a window -> task-changed;
// anything else (config, multiple tasks) -> tasks-changed. It returns when in is closed.
func coalesce(in <-chan string, d time.Duration, emit func(Event)) {
	timer := time.NewTimer(d)
	timer.Stop()

	taskIDs := map[string]struct{}{}
	sessionIDs := map[string]struct{}{}
	listLevel := false
	armed := false

	reset := func() {
		taskIDs = map[string]struct{}{}
		sessionIDs = map[string]struct{}{}
		listLevel = false
		armed = false
	}

	for {
		select {
		case path, ok := <-in:
			if !ok {
				return
			}
			id, kind := classify(path)
			switch kind {
			case kindIgnore:
				continue
			case kindList:
				listLevel = true
			case kindTask:
				taskIDs[id] = struct{}{}
			case kindSession:
				sessionIDs[id] = struct{}{}
			}
			if !armed {
				armed = true
			}
			timer.Reset(d) // trailing debounce: last event in a burst wins
		case <-timer.C:
			if !armed {
				continue
			}
			emit(buildEvent(taskIDs, sessionIDs, listLevel))
			reset()
		}
	}
}

func buildEvent(taskIDs, sessionIDs map[string]struct{}, listLevel bool) Event {
	// A config/board-level change, or more than one task touched in the window, can't be
	// expressed as a single task-changed signal — fall back to the list-wide refresh.
	if listLevel || len(taskIDs) > 1 {
		return Event{Type: evtTasksChanged}
	}
	// Exactly one task touched — possibly alongside its own session/live writes in the same
	// window. Target that task so the open detail, runs, and sessions all refresh live; the
	// client's task-changed branch invalidates this id's session queries too, so a coincident
	// session write is covered. (Previously a session in the window downgraded this to a
	// list-only refresh, leaving the open task detail stale.)
	if len(taskIDs) == 1 {
		for id := range taskIDs {
			return Event{Type: evtTaskChanged, ID: id}
		}
	}
	// No task touched: only session/live writes. Signal a session-level refresh. The id is
	// advisory — the client refreshes all sessions for the path regardless of how many changed.
	if len(sessionIDs) >= 1 {
		var id string
		for s := range sessionIDs {
			id = s
		}
		return Event{Type: evtSessionChanged, Session: id}
	}
	return Event{Type: evtTasksChanged} // unreachable: coalesce only emits when something was classified
}

// Hub fans filesystem changes out to SSE subscribers. It keeps one fsnotify watcher per
// project root, started on the first subscriber for that root and stopped when the last
// one leaves (ref-counted), so idle projects are not watched.
type Hub struct {
	debounce time.Duration

	mu     sync.Mutex
	roots  map[string]*rootWatch
	nextID int
}

// NewHub returns a Hub. debounce is the quiet window used to coalesce a save's event burst;
// pass 0 to use the default.
func NewHub(debounce time.Duration) *Hub {
	if debounce <= 0 {
		debounce = 100 * time.Millisecond
	}
	return &Hub{debounce: debounce, roots: map[string]*rootWatch{}}
}

// rootWatch is the shared watcher + subscriber set for one root.
type rootWatch struct {
	watcher *fsnotify.Watcher
	subs    map[int]chan Event

	// done is closed before the fsnotify handle. It lets the reader stop even if it
	// is currently trying to hand a burst to the coalescer, while wg makes the last
	// unsubscribe wait until both watcher goroutines have released the TempDir.
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Subscribe registers a subscriber for root, lazily starting its watcher. The returned
// channel receives coalesced Events; cancel removes the subscriber and tears down the
// watcher once the last subscriber for the root is gone. cancel is idempotent.
func (h *Hub) Subscribe(root string) (<-chan Event, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	rw := h.roots[root]
	if rw == nil {
		var err error
		rw, err = h.startWatcher(root)
		if err != nil {
			return nil, nil, err
		}
		h.roots[root] = rw
	}

	id := h.nextID
	h.nextID++
	ch := make(chan Event, 8)
	rw.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() { h.unsubscribe(root, id) })
	}
	return ch, cancel, nil
}

// startWatcher creates the fsnotify watcher for root, adds the .carbon dirs, and launches
// the read+coalesce+broadcast goroutine. Caller holds h.mu.
func (h *Hub) startWatcher(root string) (*rootWatch, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	carbon, err := repo.EnsureCarbonDirs(root, "tasks", "sessions", "live")
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("watcher: secure Carbon directories: %w", err)
	}
	dirs := []string{carbon}
	for _, name := range []string{"tasks", "sessions", "live"} {
		dir, err := repo.ValidateCarbonPath(root, filepath.Join(carbon, name))
		if err != nil {
			w.Close()
			return nil, fmt.Errorf("watcher: validate %s: %w", name, err)
		}
		dirs = append(dirs, dir)
	}
	// Watch the dirs, not the files: atomic temp+rename swaps inodes (store.atomicWrite).
	for _, dir := range dirs {
		if err := w.Add(dir); err != nil {
			w.Close()
			return nil, fmt.Errorf("watcher: watch %s: %w", dir, err)
		}
	}

	rw := &rootWatch{
		watcher: w,
		subs:    map[int]chan Event{},
		done:    make(chan struct{}),
	}

	raw := make(chan string, 64)
	rw.wg.Add(1)
	go func() {
		defer rw.wg.Done()
		defer close(raw)

		events := w.Events
		errs := w.Errors
		for events != nil || errs != nil {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				// Renamed-into-place files arrive as Create; treat Create/Write/Remove alike.
				select {
				case raw <- ev.Name:
				case <-rw.done:
					return
				}
			case _, ok := <-errs:
				if !ok {
					errs = nil
				}
				// fsnotify errors are advisory. Keep consuming events until the watcher is
				// stopped; otherwise an error burst can stall the platform watcher.
			case <-rw.done:
				return
			}
		}
	}()
	rw.wg.Add(1)
	go func() {
		defer rw.wg.Done()
		coalesce(raw, h.debounce, func(e Event) { h.broadcast(root, rw, e) })
	}()

	return rw, nil
}

// unsubscribe removes a subscriber and, if it was the last for the root, stops the watcher.
func (h *Hub) unsubscribe(root string, id int) {
	h.mu.Lock()
	rw := h.roots[root]
	if rw == nil {
		h.mu.Unlock()
		return
	}
	if ch, ok := rw.subs[id]; ok {
		delete(rw.subs, id)
		close(ch)
	}
	if len(rw.subs) == 0 {
		delete(h.roots, root)
		h.mu.Unlock()
		// Do not wait while holding h.mu: coalesce may be inside broadcast and needs
		// that mutex to observe that this root is gone.
		rw.stopAndWait()
		return
	}
	h.mu.Unlock()
}

// stopAndWait releases the platform watcher and joins every goroutine that can retain
// a directory handle. fsnotify on Windows otherwise makes t.TempDir cleanup race a
// still-running reader after the final SSE subscriber disconnects.
func (rw *rootWatch) stopAndWait() {
	rw.stopOnce.Do(func() {
		close(rw.done)
		_ = rw.watcher.Close()
	})
	rw.wg.Wait()
}

// broadcast delivers e to every subscriber of root, dropping for any slow subscriber whose
// buffer is full rather than stalling the watcher.
func (h *Hub) broadcast(root string, source *rootWatch, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rw := h.roots[root]
	// A replacement subscription may have started after this source was removed.
	// Never let a stale debounce signal cross into the new root watcher.
	if rw != source {
		return
	}
	for _, ch := range rw.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// activeRoots reports how many roots currently have a live watcher (introspection for
// tests and diagnostics).
func (h *Hub) activeRoots() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.roots)
}
