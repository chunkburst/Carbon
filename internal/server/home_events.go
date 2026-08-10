package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"carbon/internal/home"
)

const evtCatalogChanged = "catalog-changed"

// homeEvent is deliberately only a refresh hint. The Home manifest remains available
// exclusively through the normal Home API, so this stream cannot grow into a second
// catalog transport with a subtly different authorization or serialization contract.
type homeEvent struct {
	Type string `json:"type"`
}

// handleHomeEvents streams Home catalog commit hints. It is intentionally separate
// from /api/events: task stores and the Home catalog have different roots, scopes,
// filtering rules, and lifetimes.
func (s *Server) handleHomeEvents(w http.ResponseWriter, r *http.Request) {
	root, err := s.homeRoot(r, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch, cancel, err := s.homeHub.Subscribe(root)
	if err != nil {
		writeHomeErr(w, err)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Match task SSE's immediate proof that a subscription is live. EventSource
	// ignores comments, and callers need not wait for a catalog mutation to know the
	// Home watcher is installed.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// HomeHub shares one watcher per Home root among its subscribers. Unlike Hub, it
// never initializes storage: subscribing is a read-only operation and an uninitialized
// Home must stay uninitialized after a failed or stale EventSource connection.
type HomeHub struct {
	debounce time.Duration

	mu     sync.Mutex
	roots  map[string]*homeRootWatch
	nextID int
}

type homeRootWatch struct {
	watcher *fsnotify.Watcher
	subs    map[int]chan homeEvent
}

func NewHomeHub(debounce time.Duration) *HomeHub {
	if debounce <= 0 {
		debounce = 100 * time.Millisecond
	}
	return &HomeHub{debounce: debounce, roots: map[string]*homeRootWatch{}}
}

// Subscribe adds one Home stream client. The Home metadata directory is validated by
// home.Open before it is watched, including the no-symlink/no-reparse policy used by
// every other catalog read and write.
func (h *HomeHub) Subscribe(root string) (<-chan homeEvent, func(), error) {
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
	ch := make(chan homeEvent, 8)
	rw.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() { h.unsubscribe(root, id) })
	}
	return ch, cancel, nil
}

func (h *HomeHub) startWatcher(root string) (*homeRootWatch, error) {
	// Open and then Manifest both validate the direct .carbon child. Do not use the
	// task-store helper here: repo.EnsureCarbonDirs would create task directories in a
	// Home and would make a passive watcher mutate user state.
	homeHandle, err := home.Open(root)
	if err != nil {
		return nil, err
	}
	if _, err := homeHandle.Manifest(); err != nil {
		return nil, err
	}
	carbonRoot := homeHandle.CarbonRoot

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	// Watch the parent directory rather than home.json: atomic replacement swaps the
	// file inode. Filtering below accepts only the committed manifest filename.
	if err := w.Add(carbonRoot); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("home watcher: watch metadata directory: %w", err)
	}

	rw := &homeRootWatch{watcher: w, subs: map[int]chan homeEvent{}}
	raw := make(chan struct{}, 1)
	go func() {
		defer close(raw)
		for {
			select {
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if !isHomeManifestEvent(carbonRoot, event.Name) {
					continue
				}
				// A pending notification is enough; coalesceHome applies a trailing
				// debounce and verifies a readable manifest before it emits anything.
				select {
				case raw <- struct{}{}:
				default:
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				// fsnotify errors are advisory. The next durable commit will be
				// independently revalidated before a hint is sent.
			}
		}
	}()
	go coalesceHome(raw, h.debounce, func() {
		if !homeManifestReadable(root) {
			return
		}
		h.broadcast(root, rw, homeEvent{Type: evtCatalogChanged})
	})

	return rw, nil
}

// isHomeManifestEvent accepts only a direct event for .carbon/home.json. In
// particular, temporary atomic-write files, presentation metadata, and task-data
// directories below a Home's .carbon directory cannot cause catalog refreshes.
func isHomeManifestEvent(carbonRoot, filename string) bool {
	rel, err := filepath.Rel(carbonRoot, filename)
	if err != nil || filepath.Dir(rel) != "." || filepath.Base(rel) != home.ManifestFilename {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(rel, home.ManifestFilename)
	}
	return rel == home.ManifestFilename
}

// homeManifestReadable confirms the atomic rename has produced a durable, valid
// catalog before clients are asked to fetch it. A transient remove/rename sequence
// therefore never turns into a hint for a partially published manifest.
func homeManifestReadable(root string) bool {
	homeHandle, err := home.Open(root)
	if err != nil {
		return false
	}
	_, err = homeHandle.Manifest()
	return err == nil
}

func coalesceHome(in <-chan struct{}, debounce time.Duration, emit func()) {
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case _, ok := <-in:
			if !ok {
				return
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounce)
		case <-timerC:
			timer = nil
			timerC = nil
			emit()
		}
	}
}

func (h *HomeHub) unsubscribe(root string, id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rw := h.roots[root]
	if rw == nil {
		return
	}
	if ch, ok := rw.subs[id]; ok {
		delete(rw.subs, id)
		close(ch)
	}
	if len(rw.subs) == 0 {
		_ = rw.watcher.Close()
		delete(h.roots, root)
	}
}

func (h *HomeHub) broadcast(root string, source *homeRootWatch, event homeEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// An old watcher may finish its debounce after a last unsubscribe and a new
	// subscription for the same Home. Keep that stale signal out of the new stream.
	if h.roots[root] != source {
		return
	}
	for _, ch := range source.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *HomeHub) activeRoots() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.roots)
}
