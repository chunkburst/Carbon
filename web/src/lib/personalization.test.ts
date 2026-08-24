/// <reference types="node" />

import assert from "node:assert/strict";
import test from "node:test";
import {
  getAnimationStyleMetadata,
  getMarketTimeframe,
  getTaskListPresentation,
  getWorkspaceTaskSurface,
  setAnimationStyleMetadata,
  setMarketTimeframe,
  setTaskListPresentation,
  setWorkspaceTaskSurface,
} from "./personalization.ts";

function withWindow(run: (store: Map<string, string>) => void): void {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "window");
  const store = new Map<string, string>();
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      localStorage: {
        getItem: (key: string) => store.get(key) ?? null,
        setItem: (key: string, value: string) => store.set(key, value),
      },
      dispatchEvent: () => true,
    },
  });
  try {
    run(store);
  } finally {
    if (descriptor) Object.defineProperty(globalThis, "window", descriptor);
    else Reflect.deleteProperty(globalThis, "window");
  }
}

test("task list presentation is independent from the legacy animation board choice", () => {
  withWindow((store) => {
    store.set("carbon:board-presentation", "animation");
    assert.equal(getTaskListPresentation(), "row");

    setTaskListPresentation("card");
    assert.equal(getTaskListPresentation(), "card");
    assert.equal(store.get("carbon:board-presentation"), "animation");
  });
});

test("task list presentation migrates a legacy row or card preference once", () => {
  withWindow((store) => {
    store.set("carbon:board-presentation", "card");
    assert.equal(getTaskListPresentation(), "card");
    store.set("carbon:task-list-presentation:v1", "broken");
    assert.equal(getTaskListPresentation(), "card");
  });
});

test("workspace remembers whether Tasks, Agent work, or Board was last selected", () => {
  withWindow(() => {
    assert.equal(getWorkspaceTaskSurface(), "tasks");
    setWorkspaceTaskSurface("board");
    assert.equal(getWorkspaceTaskSurface(), "board");
    setWorkspaceTaskSurface("agent-work");
    assert.equal(getWorkspaceTaskSurface(), "agent-work");
  });
});

test("market timeframes are remembered independently for each project", () => {
  withWindow(() => {
    assert.equal(getMarketTimeframe("project-alpha"), "1h");
    setMarketTimeframe("project-alpha", "5m");
    setMarketTimeframe("project-beta", "1d");
    assert.equal(getMarketTimeframe("project-alpha"), "5m");
    assert.equal(getMarketTimeframe("project-beta"), "1d");
  });
});

test("invalid or empty market timeframe preferences fall back to one hour", () => {
  withWindow((store) => {
    store.set("carbon:market-timeframe:v1:project-alpha", "3d");
    assert.equal(getMarketTimeframe("project-alpha"), "1h");
    assert.equal(getMarketTimeframe(""), "1h");
  });
});

test("animation metadata is isolated by project and style", () => {
  withWindow(() => {
    assert.deepEqual(getAnimationStyleMetadata("project-alpha", "market-kline"), { speed: 100, volatility: 200 });
    setAnimationStyleMetadata("project-alpha", "market-kline", { speed: 180, volatility: 640 });
    setAnimationStyleMetadata("project-alpha", "pixel-agents", { speed: 75, volatility: 120 });
    setAnimationStyleMetadata("project-beta", "market-kline", { speed: 45, volatility: 20 });

    assert.deepEqual(getAnimationStyleMetadata("project-alpha", "market-kline"), { speed: 180, volatility: 640 });
    assert.deepEqual(getAnimationStyleMetadata("project-alpha", "pixel-agents"), { speed: 75, volatility: 120 });
    assert.deepEqual(getAnimationStyleMetadata("project-beta", "market-kline"), { speed: 45, volatility: 20 });
  });
});

test("animation metadata rejects malformed values and clamps valid numbers", () => {
  withWindow((store) => {
    store.set("carbon:animation-style-metadata:v1:project-alpha:market-kline", JSON.stringify({
      speed: 9_999,
      volatility: -40,
    }));
    assert.deepEqual(getAnimationStyleMetadata("project-alpha", "market-kline"), { speed: 300, volatility: 0 });

    store.set("carbon:animation-style-metadata:v1:project-alpha:pixel-agents", JSON.stringify({
      speed: "fast",
      volatility: null,
    }));
    assert.deepEqual(getAnimationStyleMetadata("project-alpha", "pixel-agents"), { speed: 100, volatility: 200 });
    assert.deepEqual(getAnimationStyleMetadata("", "market-kline"), { speed: 100, volatility: 200 });
  });
});
