package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon/internal/session"
)

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}
}

func writeSessionDocument(t *testing.T, path string, value session.Session) {
	t.Helper()
	doc, err := newSessionDoc(value)
	if err != nil {
		t.Fatal(err)
	}
	b, err := renderSession(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTaskPathsRejectEscapingDirectorySymlink(t *testing.T) {
	s := New(repo(t, map[string]string{}))
	external := t.TempDir()
	externalTask := filepath.Join(external, "PROJ-001.md")
	if err := os.WriteFile(externalTask, []byte(minimalTask), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.tasksDir()); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, s.tasksDir())

	if _, err := s.Get("PROJ-001"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Get through external tasks symlink = %v, want ErrPathOutsideRoot", err)
	}
	if _, err := s.ListDocs(); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("ListDocs through external tasks symlink = %v, want ErrPathOutsideRoot", err)
	}
	if _, err := s.Create(Draft{Title: "must not escape"}, "agent:test", time.Now()); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Create through external tasks symlink = %v, want ErrPathOutsideRoot", err)
	}
	if err := s.DeleteTask("PROJ-001", "agent:test"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("DeleteTask through external tasks symlink = %v, want ErrPathOutsideRoot", err)
	}
	if _, err := os.Stat(externalTask); err != nil {
		t.Fatalf("external task changed by rejected operations: %v", err)
	}
}

func TestStoreRejectsEscapingCairnParentSymlink(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask}))
	external := t.TempDir()
	if err := os.MkdirAll(filepath.Join(external, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "tasks", "PROJ-001.md"), []byte(minimalTask), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.Root(), ".carbon")); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, filepath.Join(s.Root(), ".carbon"))

	if _, err := s.Get("PROJ-001"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Get through external .carbon symlink = %v, want ErrPathOutsideRoot", err)
	}
	if err := s.Write(context.Background(), "agent:test", "write", func(*WriteTx) error { return nil }); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Write through external .carbon symlink = %v, want ErrPathOutsideRoot", err)
	}
}

func TestTaskFileSymlinkCannotEscapeReadOrDelete(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask}))
	path := s.taskPath("PROJ-001")
	external := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(external, []byte(minimalTask), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, path)

	if _, err := s.Get("PROJ-001"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Get through external task symlink = %v, want ErrPathOutsideRoot", err)
	}
	if err := s.DeleteTask("PROJ-001", "agent:test"); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("DeleteTask through external task symlink = %v, want ErrPathOutsideRoot", err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external task removed by rejected delete: %v", err)
	}
}

func TestSessionPathsRejectEscapingSymlinksAndMismatchedIDs(t *testing.T) {
	t.Run("sessions directory", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		value := storedSession()
		external := t.TempDir()
		writeSessionDocument(t, filepath.Join(external, value.ID+".yaml"), value)
		symlinkOrSkip(t, external, s.sessionsDir())

		if _, err := s.GetSession(value.ID); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("GetSession through external sessions symlink = %v, want ErrPathOutsideRoot", err)
		}
		if _, err := s.ListSessions(); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("ListSessions through external sessions symlink = %v, want ErrPathOutsideRoot", err)
		}
	})

	t.Run("session file", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		value := storedSession()
		if err := os.MkdirAll(s.sessionsDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "outside.yaml")
		writeSessionDocument(t, external, value)
		symlinkOrSkip(t, external, s.sessionPath(value.ID))

		if _, err := s.GetSession(value.ID); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("GetSession through external session symlink = %v, want ErrPathOutsideRoot", err)
		}
	})

	t.Run("filename and document id must agree", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		if err := os.MkdirAll(s.sessionsDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		value := storedSession()
		value.ID = "ses_002"
		writeSessionDocument(t, s.sessionPath("ses_001"), value)

		if _, err := s.GetSession("ses_001"); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("GetSession mismatched document id = %v, want ErrInvalidSessionID", err)
		}
		if _, err := s.ListSessions(); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("ListSessions mismatched document id = %v, want ErrInvalidSessionID", err)
		}
	})

	t.Run("filename and document id must be valid", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		if err := os.MkdirAll(s.sessionsDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		value := storedSession()
		value.ID = "not/a-session-id"
		writeSessionDocument(t, s.sessionPath("ses_001"), value)
		if _, err := s.ListSessions(); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("ListSessions invalid document id = %v, want ErrInvalidSessionID", err)
		}
	})
}

func TestLivePathsRejectEscapingSymlinksAndMismatchedIDs(t *testing.T) {
	t.Run("live file", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		if err := os.MkdirAll(s.liveDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "outside.json")
		b, err := json.Marshal(session.Live{SessionID: "ses_001", Worktree: s.Root()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(external, b, 0o644); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, external, s.livePath("ses_001"))

		if _, err := s.ReadLive("ses_001"); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("ReadLive through external live symlink = %v, want ErrPathOutsideRoot", err)
		}
		if err := s.Write(context.Background(), "agent:test", "delete live", func(tx *WriteTx) error {
			return tx.DeleteLive("ses_001")
		}); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("DeleteLive through external live symlink = %v, want ErrPathOutsideRoot", err)
		}
		if _, err := os.Stat(external); err != nil {
			t.Fatalf("external live state removed by rejected delete: %v", err)
		}
	})

	t.Run("live directory write", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		external := t.TempDir()
		symlinkOrSkip(t, external, s.liveDir())

		err := s.Write(context.Background(), "agent:test", "write live", func(tx *WriteTx) error {
			return tx.WriteLive(session.Live{SessionID: "ses_001", Worktree: s.Root()})
		})
		if !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("WriteLive through external live symlink = %v, want ErrPathOutsideRoot", err)
		}
		entries, err := os.ReadDir(external)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("WriteLive wrote outside repository: %+v", entries)
		}
	})

	t.Run("filename and document id must agree", func(t *testing.T) {
		s := New(repo(t, map[string]string{}))
		if err := os.MkdirAll(s.liveDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(session.Live{SessionID: "ses_002", Worktree: s.Root()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.livePath("ses_001"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadLive("ses_001"); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("ReadLive mismatched document id = %v, want ErrInvalidSessionID", err)
		}
	})
}

func TestWriteLockRejectsEscapingSymlink(t *testing.T) {
	s := New(repo(t, map[string]string{}))
	external := filepath.Join(t.TempDir(), "outside.lock")
	const original = "do not overwrite\n"
	if err := os.WriteFile(external, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, s.lockPath())

	err := s.Write(context.Background(), "agent:test", "write", func(*WriteTx) error { return nil })
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("Write through external lock symlink = %v, want ErrPathOutsideRoot", err)
	}
	b, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != original {
		t.Fatalf("external lock changed: %q", b)
	}
}

func TestTrashRollbackReportsEveryCompensationFailure(t *testing.T) {
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask}))
	source, err := s.taskFilePath("PROJ-001", false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := s.trashFilePath("PROJ-001", true, false, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	moveFailure := errors.New("simulated rollback move failure")
	restoreFailure := errors.New("simulated rollback restore failure")
	s.renameFn = func(string, string) error { return moveFailure }
	s.atomicWriteFn = func(string, []byte) error { return restoreFailure }
	err = s.rollbackTrashMove([]trashMoveBackup{{source: source, dest: dest, data: raw}}, 1, 1)
	if !errors.Is(err, ErrRollbackIncomplete) || !errors.Is(err, moveFailure) || !errors.Is(err, restoreFailure) {
		t.Fatalf("trash rollback error = %v, want joined compensation failures", err)
	}
}
