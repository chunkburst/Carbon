/// <reference types="node" />

import assert from "node:assert/strict";
import test from "node:test";
import type { Status, Task } from "./api.ts";
import {
  ANIMATION_BOARD_STYLE_REGISTRY,
  MARKET_CANDLE_COUNT,
  MARKET_TIMEFRAME_SECONDS,
  buildMarketKlineScene,
  buildPixelAgentsScene,
  marketRegime,
  summarizeAnimationBoard,
} from "./animation-board.ts";

const workflow = {
  states: ["todo", "in_progress", "review", "done", "blocked"],
  closed: ["done"],
  initial: "todo",
  taskStagnationAfterSeconds: 24 * 60 * 60,
} satisfies Pick<Status, "states" | "closed" | "initial" | "taskStagnationAfterSeconds">;

function task(id: string, fields: Partial<Task> = {}): Task {
  return { id, title: id, status: "todo", ready: false, ...fields };
}

const at = (day: number, hour = 0, minute = 0) => new Date(Date.UTC(2026, 7, day, hour, minute)).toISOString();
const unix = (value: string) => Math.floor(Date.parse(value) / 1_000);

function staleTask(id: string, created = at(1), fields: Partial<Task> = {}): Task {
  const stagnantAt = new Date(Date.parse(created) + workflow.taskStagnationAfterSeconds * 1_000).toISOString();
  return task(id, {
    status: "review",
    activityHealth: "stagnant",
    lastMeaningfulAt: created,
    stagnantAt,
    provenance: [{ who: "publisher", at: created, did: "created" }],
    ...fields,
  });
}

function candleFor(scene: ReturnType<typeof buildMarketKlineScene>, kind: string) {
  const marker = scene.markers.find((entry) => entry.eventKind === kind);
  assert.ok(marker, `missing ${kind} marker`);
  const candle = scene.candles[marker.candleIndex];
  assert.ok(candle, `missing candle for ${kind}`);
  return { marker, candle };
}

test("style registry keeps the work and market renderers", () => {
  assert.deepEqual(Object.keys(ANIMATION_BOARD_STYLE_REGISTRY), ["pixel-agents", "market-kline"]);
  assert.equal(ANIMATION_BOARD_STYLE_REGISTRY["pixel-agents"].build({ tasks: [], status: workflow, tick: 0 }).kind, "pixel");
  assert.equal(ANIMATION_BOARD_STYLE_REGISTRY["market-kline"].build({ tasks: [], status: workflow, tick: 0 }).kind, "market");
});

test("pixel workstations remain stable while animation ticks change sprite phase", () => {
  const tasks = [
    task("A"),
    task("B", { status: "in_progress", executionState: "active" }),
    task("C", { status: "blocked", blockerReason: "dependency" }),
    task("D", { status: "done" }),
  ];
  const first = buildPixelAgentsScene({ tasks, status: workflow, tick: 2 });
  const replay = buildPixelAgentsScene({ tasks: [...tasks].reverse(), status: workflow, tick: 2 });
  const next = buildPixelAgentsScene({ tasks, status: workflow, tick: 3 });

  assert.deepEqual(first, replay);
  assert.deepEqual(
    first.agents.map((agent) => [agent.task.id, agent.station.slot]),
    next.agents.map((agent) => [agent.task.id, agent.station.slot]),
  );
  assert.ok(first.agents.some((agent, index) => agent.station.phase !== next.agents[index]?.station.phase));
});

test("task stagnation is counted separately from blockers and session loss", () => {
  const tasks = [
    staleTask("STALE", at(1), { executionState: "stalled" }),
    task("BLOCKED", { status: "blocked", blockerReason: "API down" }),
  ];
  const summary = summarizeAnimationBoard({ tasks, status: workflow });

  assert.equal(summary.stagnant, 1);
  assert.equal(summary.blocked, 2, "the legacy session state still affects the work scene only");
  assert.equal(marketRegime(summary), "stagnant");
});

test("an empty project has no fabricated wall-clock tape", () => {
  const scene = buildMarketKlineScene({ projectKey: "empty", tasks: [], status: workflow, tick: 0 });

  assert.equal(scene.candles.length, 0);
  assert.equal(scene.markers.length, 0);
  assert.equal(scene.currentPrice, 100);
  assert.equal(scene.structuralPrice, 100);
  assert.equal(scene.energyRatio, 0);
});

test("only meaningful task actions create time-axis points", () => {
  const scene = buildMarketKlineScene({
    projectKey: "meaningful",
    tasks: [task("FLOW", {
      provenance: [
        { who: "p", at: at(1, 8), did: "created" },
        { who: "system", at: at(1, 9), did: "poll tasks" },
        { who: "system", at: at(1, 10), did: "heartbeat" },
        { who: "worker", at: at(1, 11), did: "note" },
        { who: "worker", at: at(1, 12), did: "ran checks" },
      ],
    })],
    status: workflow,
    marketTimeframe: "1m",
    tick: 0,
  });

  assert.deepEqual(scene.markers.map((marker) => marker.eventKind), ["published", "processing", "processing"]);
  assert.equal(scene.candles.length, 3);
  assert.ok(scene.candles.every((candle, index) => index === 0 || candle.time > scene.candles[index - 1]!.time));
});

test("periods aggregate real event times without filling empty intervals", () => {
  const tasks = [task("PERIOD", {
    provenance: [
      { who: "p", at: at(1, 8, 2), did: "created" },
      { who: "w", at: at(1, 8, 4), did: "lease claimed" },
      { who: "w", at: at(1, 9, 15), did: "updated" },
      { who: "w", at: at(2, 8), did: "transition done" },
    ],
  })];
  const minute = buildMarketKlineScene({ projectKey: "period", tasks, status: workflow, marketTimeframe: "1m", tick: 0 });
  const hour = buildMarketKlineScene({ projectKey: "period", tasks, status: workflow, marketTimeframe: "1h", tick: 0 });
  const day = buildMarketKlineScene({ projectKey: "period", tasks, status: workflow, marketTimeframe: "1d", tick: 0 });

  assert.equal(minute.candles.length, 4);
  assert.equal(hour.candles.length, 3);
  assert.equal(day.candles.length, 2);
  assert.equal(minute.candles[0]?.time, Math.floor(unix(at(1, 8, 2)) / MARKET_TIMEFRAME_SECONDS["1m"]) * MARKET_TIMEFRAME_SECONDS["1m"]);
  assert.ok(minute.candles.length < 96 && day.candles.length < minute.candles.length);
});

test("one stagnant task keeps its workflow state and prints one capitulation wick", () => {
  const stale = staleTask("REVIEW-STALE");
  const scene = buildMarketKlineScene({ projectKey: "one-stale", tasks: [stale], status: workflow, marketTimeframe: "1h", tick: 0, styleMetadata: { speed: 100, volatility: 0 } });
  const plunge = candleFor(scene, "stagnant");

  assert.equal(plunge.marker.task.status, "review");
  assert.equal(scene.stagnantCount, 1);
  assert.equal(scene.structuralPrice, 80);
  assert.equal(scene.currentPrice, 80);
  assert.equal(plunge.candle.close, 80);
  assert.equal(plunge.candle.low, 1);
  assert.equal(scene.markers.filter((marker) => marker.eventKind === "stagnant").length, 1);
});

test("five stagnant tasks settle at 1 and never pass the floor", () => {
  const tasks = Array.from({ length: 5 }, (_, index) => staleTask(`STALE-${index + 1}`));
  const scene = buildMarketKlineScene({ projectKey: "five-stale", tasks, status: workflow, tick: 17 });

  assert.equal(scene.stagnantCount, 5);
  assert.equal(scene.structuralPrice, 1);
  assert.ok(scene.currentPrice >= 1 && scene.currentPrice < 2);
  assert.ok(scene.candles.every((candle) => candle.low >= 1 && candle.close >= 1));
  assert.equal(scene.candles.filter((candle) => candle.low === 1).length, 1, "one streak creates one long wick, not a needle wall");
});

test("a resumed task lifts the baseline according to the remaining stagnant count", () => {
  const created = at(1);
  const resumed = task("RESUMED", {
    status: "in_progress",
    activityHealth: "fresh",
    lastMeaningfulAt: at(3),
    provenance: [
      { who: "p", at: created, did: "created" },
      { who: "w", at: at(3), did: "updated" },
    ],
  });
  const tasks = [resumed, ...Array.from({ length: 4 }, (_, index) => staleTask(`STILL-${index + 1}`, created))];
  const scene = buildMarketKlineScene({ projectKey: "partial-recovery", tasks, status: workflow, marketTimeframe: "1h", tick: 0, styleMetadata: { speed: 100, volatility: 0 } });
  const recovery = candleFor(scene, "recovered");

  assert.equal(scene.stagnantCount, 4);
  assert.equal(scene.structuralPrice, 20);
  assert.equal(scene.currentPrice, 20);
  assert.ok(recovery.candle.close > recovery.candle.open);
});

test("when all work resumes the recovery candle returns to the 100 baseline", () => {
  const taskWithGap = task("RETURN", {
    status: "in_progress",
    activityHealth: "fresh",
    lastMeaningfulAt: at(3),
    provenance: [
      { who: "p", at: at(1), did: "created" },
      { who: "w", at: at(3), did: "updated" },
    ],
  });
  const scene = buildMarketKlineScene({ projectKey: "full-recovery", tasks: [taskWithGap], status: workflow, marketTimeframe: "1h", tick: 0, styleMetadata: { speed: 100, volatility: 0 } });
  const recovery = candleFor(scene, "recovered");

  assert.equal(scene.stagnantCount, 0);
  assert.equal(scene.structuralPrice, 100);
  assert.equal(scene.currentPrice, 100);
  assert.ok(recovery.candle.close > recovery.candle.open);
});

test("refreshes do not duplicate stagnation events and ticks only animate the final candle", () => {
  const tasks = [staleTask("STABLE"), task("ACTIVE", {
    status: "in_progress",
    executionState: "active",
    provenance: [{ who: "w", at: at(3), did: "updated" }],
  })];
  const first = buildMarketKlineScene({ projectKey: "stable", tasks, status: workflow, tick: 3 });
  const next = buildMarketKlineScene({ projectKey: "stable", tasks: [...tasks].reverse(), status: workflow, tick: 4 });

  assert.deepEqual(first.markers, next.markers);
  assert.deepEqual(first.candles.slice(0, -1), next.candles.slice(0, -1));
  assert.notDeepEqual(first.candles.at(-1), next.candles.at(-1));
  assert.equal(next.markers.filter((marker) => marker.eventKind === "stagnant").length, 1);
});

test("event buckets are capped without breaking marker alignment or OHLC", () => {
  const events = Array.from({ length: MARKET_CANDLE_COUNT + 20 }, (_, index) => ({
    who: "worker",
    at: new Date(Date.parse(at(1)) + index * 60 * 60 * 1_000).toISOString(),
    did: index === 0 ? "created" : "updated",
  }));
  const scene = buildMarketKlineScene({
    projectKey: "window",
    tasks: [task("LONG", { status: "in_progress", provenance: events })],
    status: workflow,
    marketTimeframe: "1h",
    tick: 0,
    styleMetadata: { speed: 100, volatility: 0 },
  });

  assert.equal(scene.candles.length, MARKET_CANDLE_COUNT);
  assert.ok(scene.markers.every((marker) => scene.candles[marker.candleIndex]?.time === marker.time));
  for (const candle of scene.candles) {
    assert.ok(candle.high >= Math.max(candle.open, candle.close));
    assert.ok(candle.low <= Math.min(candle.open, candle.close));
    assert.ok(candle.low >= 1);
  }
});

test("volatility metadata changes live motion without changing event history", () => {
  const tasks = [task("LIVE", {
    status: "in_progress",
    executionState: "active",
    provenance: [{ who: "w", at: at(3), did: "updated" }],
  })];
  const frozen = [0, 8, 17].map((tick) => buildMarketKlineScene({ projectKey: "motion", tasks, status: workflow, tick, styleMetadata: { speed: 100, volatility: 0 } }));
  const moving = [0, 8, 17].map((tick) => buildMarketKlineScene({ projectKey: "motion", tasks, status: workflow, tick, styleMetadata: { speed: 100, volatility: 600 } }));

  assert.deepEqual(frozen[0]?.candles, frozen[1]?.candles);
  assert.notEqual(moving[0]?.currentPrice, moving[1]?.currentPrice);
  assert.deepEqual(moving[0]?.markers, moving[2]?.markers);
});
