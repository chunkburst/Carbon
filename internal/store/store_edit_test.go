package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"carbon/internal/task"
)

func TestSetTitleAndBodyPreservesUnknownKeys(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, err := s.Get("PROJ-002")
	if err != nil {
		t.Fatal(err)
	}
	_ = d.SetTitle("Renamed task")
	_ = d.SetBody("Rewritten body.\n")
	if err := s.Save(d); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(s.tasksDir(), "PROJ-002.md"))
	got := string(raw)
	if !strings.Contains(got, "title: Renamed task") {
		t.Fatalf("title not updated:\n%s", got)
	}
	if !strings.Contains(got, "priority: high") {
		t.Fatalf("unknown key 'priority' dropped:\n%s", got)
	}
	if !strings.HasSuffix(got, "Rewritten body.\n") {
		t.Fatalf("body not rewritten:\n%s", got)
	}

	reloaded, _ := s.Get("PROJ-002")
	if reloaded.Task.Title != "Renamed task" || reloaded.Body != "Rewritten body.\n" {
		t.Fatalf("reload mismatch: title=%q body=%q", reloaded.Task.Title, reloaded.Body)
	}
}

func TestSetChecksAddRemoveModify(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")

	// Keep the first check (carry its result), modify its desc, and add a new manual check.
	d.Task.Checks[0].Result = "pass"
	d.Task.Checks[0].Desc = "tests still pass"
	newChecks := append(d.Task.Checks, task.Check{Desc: "manual review", Type: "manual"})
	if err := d.SetChecks(newChecks); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}

	reloaded, _ := s.Get("PROJ-002")
	if len(reloaded.Task.Checks) != 2 {
		t.Fatalf("want 2 checks, got %d", len(reloaded.Task.Checks))
	}
	if reloaded.Task.Checks[0].Result != "pass" || reloaded.Task.Checks[0].Desc != "tests still pass" {
		t.Fatalf("retained check not carried: %+v", reloaded.Task.Checks[0])
	}
	if reloaded.Task.Checks[1].Result != "pending" {
		t.Fatalf("new check should default pending: %+v", reloaded.Task.Checks[1])
	}

	// Removing all checks deletes the key.
	if err := d.SetChecks(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.tasksDir(), "PROJ-002.md"))
	if strings.Contains(string(raw), "checks:") {
		t.Fatalf("checks key not removed:\n%s", raw)
	}
}

func TestDeleteTask(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask}))
	if err := s.DeleteTask("PROJ-001", "agent:a"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, err := os.Stat(s.taskPath("PROJ-001")); !os.IsNotExist(err) {
		t.Fatalf("file still present after delete")
	}
	if err := s.DeleteTask("PROJ-001", "agent:a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing = %v, want ErrNotFound", err)
	}
}

func TestDeleteTaskBlockedByChild(t *testing.T) {
	s := New(repo(t, map[string]string{
		"PROJ-001": minimalTask,
		"PROJ-003": "---\nid: PROJ-003\ntitle: child\nstatus: backlog\nparent: PROJ-001\n---\n",
	}))
	if err := s.DeleteTask("PROJ-001", "agent:a"); !errors.Is(err, task.ErrHasChildren) {
		t.Fatalf("delete = %v, want ErrHasChildren", err)
	}
	if _, err := os.Stat(s.taskPath("PROJ-001")); err != nil {
		t.Fatalf("blocked delete should leave file intact: %v", err)
	}
}

func TestDeleteTaskBlockedByDependent(t *testing.T) {
	s := New(repo(t, map[string]string{
		"PROJ-001": minimalTask,
		"PROJ-004": "---\nid: PROJ-004\ntitle: dependent\nstatus: backlog\ndeps: [PROJ-001]\n---\n",
	}))
	if err := s.DeleteTask("PROJ-001", "agent:a"); !errors.Is(err, task.ErrHasDependents) {
		t.Fatalf("delete = %v, want ErrHasDependents", err)
	}
}

func TestAppendProvenanceStampsNoteID(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	at := time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC)
	_ = d.AppendProvenance("agent:a", "note", "a note", at)
	_ = d.AppendProvenance("agent:a", "claimed", "", at)
	noteEntry := d.Provenance[len(d.Provenance)-2]
	sysEntry := d.Provenance[len(d.Provenance)-1]
	if noteEntry.ID == "" || !strings.HasPrefix(noteEntry.ID, "n_") {
		t.Fatalf("note entry should get an id: %+v", noteEntry)
	}
	if sysEntry.ID != "" {
		t.Fatalf("system entry should not get an id: %+v", sysEntry)
	}
}

func TestEditNoteByID(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	at := time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC)
	_ = d.AppendProvenance("agent:a", "note", "original", at)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := s.Get("PROJ-002")
	noteID := reloaded.Provenance[len(reloaded.Provenance)-1].ID

	editAt := at.Add(time.Hour)
	if err := reloaded.EditNote(noteID, -1, "edited text", editAt); err != nil {
		t.Fatalf("EditNote: %v", err)
	}
	if err := s.Save(reloaded); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get("PROJ-002")
	last := final.Provenance[len(final.Provenance)-1]
	if last.Text != "edited text" || last.EditedAt == "" {
		t.Fatalf("note not edited in place: %+v", last)
	}
}

func TestEditNoteByIndexLegacy(t *testing.T) {
	// fullTask has one id-less created entry at index 0; add an id-less legacy note.
	legacy := strings.Replace(fullTask,
		"  - {who: human:shah, at: 2026-06-21T10:00:00Z, did: created}",
		"  - {who: human:shah, at: 2026-06-21T10:00:00Z, did: created}\n  - {who: human:shah, at: 2026-06-21T10:05:00Z, did: note, text: legacy}",
		1)
	s := New(repo(t, map[string]string{"PROJ-002": legacy}))
	d, _ := s.Get("PROJ-002")
	editAt := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if err := d.EditNote("", 1, "fixed legacy", editAt); err != nil {
		t.Fatalf("EditNote by index: %v", err)
	}
	if d.Provenance[1].Text != "fixed legacy" {
		t.Fatalf("legacy note not edited: %+v", d.Provenance[1])
	}
}

func TestEditNoteRefusesSystemEntry(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	// index 0 is the `created` system entry.
	if err := d.EditNote("", 0, "nope", time.Now()); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("edit system entry = %v, want ErrNotEditable", err)
	}
}

func TestDeleteNoteSpliceKeepsAlignment(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	at := time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC)
	_ = d.AppendProvenance("agent:a", "note", "first", at)
	_ = d.AppendProvenance("agent:a", "note", "second", at.Add(time.Minute))
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := s.Get("PROJ-002")
	firstID := reloaded.Provenance[1].ID

	if err := reloaded.DeleteNote(firstID, -1); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if err := s.Save(reloaded); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get("PROJ-002")
	if len(final.Provenance) != 2 {
		t.Fatalf("want 2 entries after delete, got %d", len(final.Provenance))
	}
	if final.Provenance[1].Text != "second" {
		t.Fatalf("wrong note remained: %+v", final.Provenance[1])
	}
}

func TestDeleteNoteRefusesSystemEntry(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	if err := d.DeleteNote("", 0); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("delete system entry = %v, want ErrNotEditable", err)
	}
}

func TestNoteIDBackCompat(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-002": fullTask}))
	d, _ := s.Get("PROJ-002")
	if d.Provenance[0].ID != "" {
		t.Fatalf("legacy entry should parse with empty id: %+v", d.Provenance[0])
	}
}
