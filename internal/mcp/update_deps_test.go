package mcp

import (
	"errors"
	"testing"

	"carbon/internal/store"
	"carbon/internal/task"
)

func TestUpdateDepsReplacesAndValidatesWholeGraph(t *testing.T) {
	t.Run("sets and clears while retaining parent", func(t *testing.T) {
		svc := service(t, "agent:a")
		parent, err := svc.Create(store.Draft{Title: "parent"})
		if err != nil {
			t.Fatal(err)
		}
		dep, err := svc.Create(store.Draft{Title: "dependency"})
		if err != nil {
			t.Fatal(err)
		}
		target, err := svc.Create(store.Draft{Title: "target"})
		if err != nil {
			t.Fatal(err)
		}

		deps := []string{dep.Task.ID}
		parentID := parent.Task.ID
		updated, err := svc.UpdateWithVersion(target.Task.ID, UpdateFields{Deps: &deps, Parent: &parentID}, target.ETag())
		if err != nil {
			t.Fatalf("set deps: %v", err)
		}
		if len(updated.Task.Deps) != 1 || updated.Task.Deps[0] != dep.Task.ID || updated.Task.Parent != parent.Task.ID {
			t.Fatalf("set deps/parent = %+v", updated.Task)
		}

		empty := []string{}
		cleared, err := svc.UpdateWithVersion(target.Task.ID, UpdateFields{Deps: &empty}, updated.ETag())
		if err != nil {
			t.Fatalf("clear deps: %v", err)
		}
		if len(cleared.Task.Deps) != 0 || cleared.Task.Parent != parent.Task.ID {
			t.Fatalf("clear deps = %+v", cleared.Task)
		}
		reloaded, err := svc.Get(target.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Task.Deps) != 0 {
			t.Fatalf("cleared deps persisted as %+v", reloaded.Task.Deps)
		}
	})

	t.Run("rejects dangling dependency without mutation", func(t *testing.T) {
		svc := service(t, "agent:a")
		target, err := svc.Create(store.Draft{Title: "target"})
		if err != nil {
			t.Fatal(err)
		}
		deps := []string{"PROJ-missing"}
		if _, err := svc.UpdateWithVersion(target.Task.ID, UpdateFields{Deps: &deps}, target.ETag()); !errors.Is(err, task.ErrDanglingDep) {
			t.Fatalf("dangling deps = %v, want ErrDanglingDep", err)
		}
		reloaded, err := svc.Get(target.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Task.Deps) != 0 {
			t.Fatalf("dangling update mutated deps to %+v", reloaded.Task.Deps)
		}
	})

	t.Run("rejects self and indirect cycles", func(t *testing.T) {
		svc := service(t, "agent:a")
		first, err := svc.Create(store.Draft{Title: "first"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.Create(store.Draft{Title: "second"})
		if err != nil {
			t.Fatal(err)
		}

		self := []string{first.Task.ID}
		if _, err := svc.UpdateWithVersion(first.Task.ID, UpdateFields{Deps: &self}, first.ETag()); !errors.Is(err, task.ErrCycle) {
			t.Fatalf("self dependency = %v, want ErrCycle", err)
		}

		firstDeps := []string{second.Task.ID}
		first, err = svc.UpdateWithVersion(first.Task.ID, UpdateFields{Deps: &firstDeps}, first.ETag())
		if err != nil {
			t.Fatalf("first -> second: %v", err)
		}
		secondDeps := []string{first.Task.ID}
		if _, err := svc.UpdateWithVersion(second.Task.ID, UpdateFields{Deps: &secondDeps}, second.ETag()); !errors.Is(err, task.ErrCycle) {
			t.Fatalf("second -> first = %v, want ErrCycle", err)
		}
		reloaded, err := svc.Get(second.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Task.Deps) != 0 {
			t.Fatalf("cycle update mutated deps to %+v", reloaded.Task.Deps)
		}
	})

	t.Run("rejects stale ETag", func(t *testing.T) {
		svc := service(t, "agent:a")
		dep, err := svc.Create(store.Draft{Title: "dependency"})
		if err != nil {
			t.Fatal(err)
		}
		target, err := svc.Create(store.Draft{Title: "target"})
		if err != nil {
			t.Fatal(err)
		}
		stale := target.ETag()
		if _, err := svc.Update(target.Task.ID, UpdateFields{Title: ptr("changed")}); err != nil {
			t.Fatalf("advance version: %v", err)
		}
		deps := []string{dep.Task.ID}
		if _, err := svc.UpdateWithVersion(target.Task.ID, UpdateFields{Deps: &deps}, stale); !errors.Is(err, store.ErrVersionMismatch) {
			t.Fatalf("stale ETag = %v, want ErrVersionMismatch", err)
		}
		reloaded, err := svc.Get(target.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Task.Deps) != 0 {
			t.Fatalf("stale update mutated deps to %+v", reloaded.Task.Deps)
		}
	})
}
