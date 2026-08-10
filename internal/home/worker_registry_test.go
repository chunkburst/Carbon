package home

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestWorkerRegistryMissingFileIsEmptyAndDoesNotWrite(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	workers, err := ListWorkerRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 0 {
		t.Fatalf("missing registry = %#v, want empty", workers)
	}
	if _, err := os.Lstat(filepath.Join(root, CarbonDirName, WorkerRegistryFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading missing registry created a file: %v", err)
	}
}

func TestWorkerRegistryResetDeleteAndActivityRevival(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	deleted := first.Add(time.Hour)
	revived := deleted.Add(time.Hour)

	reset, err := resetWorkerAt(root, "agent:codex", first)
	if err != nil {
		t.Fatal(err)
	}
	if reset.CreatedAt != first.Format(time.RFC3339Nano) || reset.UpdatedAt != reset.CreatedAt || reset.ResetAt != reset.CreatedAt || reset.DeletedAt != "" {
		t.Fatalf("reset record = %+v", reset)
	}

	tombstone, err := deleteWorkerAt(root, "agent:codex", deleted)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.DeletedAt != deleted.Format(time.RFC3339Nano) || tombstone.ResetAt != first.Format(time.RFC3339Nano) {
		t.Fatalf("tombstone = %+v", tombstone)
	}
	workers, err := ReconcileWorkerActivity(root, map[string]time.Time{"agent:codex": deleted.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if got := workers["agent:codex"].DeletedAt; got != tombstone.DeletedAt {
		t.Fatalf("pre-delete activity revived worker: %+v", workers["agent:codex"])
	}

	workers, err = ReconcileWorkerActivity(root, map[string]time.Time{"agent:codex": revived})
	if err != nil {
		t.Fatal(err)
	}
	got := workers["agent:codex"]
	if got.DeletedAt != "" || got.ResetAt != deleted.Format(time.RFC3339Nano) || got.UpdatedAt != revived.Format(time.RFC3339Nano) {
		t.Fatalf("revived record = %+v", got)
	}
	path := filepath.Join(root, CarbonDirName, WorkerRegistryFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"version\": 1,\n  \"workers\": {\n    \"agent:codex\": {\n      \"createdAt\": \"2026-08-08T10:00:00Z\",\n      \"updatedAt\": \"2026-08-08T12:00:00Z\",\n      \"resetAt\": \"2026-08-08T11:00:00Z\"\n    }\n  }\n}\n"
	if string(data) != want {
		t.Fatalf("registry document =\n%s\nwant=\n%s", data, want)
	}
}

func TestWorkerRegistryFailsClosedForCorruptAndFutureDocuments(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, CarbonDirName, WorkerRegistryFilename)
	for _, test := range []struct {
		name string
		data string
		want error
	}{
		{
			name: "unknown field",
			data: `{"version":1,"workers":{},"surprise":true}`,
			want: ErrInvalidWorkerRegistry,
		},
		{
			name: "unknown nested field",
			data: `{"version":1,"workers":{"agent:one":{"createdAt":"2026-08-08T10:00:00Z","updatedAt":"2026-08-08T10:00:00Z","surprise":true}}}`,
			want: ErrInvalidWorkerRegistry,
		},
		{
			name: "duplicate JSON key",
			data: `{"version":1,"workers":{},"workers":{}}`,
			want: ErrInvalidWorkerRegistry,
		},
		{
			name: "bad timestamp",
			data: `{"version":1,"workers":{"agent:one":{"createdAt":"never","updatedAt":"never"}}}`,
			want: ErrInvalidWorkerRegistry,
		},
		{
			name: "future version",
			data: `{"version":2,"workers":{}}`,
			want: ErrFutureWorkerRegistryVersion,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ListWorkerRegistry(root); !errors.Is(err, test.want) {
				t.Fatalf("ListWorkerRegistry = %v, want %v", err, test.want)
			}
			if _, err := ResetWorker(root, "agent:new"); !errors.Is(err, test.want) {
				t.Fatalf("ResetWorker = %v, want %v", err, test.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("corrupt registry changed\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

func TestWorkerRegistryConcurrentActivityPreservesEveryActor(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		identity := strconv.Itoa(i)
		actor := "agent:worker-" + identity
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ReconcileWorkerActivity(root, map[string]time.Time{actor: time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent activity = %v", err)
		}
	}
	registry, err := ListWorkerRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry) != workers {
		t.Fatalf("registry length = %d, want %d: %#v", len(registry), workers, registry)
	}
	for i := 0; i < workers; i++ {
		actor := "agent:worker-" + strconv.Itoa(i)
		if _, exists := registry[actor]; !exists {
			t.Fatalf("missing concurrent worker %s", actor)
		}
	}
}

func TestWorkerRegistryRejectsInvalidActorWithoutWriting(t *testing.T) {
	useHomeTestCache(t)
	root := t.TempDir()
	if _, err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	if _, err := ResetWorker(root, " agent:codex"); !errors.Is(err, ErrInvalidWorkerRegistryActor) {
		t.Fatalf("ResetWorker invalid actor = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, CarbonDirName, WorkerRegistryFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid actor wrote registry: %v", err)
	}
}
