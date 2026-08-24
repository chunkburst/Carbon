package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"carbon/internal/repo"
	"carbon/internal/store"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	if err := repo.InitDataRoot(root, "ID"); err != nil {
		t.Fatal(err)
	}
	return New(store.New(root), func() time.Time {
		return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	})
}

func TestClaimOrChangePersistsAndRequiresReasonForMaterialChange(t *testing.T) {
	manager := testManager(t)
	first, err := manager.ClaimOrChange(context.Background(), "agent:architect", ClaimInput{
		Actor: "agent:architect", Role: "架构师", Types: []string{"foundation", "patch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ClaimedAt == "" || first.UpdatedAt != first.ClaimedAt || first.ChangedBy != "agent:architect" || first.Reason != "" {
		t.Fatalf("first identity = %#v", first)
	}
	if _, err := manager.ClaimOrChange(context.Background(), "agent:architect", ClaimInput{
		Actor: "agent:architect", Role: "任务发布者", Types: []string{"extension"},
	}); !errors.Is(err, ErrChangeReasonRequired) {
		t.Fatalf("change without reason = %v, want ErrChangeReasonRequired", err)
	}
	changed, err := manager.ClaimOrChange(context.Background(), "human:lead", ClaimInput{
		Actor: "agent:architect", Role: "任务发布者", Types: []string{"extension", "patch"}, Reason: "调整负责范围",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ClaimedAt != first.ClaimedAt || changed.Role != "任务发布者" || changed.ChangedBy != "human:lead" || changed.Reason != "调整负责范围" {
		t.Fatalf("changed identity = %#v", changed)
	}
	got, err := manager.Get("agent:architect")
	if err != nil || got.Role != changed.Role || len(got.Types) != 2 {
		t.Fatalf("persisted identity = %#v err=%v", got, err)
	}
	// An exact retry is genuinely idempotent and cannot erase its prior change reason.
	retry, err := manager.ClaimOrChange(context.Background(), "agent:architect", ClaimInput{
		Actor: "agent:architect", Role: "任务发布者", Types: []string{"extension", "patch"},
	})
	if err != nil || retry.UpdatedAt != changed.UpdatedAt || retry.Reason != changed.Reason {
		t.Fatalf("idempotent retry = %#v err=%v", retry, err)
	}
}

func TestClaimOrChangeRejectsMalformedActorsAndDuplicateTypes(t *testing.T) {
	manager := testManager(t)
	for name, input := range map[string]ClaimInput{
		"actor":     {Actor: "agent bad", Role: "架构师", Types: []string{"patch"}},
		"role":      {Actor: "agent:one", Role: " ", Types: []string{"patch"}},
		"duplicate": {Actor: "agent:one", Role: "架构师", Types: []string{"patch", "patch"}},
		"type":      {Actor: "agent:one", Role: "架构师", Types: []string{"Bad Type"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.ClaimOrChange(context.Background(), "agent:one", input); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("ClaimOrChange = %v, want invalid identity", err)
			}
		})
	}
}

func TestClaimOrChangeSerializesConcurrentWrites(t *testing.T) {
	manager := testManager(t)
	const workers = 16
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			role := "前端"
			types := []string{"extension"}
			if index%2 == 1 {
				role = "后端"
				types = []string{"patch"}
			}
			_, err := manager.ClaimOrChange(context.Background(), "agent:worker", ClaimInput{
				Actor: "agent:worker", Role: role, Types: types, Reason: "并发认领更新",
			})
			errs <- err
		}(i)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	records, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Actor != "agent:worker" || (records[0].Role != "前端" && records[0].Role != "后端") {
		t.Fatalf("concurrent registry = %#v", records)
	}
}
