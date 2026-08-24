/// <reference types="node" />

import assert from "node:assert/strict";
import test from "node:test";
import type { Task } from "./api.ts";
import { moveTaskAcrossStatuses, moveTaskForDropPreview, rankBetween, rankForTaskDrop } from "./task-order.ts";

function task(id: string, status: string, rank: number): Task {
  return { id, status, rank } as Task;
}

test("never emits rank zero when inserting before the first ranked task", () => {
  assert.equal(rankBetween(undefined, 1), 0.5);
  assert.notEqual(rankBetween(undefined, 1), 0);
  assert.equal(rankBetween(undefined, undefined), 1024);
});

test("uses hidden tasks as filtered drop boundaries", () => {
  const active = task("TASK-active", "todo", 10);
  const hiddenBefore = task("TASK-hidden-before", "done", 90);
  const visible = task("TASK-visible", "done", 100);
  const hiddenAfter = task("TASK-hidden-after", "done", 110);

  assert.equal(
    rankForTaskDrop(active.id, "done", [active.id, visible.id], [active, hiddenBefore, visible, hiddenAfter]),
    95,
  );
  assert.equal(
    rankForTaskDrop(active.id, "done", [visible.id, active.id], [active, hiddenBefore, visible, hiddenAfter]),
    105,
  );
});

test("uses the first complete-list gap between visible filtered neighbors", () => {
  const active = task("TASK-active", "done", 10);
  const previous = task("TASK-previous", "done", 100);
  const hidden = task("TASK-hidden", "done", 150);
  const next = task("TASK-next", "done", 200);

  assert.equal(
    rankForTaskDrop(active.id, "done", [previous.id, active.id, next.id], [active, previous, hidden, next]),
    125,
  );
});

test("appends after hidden tasks in an empty-looking filtered state", () => {
  const active = task("TASK-active", "todo", 10);
  const hidden = task("TASK-hidden", "done", 200);
  assert.equal(rankForTaskDrop(active.id, "done", [active.id], [active, hidden]), 201);
});

test("keeps cross-status before and after placement stable through pointer release", () => {
  const columns = { todo: ["X"], done: ["Y", "Z"] };
  assert.deepEqual(
    moveTaskAcrossStatuses(columns, { activeId: "X", from: "todo", to: "done", overId: "Y", after: false }),
    { todo: [], done: ["X", "Y", "Z"] },
  );
  assert.deepEqual(
    moveTaskAcrossStatuses(columns, { activeId: "X", from: "todo", to: "done", overId: "Y", after: true }),
    { todo: [], done: ["Y", "X", "Z"] },
  );
});

test("keeps following the final hover target after entering another status", () => {
  const entered = moveTaskForDropPreview(
    { todo: ["X"], done: ["Y", "Z"] },
    { activeId: "X", from: "todo", to: "done", overId: "Y", after: true, hasCrossedStatus: true },
  );
  assert.deepEqual(entered, { todo: [], done: ["Y", "X", "Z"] });

  const finalPreview = moveTaskForDropPreview(entered, {
    activeId: "X",
    from: "done",
    to: "done",
    overId: "Z",
    after: true,
    hasCrossedStatus: true,
  });
  assert.deepEqual(finalPreview, { todo: [], done: ["Y", "Z", "X"] });
});

test("keeps the final hover side after crossing out and returning to the original status", () => {
  const crossed = moveTaskForDropPreview(
    { todo: ["X", "W", "Y"], done: ["Z"] },
    { activeId: "X", from: "todo", to: "done", overId: "Z", after: true, hasCrossedStatus: true },
  );
  const returned = moveTaskForDropPreview(crossed, {
    activeId: "X",
    from: "done",
    to: "todo",
    overId: "W",
    after: true,
    hasCrossedStatus: true,
  });
  assert.deepEqual(returned, { todo: ["W", "X", "Y"], done: ["Z"] });

  const finalPreview = moveTaskForDropPreview(returned, {
    activeId: "X",
    from: "todo",
    to: "todo",
    overId: "Y",
    after: true,
    hasCrossedStatus: true,
  });
  assert.deepEqual(finalPreview, { todo: ["W", "Y", "X"], done: ["Z"] });
});
