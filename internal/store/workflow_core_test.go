package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCarbonFieldsETagAndTransactionalBulk(t *testing.T) {
	s := New(repo(t, map[string]string{}))
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	first, err := s.Create(Draft{Title: "one", ProjectID: "p1", ProjectIDSet: true, Type: "plugin", Importance: "core"}, "agent:a", at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(Draft{Title: "two", ProjectID: "p1", ProjectIDSet: true}, "agent:a", at)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.Version == "" || first.ETag() == "" || first.Task.Type != "plugin" || first.Task.Importance != "core" {
		t.Fatalf("created carbon task = %+v etag=%q", first.Task, first.ETag())
	}

	oldETag := first.ETag()
	project := "p2"
	assignee := "human:li"
	changed, err := s.BulkUpdate(context.Background(), "human:li", BulkUpdate{
		IDs:              []string{first.Task.ID, second.Task.ID},
		ExpectedVersions: map[string]string{first.Task.ID: first.ETag(), second.Task.ID: second.ETag()},
		ProjectID:        &project, Assignee: &assignee, Reason: "move project ownership",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 || changed[0].Task.ProjectID != "p2" || changed[0].Task.Assignee != assignee {
		t.Fatalf("bulk result = %+v", changed)
	}
	if changed[0].ETag() == oldETag {
		t.Fatal("etag did not advance")
	}
	if got := changed[0].Provenance[len(changed[0].Provenance)-2].Did; got != "project moved" {
		t.Fatalf("project audit = %q", got)
	}
	if got := changed[0].Provenance[len(changed[0].Provenance)-1].Did; got != "assignee changed" {
		t.Fatalf("assignee audit = %q", got)
	}

	project = "p3"
	_, err = s.BulkUpdate(context.Background(), "human:li", BulkUpdate{
		IDs:              []string{first.Task.ID, second.Task.ID},
		ExpectedVersions: map[string]string{first.Task.ID: oldETag, second.Task.ID: second.ETag()}, ProjectID: &project,
	})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale bulk = %v, want version mismatch", err)
	}
	got, err := s.Get(second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.ProjectID != "p2" {
		t.Fatalf("failed bulk partially wrote second task: %+v", got.Task)
	}
}

func TestImportTaskPreservesUnknownFrontmatterAndBody(t *testing.T) {
	source := New(repo(t, map[string]string{"PROJ-001": `---
id: PROJ-001
title: import me
status: backlog
future_key: retain-me
---

Body must remain.
`}))
	doc, err := source.Get("PROJ-001")
	if err != nil {
		t.Fatal(err)
	}
	target := New(repo(t, map[string]string{}))
	project := "target-project"
	imported, receipt, err := target.ImportTask(context.Background(), "system:migration", ImportTaskInput{Doc: doc, TargetID: "PROJ-009", TargetProjectID: &project})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SourceID != "PROJ-001" || receipt.TargetID != "PROJ-009" || imported.Task.ProjectID != project {
		t.Fatalf("receipt/import = %+v %+v", receipt, imported.Task)
	}
	raw, err := ReadDataForTest(target, "PROJ-009")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !containsAll(string(raw), "future_key: retain-me", "Body must remain.") {
		t.Fatalf("lossless import changed content:\n%s", raw)
	}
}

// ReadDataForTest stays package-private in effect (tests share store) and exercises the
// same managed task path as an external migration would get through ImportTask.
func ReadDataForTest(s *Store, id string) ([]byte, error) {
	path, err := s.taskFilePath(id, false, true, false)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
