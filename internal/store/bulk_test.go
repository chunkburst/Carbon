package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestBulkMoveRequiresExplicitTargetScope(t *testing.T) {
	st := New(t.TempDir())

	if _, err := st.BulkMove(context.Background(), "human:test", BulkMove{IDs: []string{"CAR-001"}}); !errors.Is(err, ErrBulkMoveClusterWideRequired) {
		t.Fatalf("empty project without cluster_wide = %v, want ErrBulkMoveClusterWideRequired", err)
	}
	if _, err := st.BulkMove(context.Background(), "human:test", BulkMove{IDs: []string{"CAR-001"}, ProjectID: "project-one", ClusterWide: true}); !errors.Is(err, ErrBulkMoveProjectConflict) {
		t.Fatalf("concrete project with cluster_wide = %v, want ErrBulkMoveProjectConflict", err)
	}
}

func TestSaveManyReportsIncompleteRollbackInsteadOfClaimingAtomicity(t *testing.T) {
	taskTwo := strings.Replace(minimalTask, "PROJ-001", "PROJ-002", 1)
	s := New(repo(t, map[string]string{"PROJ-001": minimalTask, "PROJ-002": taskTwo}))
	first, err := s.Get("PROJ-001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Get("PROJ-002")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SetTitle("first write reached disk"); err != nil {
		t.Fatal(err)
	}
	if err := second.SetTitle("second write must fail"); err != nil {
		t.Fatal(err)
	}

	writeFailure := errors.New("simulated second write failure")
	rollbackFailure := errors.New("simulated rollback failure")
	calls := 0
	s.atomicWriteFn = func(path string, data []byte) error {
		calls++
		switch calls {
		case 1:
			return atomicWrite(path, data)
		case 2:
			return writeFailure
		default:
			return rollbackFailure
		}
	}
	err = s.Write(context.Background(), "agent:test", "injected bulk failure", func(tx *WriteTx) error {
		return tx.saveMany([]*Doc{first, second})
	})
	if !errors.Is(err, writeFailure) || !errors.Is(err, rollbackFailure) || !errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("saveMany failure = %v, want primary + rollback errors", err)
	}
	raw, readErr := os.ReadFile(s.taskPath("PROJ-001"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "first write reached disk") {
		t.Fatalf("test did not create the intended partial durable state:\n%s", raw)
	}
}
