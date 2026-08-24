package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"carbon/internal/home"
)

func TestHomeSSEStreamsOneReadableCatalogHintAfterAtomicMutation(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewWithScope("human:test", ScopeDefaults{}).Handler())
	defer srv.Close()
	resp, reader := openHomeEventStream(t, srv.URL, root)
	defer resp.Body.Close()

	created, err := home.AddStandaloneProject(root, home.AddProjectRequest{
		Name: "Created by another Home client", SourcePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	line, err := nextHomeSSEData(reader)
	if err != nil {
		t.Fatalf("read Home SSE: %v", err)
	}
	var event homeEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("decode Home SSE %q: %v", line, err)
	}
	if event != (homeEvent{Type: evtCatalogChanged}) {
		t.Fatalf("event = %#v, want only catalog-changed hint", event)
	}

	// The watcher validates the final rename target before broadcasting. Receiving a
	// hint must therefore mean a fresh Home GET can already observe the new project.
	opened, err := home.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := opened.Manifest()
	if err != nil {
		t.Fatalf("manifest was not readable when hint arrived: %v", err)
	}
	if !manifestHasStandaloneProject(manifest, created.ID) {
		t.Fatalf("manifest after Home SSE omitted %s: %#v", created.ID, manifest.Projects)
	}

	// One atomic home.json replacement may report several raw filesystem operations;
	// the public stream must still contain exactly one catalog hint.
	extra := make(chan string, 1)
	go func() {
		if data, err := nextHomeSSEData(reader); err == nil {
			extra <- data
		}
	}()
	select {
	case data := <-extra:
		t.Fatalf("unexpected second Home hint for one mutation: %q", data)
	case <-time.After(350 * time.Millisecond):
	}
}

func TestHomeHubIsolatesHomesAndIgnoresNonManifestFiles(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if _, err := home.Ensure(root); err != nil {
			t.Fatal(err)
		}
	}

	hub := NewHomeHub(15 * time.Millisecond)
	firstEvents, cancelFirst, err := hub.Subscribe(first)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelFirst()
	secondEvents, cancelSecond, err := hub.Subscribe(second)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSecond()

	if _, err := home.AddStandaloneProject(first, home.AddProjectRequest{Name: "Only first", SourcePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-firstEvents:
		if event != (homeEvent{Type: evtCatalogChanged}) {
			t.Fatalf("first Home event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Home did not receive its catalog hint")
	}
	select {
	case event := <-secondEvents:
		t.Fatalf("second Home leaked first Home event: %#v", event)
	case <-time.After(120 * time.Millisecond):
	}

	if err := os.WriteFile(filepath.Join(first, home.CarbonDirName, "presentation.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-firstEvents:
		t.Fatalf("non-manifest file produced catalog hint: %#v", event)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestHomeHubRefCountedTeardownAndNeverInitializesTaskStorage(t *testing.T) {
	root := t.TempDir()
	hub := NewHomeHub(10 * time.Millisecond)
	if _, _, err := hub.Subscribe(root); !errors.Is(err, home.ErrNotInitialized) {
		t.Fatalf("uninitialized Home subscription = %v, want ErrNotInitialized", err)
	}
	if _, err := os.Stat(filepath.Join(root, home.CarbonDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passive Home watcher created metadata directory: %v", err)
	}

	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	_, cancelOne, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	_, cancelTwo, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.activeRoots(); got != 1 {
		t.Fatalf("two Home subscribers started %d watchers, want 1", got)
	}
	cancelOne()
	if got := hub.activeRoots(); got != 1 {
		t.Fatalf("Home watcher stopped with one subscriber remaining, got %d", got)
	}
	cancelTwo()
	deadline := time.Now().Add(time.Second)
	for hub.activeRoots() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Home watcher not torn down after last subscriber, roots=%d", hub.activeRoots())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHomeHubFinalUnsubscribeJoinsWatcherGoroutines(t *testing.T) {
	root := t.TempDir()
	if _, err := home.Ensure(root); err != nil {
		t.Fatal(err)
	}
	hub := NewHomeHub(10 * time.Millisecond)
	_, cancel, err := hub.Subscribe(root)
	if err != nil {
		t.Fatal(err)
	}

	hub.mu.Lock()
	rw := hub.roots[root]
	hub.mu.Unlock()
	if rw == nil {
		t.Fatal("subscription did not install a Home watcher")
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
		t.Fatal("final cancel returned before Home watcher goroutines stopped")
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove watched Home TempDir after final cancel: %v", err)
	}
}

func openHomeEventStream(t *testing.T, serverURL, root string) (*http.Response, *bufio.Reader) {
	t.Helper()
	query := url.Values{}
	query.Set("home", root)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/home/events?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("Home SSE status = %d: %s", resp.StatusCode, body)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		resp.Body.Close()
		t.Fatalf("Home SSE content type = %q, want text/event-stream", contentType)
	}
	return resp, bufio.NewReader(resp.Body)
}

func nextHomeSSEData(reader *bufio.Reader) (string, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			return strings.TrimSpace(data), nil
		}
	}
}

func manifestHasStandaloneProject(manifest home.Manifest, id string) bool {
	for _, project := range manifest.Projects {
		if project.ID == id {
			return true
		}
	}
	return false
}
