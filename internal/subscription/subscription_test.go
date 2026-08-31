package subscription

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"carbon/internal/store"
)

const (
	testProject = "project-subscription"
	testActor   = "agent:alice"
	testSubID   = "sub-main"
)

func newTestManager(t *testing.T) (*store.Store, *Manager) {
	t.Helper()
	data := store.New(t.TempDir())
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return data, New(data, func() time.Time { return now })
}

func initializeTestSubscription(t *testing.T, manager *Manager, filters TaskFilter) InitializeResult {
	t.Helper()
	out, err := manager.Initialize(context.Background(), testProject, testActor, InitializeInput{
		SubscriptionID: testSubID,
		IdempotencyKey: "initialize-1",
		Mode:           ModeMixed,
		Modules:        []Module{ModuleTasks},
		Tasks:          filters,
	})
	if err != nil {
		t.Fatalf("initialize subscription: %v", err)
	}
	return out
}

func testInput(kind string) EventInput {
	return EventInput{
		ProjectID: testProject, OccurredAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Module: ModuleTasks, Kind: kind, ResourceID: "task-one", Actor: testActor,
		Status: "todo", Type: "patch", Importance: "important",
	}
}

func testSource() SourceRef {
	return SourceRef{Kind: SourceTaskProvenance, ResourceID: "task-one", MutationID: "mutation-one"}
}

func prepareOnly(t *testing.T, data *store.Store, manager *Manager, input EventInput) PreparedEvent {
	t.Helper()
	var prepared PreparedEvent
	err := data.Write(context.Background(), testActor, "prepare test event", func(tx *store.WriteTx) error {
		var err error
		prepared, err = manager.PrepareTx(tx, input, testSource())
		return err
	})
	if err != nil {
		t.Fatalf("prepare event: %v", err)
	}
	return prepared
}

func TestPrepareWithoutInterestedSubscriptionDoesNotCreateLedgerOrMarker(t *testing.T) {
	data, manager := newTestManager(t)
	prepared := prepareOnly(t, data, manager, testInput("created"))
	if prepared.Event.ID != "" {
		t.Fatalf("prepared event = %#v, want no-op without subscriptions", prepared)
	}
	for _, dir := range []string{pendingDir, ledgerDir} {
		names, err := data.ListData(dir)
		if err != nil {
			t.Fatalf("list %s: %v", dir, err)
		}
		if len(names) != 0 {
			t.Fatalf("%s files = %v, want none without recipients", dir, names)
		}
	}
}

func TestRecoveryPublishesOnceAndDropsSourceLessMarker(t *testing.T) {
	t.Run("source persisted ledger missing", func(t *testing.T) {
		data, manager := newTestManager(t)
		initializeTestSubscription(t, manager, TaskFilter{})
		prepared := prepareOnly(t, data, manager, testInput("created"))
		if prepared.Event.ID == "" {
			t.Fatal("expected prepared marker")
		}
		if err := manager.Recover(context.Background(), testProject, testActor, func(source SourceRef) (bool, error) {
			return source == prepared.Source, nil
		}); err != nil {
			t.Fatalf("recover source-persisted marker: %v", err)
		}
		out, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 10})
		if err != nil {
			t.Fatalf("poll recovered event: %v", err)
		}
		if len(out.Events) != 1 || out.Events[0].ID != prepared.Event.ID {
			t.Fatalf("recovered events = %#v, want exactly %q", out.Events, prepared.Event.ID)
		}
		if err := manager.Recover(context.Background(), testProject, testActor, func(SourceRef) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("repeat recovery: %v", err)
		}
		out, err = manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Cursor: out.Cursor, Limit: 10})
		if err != nil {
			t.Fatalf("poll after repeat recovery: %v", err)
		}
		if len(out.Events) != 0 {
			t.Fatalf("repeat recovery redelivered %#v", out.Events)
		}
	})

	t.Run("ledger persisted pending cleanup interrupted", func(t *testing.T) {
		data, manager := newTestManager(t)
		initializeTestSubscription(t, manager, TaskFilter{})
		prepared := prepareOnly(t, data, manager, testInput("updated"))
		err := data.Write(context.Background(), testActor, "persist ledger but retain marker", func(tx *store.WriteTx) error {
			ledger, err := readLedgerTx(tx, testProject)
			if err != nil {
				return err
			}
			subscriptions, err := readSubscriptionsTx(tx, testProject)
			if err != nil {
				return err
			}
			if _, changed, err := appendPreparedToLedger(&ledger, subscriptions.Subscriptions, prepared); err != nil {
				return err
			} else if !changed {
				return errors.New("expected initial ledger append")
			}
			return writeLedgerTx(tx, ledger)
		})
		if err != nil {
			t.Fatalf("simulate cleanup interruption: %v", err)
		}
		if err := manager.Recover(context.Background(), testProject, testActor, func(SourceRef) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("recover committed marker: %v", err)
		}
		out, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 10})
		if err != nil {
			t.Fatalf("poll committed marker: %v", err)
		}
		if len(out.Events) != 1 || out.Events[0].ID != prepared.Event.ID {
			t.Fatalf("events after cleanup recovery = %#v", out.Events)
		}
		names, err := data.ListData(pendingDir)
		if err != nil {
			t.Fatalf("list pending markers: %v", err)
		}
		if len(names) != 0 {
			t.Fatalf("pending markers remain after recovery: %v", names)
		}
	})

	t.Run("source absent is never exposed", func(t *testing.T) {
		data, manager := newTestManager(t)
		initializeTestSubscription(t, manager, TaskFilter{})
		prepareOnly(t, data, manager, testInput("updated"))
		if err := manager.Recover(context.Background(), testProject, testActor, func(SourceRef) (bool, error) { return false, nil }); err != nil {
			t.Fatalf("recover absent source: %v", err)
		}
		out, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 10})
		if err != nil {
			t.Fatalf("poll absent source: %v", err)
		}
		if len(out.Events) != 0 {
			t.Fatalf("source-less event exposed: %#v", out.Events)
		}
	})
}

func TestLedgerCompactsConsumedPrefixAndExpiresSlowCursorWithoutBlocking(t *testing.T) {
	data, manager := newTestManager(t)
	initializeTestSubscription(t, manager, TaskFilter{})
	ledger := ledgerState{Version: stateVersion, ProjectID: testProject, NextSeq: maxLedgerEvents}
	ledger.Events = make([]Event, 0, maxLedgerEvents)
	for seq := 1; seq <= maxLedgerEvents; seq++ {
		ledger.Events = append(ledger.Events, Event{
			ProjectID: testProject, Seq: uint64(seq), ID: fmt.Sprintf("evt_%024x", seq),
			OccurredAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			Module:     ModuleTasks, Kind: "updated", ResourceID: "task-one", Actor: testActor,
			Status: "todo", Type: "patch", Importance: "important",
		})
	}
	prepared := PreparedEvent{Event: Event{
		ProjectID: testProject, ID: fmt.Sprintf("evt_%024x", maxLedgerEvents+1),
		OccurredAt: time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Module:     ModuleTasks, Kind: "updated", ResourceID: "task-one", Actor: testActor,
		Status: "todo", Type: "patch", Importance: "important",
	}, Source: testSource()}
	err := data.Write(context.Background(), testActor, "fill bounded ledger", func(tx *store.WriteTx) error {
		subscriptions, err := readSubscriptionsTx(tx, testProject)
		if err != nil {
			return err
		}
		if _, changed, err := appendPreparedToLedger(&ledger, subscriptions.Subscriptions, prepared); err != nil {
			return err
		} else if !changed {
			return errors.New("expected append after capacity")
		}
		return writeLedgerTx(tx, ledger)
	})
	if err != nil {
		t.Fatalf("append after full ledger: %v", err)
	}
	if ledger.BaseSeq != 1 || len(ledger.Events) != maxLedgerEvents || ledger.NextSeq != maxLedgerEvents+1 {
		t.Fatalf("compacted ledger = base=%d len=%d next=%d", ledger.BaseSeq, len(ledger.Events), ledger.NextSeq)
	}
	_, err = manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 1})
	if !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("slow cursor error = %v, want ErrCursorExpired", err)
	}
}

func TestLedgerCompactsFullyConsumedPrefixBeforeExpiringAnyone(t *testing.T) {
	state := ledgerState{Version: stateVersion, ProjectID: testProject, NextSeq: maxLedgerEvents}
	state.Events = make([]Event, 0, maxLedgerEvents)
	for seq := 1; seq <= maxLedgerEvents; seq++ {
		state.Events = append(state.Events, Event{
			ProjectID: testProject, Seq: uint64(seq), ID: fmt.Sprintf("evt_%024x", seq),
			OccurredAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			Module:     ModuleTasks, Kind: "updated", ResourceID: "task-one", Actor: testActor,
			Status: "todo", Type: "patch", Importance: "important",
		})
	}
	prepared := PreparedEvent{Event: Event{
		ProjectID: testProject, ID: fmt.Sprintf("evt_%024x", maxLedgerEvents+1),
		OccurredAt: time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Module:     ModuleTasks, Kind: "updated", ResourceID: "task-one", Actor: testActor,
		Status: "todo", Type: "patch", Importance: "important",
	}, Source: testSource()}
	consumed := Subscription{Actor: testActor, ID: testSubID, CursorHighWater: uint64(maxLedgerEvents)}
	if _, changed, err := appendPreparedToLedger(&state, []Subscription{consumed}, prepared); err != nil || !changed {
		t.Fatalf("append after fully consumed prefix: changed=%v err=%v", changed, err)
	}
	if state.BaseSeq != maxLedgerEvents || len(state.Events) != 1 || state.Events[0].Seq != maxLedgerEvents+1 {
		t.Fatalf("safe prefix compaction = base=%d events=%#v", state.BaseSeq, state.Events)
	}
}

func TestPollWaitIsBoundedAndContextCancellable(t *testing.T) {
	_, manager := newTestManager(t)
	initializeTestSubscription(t, manager, TaskFilter{})
	if _, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Wait: maxCursorWait + time.Millisecond}); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("overlong poll wait = %v, want invalid subscription", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Poll(ctx, testProject, testActor, PollInput{SubscriptionID: testSubID, Wait: time.Second}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled poll = %v, want context canceled", err)
	}
}

func TestPollOnlyAdvancesAfterClientReturnsCursor(t *testing.T) {
	data, manager := newTestManager(t)
	initializeTestSubscription(t, manager, TaskFilter{})
	prepared := prepareOnly(t, data, manager, testInput("created"))
	if err := data.Write(context.Background(), testActor, "commit test event", func(tx *store.WriteTx) error {
		_, err := manager.CommitTx(tx, prepared)
		return err
	}); err != nil {
		t.Fatalf("commit event: %v", err)
	}
	first, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 10})
	if err != nil || len(first.Events) != 1 {
		t.Fatalf("first poll = %#v err=%v", first, err)
	}
	// A caller that has not returned the cursor has not acknowledged delivery;
	// retrial is intentionally an exactly-same-event redelivery, not a gap.
	retried, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Limit: 10})
	if err != nil || len(retried.Events) != 1 || retried.Events[0].ID != first.Events[0].ID {
		t.Fatalf("unacknowledged retry = %#v err=%v", retried, err)
	}
	acknowledged, err := manager.Poll(context.Background(), testProject, testActor, PollInput{SubscriptionID: testSubID, Cursor: first.Cursor, Limit: 10})
	if err != nil || len(acknowledged.Events) != 0 {
		t.Fatalf("acknowledged cursor = %#v err=%v", acknowledged, err)
	}
}
