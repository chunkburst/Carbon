package worklog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"

	"gopkg.in/yaml.v3"
)

func worklogStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repo.CarbonDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	return store.New(root), root
}

func worklogManager(t *testing.T) (*Manager, string) {
	t.Helper()
	s, root := worklogStore(t)
	return New(s, func() time.Time { return time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC) }), root
}

func logDraft() Log {
	return Log{
		Worker:     "agent:codex",
		Visibility: ProjectPublic,
		ClusterID:  "cluster_" + strings.Repeat("a", 32),
		ProjectID:  "project_" + strings.Repeat("b", 32),
		TaskID:     "CAR-123",
		Title:      "Implemented durable work logs",
		Body:       "Created strict storage.\n\n- tests\n- versioning\n",
		Tags:       []string{"storage", "MCP"},
	}
}

func explicitID(n int) string { return fmt.Sprintf("log_%032x", n) }

func TestCRUDRoundTripAndAuditStamping(t *testing.T) {
	m, root := worklogManager(t)
	created, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "log_") || len(created.ID) != len("log_")+32 {
		t.Fatalf("generated id = %q", created.ID)
	}
	if created.Version == "" || created.ETag() == "" {
		t.Fatalf("created version = %#v", created)
	}
	if created.CreatedBy != "agent:codex" || created.UpdatedBy != "agent:codex" || created.CreatedAt != created.UpdatedAt {
		t.Fatalf("created audit = %#v", created)
	}

	raw, err := os.ReadFile(filepath.Join(root, repo.CarbonDirName, dataDir, filename(created.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "version:") {
		t.Fatalf("Version leaked into durable YAML:\n%s", raw)
	}

	got, err := m.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("round trip = %#v\nwant %#v", got, created)
	}

	updatedDraft := got
	updatedDraft.Title = "Updated by a human reviewer"
	updatedDraft.Tags = append(updatedDraft.Tags, "reviewed")
	// Different actor and Worker are intentionally allowed by this storage layer. Scope
	// policy above it decides who is authorized to perform this write.
	updated, err := m.Update(context.Background(), "human:owner", updatedDraft, got.ETag())
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != created.CreatedAt || updated.CreatedBy != created.CreatedBy {
		t.Fatalf("creation audit changed: %#v", updated)
	}
	if updated.UpdatedBy != "human:owner" || updated.Version == created.Version {
		t.Fatalf("update audit/version = %#v", updated)
	}

	if err := m.Delete(context.Background(), "human:owner", updated.ID, updated.ETag()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(updated.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestStandaloneLogRequiresProjectAndNoCluster(t *testing.T) {
	m, _ := worklogManager(t)
	standalone := logDraft()
	standalone.Standalone = true
	standalone.ClusterID = ""
	created, err := m.Create(context.Background(), "agent:codex", standalone)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Standalone || created.ClusterID != "" || created.ProjectID == "" {
		t.Fatalf("standalone round trip = %#v", created)
	}

	missingProject := standalone
	missingProject.ProjectID = ""
	if _, err := m.Create(context.Background(), "agent:codex", missingProject); !errors.Is(err, ErrInvalidWorkLog) {
		t.Fatalf("standalone without project = %v, want ErrInvalidWorkLog", err)
	}
	withCluster := standalone
	withCluster.ClusterID = "cluster_" + strings.Repeat("a", 32)
	if _, err := m.Create(context.Background(), "agent:codex", withCluster); !errors.Is(err, ErrInvalidWorkLog) {
		t.Fatalf("standalone with cluster = %v, want ErrInvalidWorkLog", err)
	}
}

func TestVersionPreconditions(t *testing.T) {
	m, _ := worklogManager(t)
	created, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	stale := created.ETag()
	created.Title = "A newer body of work"
	updated, err := m.Update(context.Background(), "agent:codex", created, stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(context.Background(), "agent:codex", updated, stale); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale update = %v, want ErrVersionMismatch", err)
	}
	if err := m.Delete(context.Background(), "agent:codex", updated.ID, stale); !errors.Is(err, store.ErrVersionMismatch) {
		t.Fatalf("stale delete = %v, want ErrVersionMismatch", err)
	}
	if err := m.Delete(context.Background(), "agent:codex", updated.ID, updated.Version); err != nil {
		t.Fatalf("raw-version delete = %v", err)
	}
}

func TestValidationAndFilterBounds(t *testing.T) {
	m, _ := worklogManager(t)
	tooManyTags := make([]string, maxTags+1)
	for i := range tooManyTags {
		tooManyTags[i] = fmt.Sprintf("tag-%d", i)
	}
	cases := []struct {
		name   string
		mutate func(*Log)
	}{
		{"invalid worker", func(log *Log) { log.Worker = " agent:codex" }},
		{"bad visibility", func(log *Log) { log.Visibility = "personal" }},
		{"public needs project", func(log *Log) { log.ProjectID = "" }},
		{"unsafe cluster id", func(log *Log) { log.ClusterID = "../cluster" }},
		{"title control", func(log *Log) { log.Title = "bad\x00title" }},
		{"body control", func(log *Log) { log.Body = "bad\x00body" }},
		{"duplicate tags", func(log *Log) { log.Tags = []string{"same", "SAME"} }},
		{"too many tags", func(log *Log) { log.Tags = tooManyTags }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := logDraft()
			tc.mutate(&log)
			if _, err := m.Create(context.Background(), "agent:codex", log); !errors.Is(err, ErrInvalidWorkLog) {
				t.Fatalf("Create error = %v, want ErrInvalidWorkLog", err)
			}
		})
	}

	for _, limit := range []int{0, MaxListLimit + 1} {
		if _, err := m.List(Filter{Limit: limit}); !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("List limit %d = %v, want ErrInvalidFilter", limit, err)
		}
	}
	if _, err := m.List(Filter{Worker: "agent:codex\nnext", Limit: 1}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("invalid worker filter = %v, want ErrInvalidFilter", err)
	}
}

func TestStrictYAMLAndCorruptDataFailClosed(t *testing.T) {
	m, root := worklogManager(t)
	created, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, repo.CarbonDirName, dataDir, filename(created.ID))
	data, err := yaml.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, append(data, []byte("unexpected: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(created.ID); !errors.Is(err, ErrInvalidWorkLog) {
		t.Fatalf("unknown YAML field = %v, want ErrInvalidWorkLog", err)
	}

	if err := os.WriteFile(filename, []byte("id: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(created.ID); !errors.Is(err, ErrInvalidWorkLog) {
		t.Fatalf("corrupt YAML = %v, want ErrInvalidWorkLog", err)
	}
}

func TestListFiltersDeterminismAndDefensiveCopies(t *testing.T) {
	m, _ := worklogManager(t)
	entries := []Log{
		{ID: explicitID(3), Worker: "agent:one", Visibility: WorkerPrivate, ClusterID: "cluster_" + strings.Repeat("a", 32), Title: "third", Tags: []string{"private"}},
		{ID: explicitID(1), Worker: "agent:one", Visibility: ProjectPublic, ClusterID: "cluster_" + strings.Repeat("a", 32), ProjectID: "project_" + strings.Repeat("b", 32), TaskID: "CAR-1", Title: "first", Tags: []string{"project"}},
		{ID: explicitID(2), Worker: "agent:two", Visibility: GlobalPublic, ClusterID: "cluster_" + strings.Repeat("a", 32), ProjectID: "project_" + strings.Repeat("c", 32), TaskID: "CAR-2", Title: "second", Tags: []string{"global"}},
	}
	for _, log := range entries {
		if _, err := m.Create(context.Background(), "agent:writer", log); err != nil {
			t.Fatal(err)
		}
	}

	all, err := m.List(Filter{Limit: MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(all), []string{explicitID(1), explicitID(2), explicitID(3)}; !slicesEqual(got, want) {
		t.Fatalf("deterministic ids = %v, want %v", got, want)
	}

	cases := []struct {
		filter Filter
		want   []string
	}{
		{Filter{Worker: "agent:one", Limit: 10}, []string{explicitID(1), explicitID(3)}},
		{Filter{Visibility: ProjectPublic, Limit: 10}, []string{explicitID(1)}},
		{Filter{ProjectID: "project_" + strings.Repeat("c", 32), Limit: 10}, []string{explicitID(2)}},
		{Filter{TaskID: "CAR-1", Limit: 10}, []string{explicitID(1)}},
		{Filter{Limit: 1}, []string{explicitID(1)}},
	}
	for _, tc := range cases {
		got, err := m.List(tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		if actual := ids(got); !slicesEqual(actual, tc.want) {
			t.Fatalf("List(%+v) = %v, want %v", tc.filter, actual, tc.want)
		}
	}

	all[0].Tags[0] = "mutated"
	again, err := m.Get(explicitID(1))
	if err != nil {
		t.Fatal(err)
	}
	if again.Tags[0] != "project" {
		t.Fatalf("returned tags alias durable data: %v", again.Tags)
	}
}

func TestSymlinkedWorkLogDataCannotEscapeStore(t *testing.T) {
	m, root := worklogManager(t)
	created, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, repo.CarbonDirName, dataDir, filename(created.ID))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("creating test symlink is unavailable: %v", err)
	}
	if _, err := m.Get(created.ID); !errors.Is(err, store.ErrPathOutsideRoot) {
		t.Fatalf("Get through external symlink = %v, want ErrPathOutsideRoot", err)
	}
}

func TestSymlinkedWorkLogDirectoryCannotEscapeStore(t *testing.T) {
	m, root := worklogManager(t)
	created, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	worklogs := filepath.Join(root, repo.CarbonDirName, dataDir)
	if err := os.Remove(filepath.Join(worklogs, filename(created.ID))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(worklogs); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, worklogs); err != nil {
		t.Skipf("creating test directory symlink is unavailable: %v", err)
	}
	if _, err := m.List(Filter{Limit: 1}); !errors.Is(err, store.ErrPathOutsideRoot) {
		t.Fatalf("List through external worklogs directory = %v, want ErrPathOutsideRoot", err)
	}
}

func TestConcurrentCreateAndVersionedUpdate(t *testing.T) {
	m, _ := worklogManager(t)
	const writers = 24
	errs := make(chan error, writers)
	var creates sync.WaitGroup
	for i := 0; i < writers; i++ {
		creates.Add(1)
		go func(i int) {
			defer creates.Done()
			log := logDraft()
			log.Title = fmt.Sprintf("concurrent create %d", i)
			_, err := m.Create(context.Background(), "agent:codex", log)
			errs <- err
		}(i)
	}
	creates.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create = %v", err)
		}
	}
	listed, err := m.List(Filter{Limit: MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != writers {
		t.Fatalf("concurrent creates listed %d records, want %d", len(listed), writers)
	}

	base, err := m.Create(context.Background(), "agent:codex", logDraft())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, writers)
	var updates sync.WaitGroup
	for i := 0; i < writers; i++ {
		updates.Add(1)
		go func(i int) {
			defer updates.Done()
			candidate := clone(base)
			candidate.Title = fmt.Sprintf("competing update %d", i)
			<-start
			_, err := m.Update(context.Background(), "agent:writer", candidate, base.ETag())
			results <- err
		}(i)
	}
	close(start)
	updates.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, store.ErrVersionMismatch) {
			t.Fatalf("concurrent Update = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("versioned update successes = %d, want 1", successes)
	}
	if _, err := m.Get(base.ID); err != nil {
		t.Fatalf("final concurrent record is unreadable: %v", err)
	}
}

func ids(logs []Log) []string {
	out := make([]string, len(logs))
	for i, log := range logs {
		out[i] = log.ID
	}
	return out
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
