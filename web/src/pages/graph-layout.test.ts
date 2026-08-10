/// <reference types="node" />

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  DEPENDENCY_LAYOUT_WORKER_THRESHOLD,
  GRAPH_NODE_HEIGHT,
  GRAPH_NODE_WIDTH,
  layoutDependencyGraph,
  partitionDependencyTasks,
  shouldUseDependencyLayoutWorker,
  type DependencyGraphLayout,
} from "./graph-layout.ts";

type Task = { id: string; deps?: string[] };

function isolated(count: number): Task[] {
  return Array.from({ length: count }, (_, index) => ({ id: `TASK-${String(index).padStart(3, "0")}` }));
}

function chain(count: number): Task[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `CHAIN-${String(index).padStart(4, "0")}`,
    ...(index > 0 ? { deps: [`CHAIN-${String(index - 1).padStart(4, "0")}`] } : {}),
  }));
}

function bounds(layout: DependencyGraphLayout) {
  const positions = [...layout.positions.values()];
  const minX = Math.min(...positions.map(({ x }) => x));
  const minY = Math.min(...positions.map(({ y }) => y));
  const maxX = Math.max(...positions.map(({ x }) => x + GRAPH_NODE_WIDTH));
  const maxY = Math.max(...positions.map(({ y }) => y + GRAPH_NODE_HEIGHT));
  return { width: maxX - minX, height: maxY - minY };
}

function assertNoOverlaps(layout: DependencyGraphLayout): void {
  const positions = [...layout.positions.entries()];
  for (let left = 0; left < positions.length; left += 1) {
    for (let right = left + 1; right < positions.length; right += 1) {
      const [, a] = positions[left];
      const [, b] = positions[right];
      const overlaps = a.x < b.x + GRAPH_NODE_WIDTH
        && a.x + GRAPH_NODE_WIDTH > b.x
        && a.y < b.y + GRAPH_NODE_HEIGHT
        && a.y + GRAPH_NODE_HEIGHT > b.y;
      assert.equal(overlaps, false, `${positions[left][0]} overlaps ${positions[right][0]}`);
    }
  }
}

test("packs isolated task cards into a compact landscape canvas", () => {
  for (const count of [100, 500]) {
    const layout = layoutDependencyGraph(isolated(count));
    const canvas = bounds(layout);
    const aspect = canvas.width / canvas.height;

    assert.equal(layout.positions.size, count);
    assert.ok(aspect > 1.25 && aspect < 1.65, `${count} nodes produced aspect ${aspect}`);
    assertNoOverlaps(layout);
  }
});

test("keeps connected components and geometry deterministic", () => {
  const tasks: Task[] = [
    { id: "a" },
    { id: "b", deps: ["a"] },
    { id: "c", deps: ["a"] },
    { id: "d", deps: ["c"] },
    { id: "e" },
  ];
  const first = layoutDependencyGraph(tasks);
  const second = layoutDependencyGraph([...tasks].reverse());

  assert.equal(first.components.length, 2);
  assert.deepEqual([...first.positions.entries()], [...second.positions.entries()]);
  assertNoOverlaps(first);
});

test("keeps large unlinked task sets out of the dependency canvas", () => {
  const tasks = isolated(1_000);
  const partition = partitionDependencyTasks(tasks);

  assert.equal(partition.tasks.length, 1_000);
  assert.equal(partition.connected.length, 0);
  assert.equal(partition.isolated.length, 1_000);
  assert.deepEqual(partition.connections, []);
});

test("routes only connected graphs above the threshold to the layout Worker", () => {
  assert.equal(shouldUseDependencyLayoutWorker(DEPENDENCY_LAYOUT_WORKER_THRESHOLD), false);
  assert.equal(shouldUseDependencyLayoutWorker(DEPENDENCY_LAYOUT_WORKER_THRESHOLD + 1), true);
  assert.equal(shouldUseDependencyLayoutWorker(1_000), true);
});

test("lays a 1,000-task connected chain with complete geometry", () => {
  const layout = layoutDependencyGraph(chain(1_000));

  assert.equal(layout.positions.size, 1_000);
  assert.equal(layout.connections.length, 999);
  assert.equal(layout.components.length, 1);
  assertNoOverlaps(layout);
});

test("partitions valid dependency islands without losing cycles or isolated work", () => {
  const tasks: Task[] = [
    { id: "a", deps: ["c"] },
    { id: "b", deps: ["a"] },
    { id: "c", deps: ["b"] },
    { id: "solo" },
    { id: "dangling", deps: ["outside-scope"] },
  ];
  const first = partitionDependencyTasks(tasks);
  const second = partitionDependencyTasks([...tasks].reverse());

  assert.deepEqual(first.connected.map((task) => task.id), ["a", "b", "c"]);
  assert.deepEqual(first.isolated.map((task) => task.id), ["dangling", "solo"]);
  assert.deepEqual(first, second);
});

test("keeps the dependency canvas auto-arranged and non-editable", () => {
  const source = readFileSync(new URL("./Graph.tsx", import.meta.url), "utf8");

  assert.match(source, /nodesConnectable=\{false\}/);
  assert.match(source, /edgesReconnectable=\{false\}/);
  assert.match(source, /connectOnClick=\{false\}/);
  assert.match(source, /deleteKeyCode=\{null\}/);
  assert.match(source, /isConnectableStart=\{false\}/);
  assert.match(source, /isConnectableEnd=\{false\}/);
  assert.match(source, /Auto-arrange from dependencies/);
  assert.match(source, /data-carbon-task-surface/);
});
