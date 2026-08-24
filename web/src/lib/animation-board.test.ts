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
  stableHash,
  summarizeAnimationBoard,
} from "./animation-board.ts";
import { getAnimationBoardStyle, getBoardPresentation } from "./personalization.ts";

const workflow = {
  states: ["todo", "in_progress", "done", "blocked"],
  closed: ["done"],
  initial: "todo",
} satisfies Pick<Status, "states" | "closed" | "initial">;

function task(id: string, fields: Partial<Task> = {}): Task {
  return {
    id,
    title: id,
    status: "todo",
    ready: false,
    ...fields,
  };
}

function at(hour: number): string {
  return `2026-08-23T${String(hour).padStart(2, "0")}:00:00.000Z`;
}

function after(iso: string, seconds: number): string {
  return new Date(Date.parse(iso) + seconds * 1_000).toISOString();
}

function eventCandle(scene: ReturnType<typeof buildMarketKlineScene>, kind: string) {
  const marker = scene.markers.find((candidate) => candidate.eventKind === kind);
  assert.ok(marker, `missing ${kind} marker`);
  return { marker, candle: scene.candles[marker.candleIndex]! };
}

function longestGreenRun(scene: ReturnType<typeof buildMarketKlineScene>): number {
  return scene.candles.reduce(
    (state, candle) => candle.close > candle.open
      ? { current: state.current + 1, longest: Math.max(state.longest, state.current + 1) }
      : { current: 0, longest: state.longest },
    { current: 0, longest: 0 },
  ).longest;
}

function candleBody(candle: ReturnType<typeof buildMarketKlineScene>["candles"][number]): number {
  return Math.abs(candle.close - candle.open);
}

function candleShadow(candle: ReturnType<typeof buildMarketKlineScene>["candles"][number]): number {
  return Math.max(candle.high - Math.max(candle.open, candle.close), Math.min(candle.open, candle.close) - candle.low);
}

function candleOhlc(candle: ReturnType<typeof buildMarketKlineScene>["candles"][number]) {
  return {
    time: candle.time,
    open: candle.open,
    high: candle.high,
    low: candle.low,
    close: candle.close,
  };
}

function upperShadow(candle: ReturnType<typeof buildMarketKlineScene>["candles"][number]): number {
  return candle.high - Math.max(candle.open, candle.close);
}

function lowerShadow(candle: ReturnType<typeof buildMarketKlineScene>["candles"][number]): number {
  return Math.min(candle.open, candle.close) - candle.low;
}

function average(values: readonly number[]): number {
  return values.reduce((sum, value) => sum + value, 0) / Math.max(1, values.length);
}

function sceneNeedles(scene: ReturnType<typeof buildMarketKlineScene>): number {
  return scene.candles.filter((candle) => candleShadow(candle) > 0.12).length;
}

test("style registry exposes the two extensible animation renderers", () => {
  assert.deepEqual(Object.keys(ANIMATION_BOARD_STYLE_REGISTRY), ["pixel-agents", "market-kline"]);
  assert.equal(ANIMATION_BOARD_STYLE_REGISTRY["pixel-agents"].build({ tasks: [], status: workflow, tick: 0 }).kind, "pixel");
  assert.equal(ANIMATION_BOARD_STYLE_REGISTRY["market-kline"].build({ tasks: [], status: workflow, tick: 0 }).kind, "market");
});

test("invalid historical presentation preferences fail closed to row and pixel", () => {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      localStorage: {
        getItem(key: string) {
          if (key === "carbon:board-presentation") return "gallery";
          if (key === "carbon:animation-board-style") return "neon-candles";
          return null;
        },
      },
    },
  });

  try {
    assert.equal(getBoardPresentation(), "row");
    assert.equal(getAnimationBoardStyle(), "pixel-agents");
  } finally {
    if (descriptor) Object.defineProperty(globalThis, "window", descriptor);
    else Reflect.deleteProperty(globalThis, "window");
  }
});

test("pixel stations are stable and a tick changes only sprite phase", () => {
  const tasks = [
    task("TASK-1", { title: "Plan interface", assignee: "planner", type: "foundation" }),
    task("TASK-2", { title: "Build worker", status: "in_progress", executionState: "active", assignee: "worker", type: "extension" }),
    task("TASK-3", { title: "Validate delivery", status: "done", type: "patch" }),
    task("TASK-4", { title: "Unblock API", status: "blocked", blockerReason: "waiting for token", assignee: "ops" }),
  ];
  const first = buildPixelAgentsScene({ tasks, status: workflow, tick: 4 });
  const replay = buildPixelAgentsScene({ tasks: [...tasks].reverse(), status: workflow, tick: 4 });
  const next = buildPixelAgentsScene({ tasks, status: workflow, tick: 5 });

  assert.deepEqual(first, replay);
  assert.equal(first.lanes.find((lane) => lane.state === "active")?.count, 1);
  const geometry = (scene: typeof first) => scene.agents.map((agent) => ({ id: agent.task.id, x: agent.x, y: agent.y, station: agent.station.slot }));
  assert.deepEqual(geometry(first), geometry(next));
  assert.ok(first.agents.some((agent, index) => agent.station.phase !== next.agents[index]?.station.phase));
});

test("pixel stations form unique workflow columns on a dense board", () => {
  const states: Array<Partial<Task>> = [
    { status: "todo" },
    { status: "in_progress", executionState: "active" },
    { status: "blocked", blockerReason: "waiting" },
    { status: "done" },
  ];
  const tasks = Array.from({ length: 68 }, (_, index) => task(`DENSE-${index + 1}`, states[index % states.length]));
  const scene = buildPixelAgentsScene({ tasks, status: workflow, tick: 0 });
  assert.equal(new Set(scene.agents.map((agent) => agent.station.slot)).size, tasks.length);
  for (const agent of scene.agents) {
    const expected = agent.state === "queued" ? 0 : agent.state === "active" ? 1 : agent.state === "blocked" ? 2 : 3;
    assert.equal(agent.station.column, expected);
  }
});

test("compact provenance becomes a multi-event task tape while notes stay silent", () => {
  const scene = buildMarketKlineScene({
    projectKey: "event-tape",
    status: workflow,
    tick: 0,
    tasks: [
      task("JOURNEY", {
        status: "done",
        provenance: [
          { who: "publisher", at: at(1), did: "created" },
          { who: "worker", at: at(2), did: "claim approved" },
          { who: "worker", at: at(3), did: "began session" },
          { who: "worker", at: at(3), did: "ran checks" },
          { who: "worker", at: at(5), did: "transition done" },
          { who: "worker", at: at(6), did: "note" },
        ],
      }),
      task("BLOCK", { status: "blocked", blockerReason: "dependency", provenance: [{ who: "ops", at: at(3), did: "transition blocked" }] }),
      task("RECOVER", { status: "in_progress", executionState: "active", provenance: [{ who: "ops", at: at(4), did: "unblocked" }] }),
      task("QUIET", { provenance: [{ who: "writer", at: at(5), did: "note" }] }),
    ],
  });
  const journey = scene.markers.filter((marker) => marker.task.id === "JOURNEY");
  const kinds = new Set(scene.markers.map((marker) => marker.eventKind));

  assert.deepEqual(journey.map((marker) => marker.eventKind), ["published", "claimed", "processing", "processing", "completed"]);
  assert.ok(kinds.has("blocked") && kinds.has("recovered") && kinds.has("quiet"));
  assert.ok(!scene.markers.some((marker) => marker.did === "note"));
  assert.equal(new Set(scene.markers.map((marker) => marker.id)).size, scene.markers.length);
  assert.equal(journey[0]?.actor, "publisher");
  assert.equal(journey.at(-1)?.pattern, "completion-rally");
  assert.equal(journey[2]?.candleIndex, journey[3]?.candleIndex, "one task can publish multiple events into one time window");
  assert.ok(journey.every((marker) => marker.time === scene.candles[marker.candleIndex]?.time));
});

test("market periods use real fixed buckets and keep the final bar after the latest event", () => {
  const eventTime = "2026-08-23T10:07:42.000Z";
  const unixTime = Math.floor(Date.parse(eventTime) / 1_000);

  for (const [timeframe, step] of Object.entries(MARKET_TIMEFRAME_SECONDS)) {
    const scene = buildMarketKlineScene({
      projectKey: `period:${timeframe}`,
      marketTimeframe: timeframe as keyof typeof MARKET_TIMEFRAME_SECONDS,
      status: workflow,
      tick: 0,
      tasks: [task(`PERIOD-${timeframe}`, { provenance: [{ who: "publisher", at: eventTime, did: "created" }] })],
    });
    const marker = scene.markers[0]!;

    assert.equal(scene.timeframe, timeframe);
    assert.equal(scene.barSeconds, step);
    assert.equal(scene.candles.length, MARKET_CANDLE_COUNT);
    assert.ok(scene.candles.every((candle, index) => index === 0 || candle.time - scene.candles[index - 1]!.time === step));
    assert.equal(marker.time, Math.floor(unixTime / step) * step);
    assert.equal(marker.candleIndex, MARKET_CANDLE_COUNT - 2);
    assert.equal(scene.candles.at(-1)?.time, marker.time + step);
  }
});

test("every fixed period keeps a continuous, bounded task tape", () => {
  const eventTime = "2026-08-23T10:07:42.000Z";
  const waveLengths = new Map<string, number>();

  for (const [timeframe, step] of Object.entries(MARKET_TIMEFRAME_SECONDS)) {
    const scene = buildMarketKlineScene({
      projectKey: "continuous-periods",
      marketTimeframe: timeframe as keyof typeof MARKET_TIMEFRAME_SECONDS,
      status: workflow,
      tick: 0,
      tasks: [
        task(`EVENT-${timeframe}`, { provenance: [{ who: "publisher", at: eventTime, did: "created" }] }),
        task(`TAIL-${timeframe}`, { updatedAt: after(eventTime, step * 24) }),
      ],
    });
    const marker = eventCandle(scene, "published").marker;
    const wave = scene.candles.filter((candle) => candle.pattern === "publish-volatility");
    const wicks = scene.candles.map(candleShadow);

    assert.equal(scene.candles.length, MARKET_CANDLE_COUNT);
    assert.ok(scene.candles.every((candle, index) => index === 0 || candle.open === scene.candles[index - 1]!.close));
    assert.ok(marker.candleIndex >= 0 && marker.candleIndex < MARKET_CANDLE_COUNT);
    waveLengths.set(timeframe, wave.length);
    assert.ok(wave.length >= 1 && wave.length <= 16, `${timeframe} retains a period-appropriate activity wave`);
    assert.ok(scene.candles.every((candle) => candleBody(candle) <= 0.82));
    assert.ok(scene.candles.filter((candle) => candle.pattern === "quiet-drift").every((candle) => candleBody(candle) <= 0.12));
    assert.ok(wicks.every((wick) => wick <= 0.16));
    assert.ok(wicks.filter((wick) => wick > 0.12).length <= 1, `${timeframe} permits at most one restrained sweep`);

    for (let index = 0; index < wave.length; index += 1) {
      const body = candleBody(wave[index]!);
      if (body <= 0.24) continue;
      const neighbours = [wave[index - 1], wave[index + 1]].filter(Boolean);
      assert.ok(
        wave.length <= 2 || neighbours.some((neighbour) => candleBody(neighbour!) >= Math.min(0.12, body * 0.32)),
        `${timeframe} activity wave has no isolated giant pillar`,
      );
    }
  }

  assert.ok(waveLengths.get("1m")! > waveLengths.get("5m")!);
  assert.ok(waveLengths.get("5m")! > waveLengths.get("30m")!);
  assert.ok(waveLengths.get("30m")! >= waveLengths.get("1h")!);
  assert.ok(waveLengths.get("1h")! >= waveLengths.get("1d")!);
  assert.ok(new Set(waveLengths.values()).size >= 4, "periods must not redraw the same wave length");
});

test("old timestamped events scroll out instead of being squeezed into the first candle", () => {
  const scene = buildMarketKlineScene({
    projectKey: "fixed-window",
    marketTimeframe: "1h",
    status: workflow,
    tick: 0,
    tasks: [
      task("OLD", { provenance: [{ who: "p", at: "2026-08-01T01:00:00.000Z", did: "created" }] }),
      task("RECENT", { provenance: [{ who: "w", at: "2026-08-23T14:00:00.000Z", did: "updated" }] }),
    ],
  });

  assert.ok(!scene.markers.some((marker) => marker.task.id === "OLD"));
  assert.equal(scene.markers.find((marker) => marker.task.id === "RECENT")?.candleIndex, MARKET_CANDLE_COUNT - 2);
});

test("untimed tasks retain board statistics without fabricating historical events", () => {
  const input = { projectKey: "untimed-evidence", status: workflow, tick: 0 };
  const empty = buildMarketKlineScene({ ...input, tasks: [] });
  const scene = buildMarketKlineScene({
    ...input,
    tasks: [
      task("UNTIMED-DONE", { status: "done" }),
      task("UNTIMED-WORK", { status: "in_progress", executionState: "active", provenance: [{ who: "worker", at: "", did: "updated" }] }),
      task("UNTIMED-BLOCK", { status: "blocked", blockerReason: "dependency", provenance: [{ who: "worker", at: "", did: "stalled" }] }),
    ],
  });

  assert.equal(scene.summary.total, 3);
  assert.equal(scene.summary.completed, 1);
  assert.equal(scene.summary.active, 1);
  assert.equal(scene.summary.blocked, 1);
  assert.equal(scene.markers.length, 0);
  assert.ok(scene.candles.every((candle) => candle.cause === "quiet" && candle.eventCount === 0));
  assert.deepEqual(scene.candles.slice(0, -1), empty.candles.slice(0, -1));
  assert.notDeepEqual(scene.candles.at(-1), empty.candles.at(-1), "current task state may animate only the live candle");
});

test("each market activity has bounded direction, pattern, and energy semantics", () => {
  const make = (id: string, fields: Partial<Task>) => buildMarketKlineScene({ projectKey: `semantic:${id}`, tasks: [task(id, fields)], status: workflow, tick: 0 });
  const published = make("PUBLISH", { provenance: [{ who: "p", at: at(1), did: "created" }] });
  const claimed = make("CLAIM", { provenance: [{ who: "w", at: at(1), did: "lease claimed" }] });
  const processing = make("PROCESS", { status: "in_progress", executionState: "active", provenance: [{ who: "w", at: at(1), did: "updated" }] });
  const completed = make("DONE", { status: "done", provenance: [{ who: "w", at: at(1), did: "transition done" }] });
  const blocked = make("BLOCK", { status: "blocked", blockerReason: "dependency", provenance: [{ who: "w", at: at(1), did: "stalled" }] });
  const recovered = make("RECOVER", { status: "in_progress", executionState: "active", provenance: [{ who: "w", at: at(1), did: "unblocked" }] });

  const publish = eventCandle(published, "published");
  const claim = eventCandle(claimed, "claimed");
  const process = eventCandle(processing, "processing");
  const done = eventCandle(completed, "completed");
  const stop = eventCandle(blocked, "blocked");
  const recover = eventCandle(recovered, "recovered");

  assert.equal(publish.candle.pattern, "publish-volatility");
  assert.ok(candleBody(publish.candle) >= 0.2, "publish begins a visible setup body");
  assert.equal(claim.candle.pattern, "claim-compression");
  assert.ok(candleBody(claim.candle) < candleBody(publish.candle), "claim is calmer than a release");
  assert.equal(process.candle.pattern, "processing-contest");
  assert.ok(process.candle.bullForce > 0 && process.candle.bearForce > 0);
  assert.ok(process.candle.energy > claim.candle.energy);
  assert.ok(done.candle.close > done.candle.open && done.candle.pattern === "completion-rally");
  assert.ok(stop.candle.close < stop.candle.open && stop.candle.open - stop.candle.close >= 0.18);
  assert.ok(stop.candle.energy > claim.candle.energy && stop.candle.pattern === "blocker-selloff");
  assert.ok(recover.candle.close > recover.candle.open && recover.candle.pattern === "recovery-bounce");
});

test("delivery, recovery, and blockers settle through compact five-minute directional waves", () => {
  const tail = task("TAIL", { updatedAt: after(at(3), 5 * 60 * 20) });
  const sceneFor = (id: string, did: string, fields: Partial<Task>, pattern: string) => {
    const scene = buildMarketKlineScene({
      projectKey: `directional:${id}`,
      marketTimeframe: "5m",
      status: workflow,
      tick: 0,
      tasks: [task(id, { ...fields, provenance: [{ who: "worker", at: at(3), did }] }), tail],
    });
    return scene.candles.filter((candle) => candle.pattern === pattern);
  };
  const completed = sceneFor("DONE", "transition done", { status: "done" }, "completion-rally");
  const recovered = sceneFor("RECOVER", "unblocked", { status: "in_progress", executionState: "active" }, "recovery-bounce");
  const blocked = sceneFor("BLOCK", "stalled", { status: "blocked", blockerReason: "dependency" }, "blocker-selloff");
  const direction = (candles: readonly { open: number; close: number }[]) => candles.reduce((sum, candle) => sum + candle.close - candle.open, 0);

  assert.ok(completed.length >= 5 && completed.length <= 8);
  assert.ok(recovered.length >= 5 && recovered.length <= 8);
  assert.ok(blocked.length >= 6 && blocked.length <= 9);
  assert.ok(direction(completed) > 0.45);
  assert.ok(direction(recovered) > 0.45);
  assert.ok(direction(blocked) < -0.5);
});

test("batch publishing carries more energy than one publish through bodies rather than a needle wall", () => {
  const one = buildMarketKlineScene({
    projectKey: "publish-one",
    status: workflow,
    tick: 0,
    tasks: [task("P-1", { provenance: [{ who: "p", at: at(7), did: "published" }] })],
  });
  const batch = buildMarketKlineScene({
    projectKey: "publish-batch",
    status: workflow,
    tick: 0,
    tasks: Array.from({ length: 24 }, (_, index) => task(`P-${index + 1}`, { provenance: [{ who: "p", at: at(7), did: "published" }] })),
  });
  const oneCandle = eventCandle(one, "published").candle;
  const batchCandle = eventCandle(batch, "published").candle;
  const batchRange = Math.max(...batch.candles.map((candle) => candle.high)) - Math.min(...batch.candles.map((candle) => candle.low));
  const quiet = buildMarketKlineScene({ projectKey: "publish-quiet", status: workflow, tick: 0, tasks: [] });
  const quietEnergy = average(quiet.candles.map((candle) => candle.energy));

  assert.ok(batchCandle.eventCount > oneCandle.eventCount);
  assert.ok(batchCandle.energy > oneCandle.energy);
  assert.ok(candleBody(batchCandle) >= candleBody(oneCandle));
  assert.ok(batchRange < 7);
  assert.ok(batch.candles.every((candle) => candleBody(candle) <= 0.82));
  assert.ok(sceneNeedles(batch) <= 1);
  assert.ok(batchCandle.energy / quietEnergy < 6.5, "display energy keeps large and small activity in one readable range");
});

test("claim activity is more compressed than its preceding publish", () => {
  const scene = buildMarketKlineScene({
    projectKey: "claim-compression",
    status: workflow,
    tick: 0,
    tasks: [task("FLOW", {
      provenance: [
        { who: "p", at: at(1), did: "created" },
        { who: "w", at: at(4), did: "lease claimed" },
      ],
    })],
  });
  const publish = eventCandle(scene, "published").candle;
  const claim = eventCandle(scene, "claimed").candle;
  const publishRange = publish.high - publish.low;
  const claimRange = claim.high - claim.low;

  assert.ok(claimRange < publishRange);
  assert.ok(candleBody(claim) < candleBody(publish));
  assert.ok(claim.energy > 10, "claim remains visibly more active than a quiet tape");
});

test("five-minute publish and processing form quick multi-bar contests while claims stay calmer", () => {
  const tail = task("TAIL", { updatedAt: after(at(3), 5 * 60 * 20) });
  const sceneFor = (projectKey: string, did: string, fields: Partial<Task>, pattern: string) => {
    const scene = buildMarketKlineScene({
      projectKey,
      marketTimeframe: "5m",
      status: workflow,
      tick: 0,
      tasks: [task("EVENT", { ...fields, provenance: [{ who: "worker", at: at(3), did }] }), tail],
    });
    return { scene, wave: scene.candles.filter((candle) => candle.pattern === pattern) };
  };
  const quiet = buildMarketKlineScene({ projectKey: "contest-quiet", status: workflow, tick: 0, tasks: [task("QUIET")] });
  const publish = sceneFor("contest-publish", "created", {}, "publish-volatility");
  const processing = sceneFor("contest-processing", "updated", { status: "in_progress", executionState: "active" }, "processing-contest");
  const claimed = sceneFor("contest-claim", "lease claimed", {}, "claim-compression");
  const quietBody = average(quiet.candles.map(candleBody));
  const publishBody = average(publish.wave.map(candleBody));
  const processingBody = average(processing.wave.map(candleBody));
  const claimBody = average(claimed.wave.map(candleBody));
  const hasSameSideRun = (candles: readonly { open: number; close: number }[]) => candles
    .slice(1)
    .some((candle, index) => (candle.close - candle.open) * (candles[index]!.close - candles[index]!.open) > 0);
  const hasBothSides = (candles: readonly { open: number; close: number }[]) => candles
    .some((candle) => candle.close > candle.open)
    && candles.some((candle) => candle.close < candle.open);
  const noIsolatedPillar = (candles: readonly { open: number; close: number }[]) => candles
    .every((candle, index) => {
      const body = Math.abs(candle.close - candle.open);
      if (body <= 0.4) return true;
      const neighbours = [candles[index - 1], candles[index + 1]].filter(Boolean);
      return neighbours.some((neighbour) => Math.abs(neighbour!.close - neighbour!.open) >= Math.min(0.24, body * 0.42));
    });

  assert.ok(publish.wave.length >= 8 && publish.wave.length <= 11);
  assert.ok(processing.wave.length >= 9 && processing.wave.length <= 12);
  assert.ok(hasBothSides(publish.wave) && hasBothSides(processing.wave));
  assert.ok(hasSameSideRun(publish.wave) && hasSameSideRun(processing.wave), "the contest must not alternate like a metronome");
  assert.ok(noIsolatedPillar(publish.wave) && noIsolatedPillar(processing.wave));
  assert.ok(publishBody > quietBody * 2.5);
  assert.ok(processingBody > quietBody * 2.5);
  assert.ok(claimBody < publishBody && claimBody < processingBody);
  assert.ok(average(claimed.wave.map((candle) => candle.energy)) > average(quiet.candles.map((candle) => candle.energy)));
  assert.ok(average(processing.wave.map((candle) => candle.energy)) > average(quiet.candles.map((candle) => candle.energy)) * 2);
});

test("quiet work produces bounded red/green drift without long needles", () => {
  const scene = buildMarketKlineScene({ projectKey: "quiet-tape", tasks: [task("QUIET")], status: workflow, tick: 0 });
  const bodies = scene.candles.map((candle) => candle.close - candle.open);
  const range = Math.max(...scene.candles.map((candle) => candle.high)) - Math.min(...scene.candles.map((candle) => candle.low));

  assert.ok(bodies.some((body) => body > 0) && bodies.some((body) => body < 0));
  assert.ok(range < 1 && Math.abs(scene.currentPrice - 100) < 1);
  assert.ok(sceneNeedles(scene) <= 1, "quiet tape has no needle pattern");
  assert.ok(scene.markers.every((marker) => marker.eventKind === "quiet"));
});

test("dense timestamped deliveries stay liquid instead of forming a staircase", () => {
  const tasks = [
    ...Array.from({ length: 68 }, (_, index) => task(`DENSE-DONE-${index + 1}`, {
      status: "done",
      provenance: [{ who: "worker", at: at(7), did: "transition done" }],
    })),
    task("DENSE-QUEUED", { updatedAt: at(12) }),
  ];
  const scene = buildMarketKlineScene({ projectKey: "dense-release", tasks, status: workflow, tick: 0 });
  const range = Math.max(...scene.candles.map((candle) => candle.high)) - Math.min(...scene.candles.map((candle) => candle.low));

  assert.equal(scene.markers.length, tasks.length);
  assert.equal(new Set(scene.markers.map((marker) => marker.id)).size, tasks.length);
  assert.ok(scene.candles.some((candle) => candle.close > candle.open));
  assert.ok(scene.candles.some((candle) => candle.close < candle.open));
  assert.ok(longestGreenRun(scene) <= 10, "dense delivery does not become an endless green staircase");
  assert.ok(scene.candles.every((candle) => candleBody(candle) <= 0.82));
  assert.ok(sceneNeedles(scene) <= 1, "dense history must not turn into a needle array");
  assert.ok(range < 5 && Math.abs(scene.currentPrice - 100) < 3);
  assert.ok(Math.min(...scene.candles.map((candle) => candle.low)) > 90);
  assert.ok(Math.max(...scene.candles.map((candle) => candle.high)) < 110);
});

test("project keys isolate seeded markets while same-project refreshes stay stable", () => {
  const tasks = [
    task("A", { status: "done", provenance: [{ who: "w", at: at(1), did: "transition done" }] }),
    task("B", { status: "in_progress", executionState: "active", provenance: [{ who: "w", at: at(3), did: "updated" }] }),
  ];
  const input = { tasks, status: workflow, tick: 4, projectKey: "project-alpha" };
  const first = buildMarketKlineScene(input);
  const replay = buildMarketKlineScene({ ...input, tasks: [...tasks].reverse() });
  const isolated = buildMarketKlineScene({ ...input, projectKey: "project-beta" });

  assert.deepEqual(first, replay);
  assert.notDeepEqual(first.candles, isolated.candles);
  assert.equal(stableHash("Carbon"), stableHash("Carbon"));
});

test("adding a provenance event preserves existing historical candles outside its local window", () => {
  const base = task("FLOW", {
    status: "done",
    provenance: [
      { who: "p", at: "2026-08-23T01:00:00.000Z", did: "created" },
      { who: "w", at: "2026-08-23T03:00:00.000Z", did: "transition done" },
    ],
  });
  const appended = {
    ...base,
    provenance: [
      ...base.provenance!,
      { who: "w", at: "2026-08-24T02:00:00.000Z", did: "updated" },
    ],
  } satisfies Task;
  const input = { projectKey: "local-event-window", status: workflow, tick: 0 };
  const before = buildMarketKlineScene({ ...input, tasks: [base] });
  const after = buildMarketKlineScene({ ...input, tasks: [appended] });
  const beforeByTime = new Map(before.candles.map((candle) => [candle.time, candle]));
  const oldLiveTime = before.candles.at(-1)!.time;
  const sharedHistorical = after.candles.filter((candle) => candle.time < oldLiveTime && beforeByTime.has(candle.time));

  assert.ok(sharedHistorical.length > 40);
  for (const candle of sharedHistorical) assert.deepEqual(candle, beforeByTime.get(candle.time));
  assert.ok(after.markers.some((marker) => marker.eventKind === "processing" && marker.did === "updated"));
});

test("OHLC summaries stay valid and ticks update only the live candle", () => {
  const input = {
    projectKey: "live-event-tape",
    status: workflow,
    tasks: [
      task("EVENTS", {
        status: "done",
        provenance: [
          { who: "p", at: at(1), did: "created" },
          { who: "w", at: at(2), did: "lease claimed" },
          { who: "w", at: at(3), did: "began session" },
          { who: "w", at: at(4), did: "transition done" },
        ],
      }),
      task("BLOCK", { status: "blocked", blockerReason: "dependency", provenance: [{ who: "ops", at: at(5), did: "transition blocked" }] }),
    ],
  };
  const first = buildMarketKlineScene({ ...input, tick: 3 });
  const next = buildMarketKlineScene({ ...input, tick: 4 });

  for (const candle of first.candles) {
    assert.ok(candle.high >= Math.max(candle.open, candle.close));
    assert.ok(candle.low <= Math.min(candle.open, candle.close));
    assert.ok(candle.high >= candle.low);
    assert.ok(candle.energy >= 10 && candle.energy <= 72);
    assert.ok(candle.bullForce >= 0 && candle.bearForce >= 0);
  }
  assert.deepEqual(first.candles.slice(0, -1), next.candles.slice(0, -1));
  assert.notDeepEqual(first.candles.at(-1), next.candles.at(-1));
  assert.deepEqual(first.markers, next.markers);
  assert.equal(first.dominantEvent, "blocked");
  assert.ok(first.currentPattern.length > 0);
  assert.deepEqual(summarizeAnimationBoard(input), {
    total: 2,
    active: 0,
    completed: 1,
    blocked: 1,
    queued: 0,
    allInProgress: false,
  });
  assert.equal(marketRegime(first.summary), "blocked");
});

test("active work produces a visible fast intrabar contest without rewriting history", () => {
  const tasks = [task("LIVE", {
    status: "in_progress",
    executionState: "active",
    updatedAt: "2026-08-23T10:00:00.000Z",
  })];
  const scenes = Array.from({ length: 12 }, (_, tick) => buildMarketKlineScene({
    projectKey: "live-fast-contest",
    status: workflow,
    tasks,
    tick,
  }));
  const closes = scenes.map((scene) => scene.candles.at(-1)!.close);

  assert.ok(Math.max(...closes) - Math.min(...closes) > 0.16);
  for (const scene of scenes.slice(1)) {
    assert.deepEqual(scene.candles.slice(0, -1), scenes[0]!.candles.slice(0, -1));
  }
});

test("zero market volatility freezes live OHLC and pressure without touching history", () => {
  const input = {
    projectKey: "metadata-zero-volatility",
    status: workflow,
    tasks: [task("LIVE", {
      status: "in_progress",
      executionState: "active",
      updatedAt: "2026-08-23T10:00:00.000Z",
    })],
    styleMetadata: { speed: 100, volatility: 0 },
  };
  const scenes = [0, 1, 7, 24].map((tick) => buildMarketKlineScene({ ...input, tick }));
  const first = scenes[0]!;

  for (const scene of scenes) {
    assert.equal(scene.livePressure, 0);
    assert.deepEqual(candleOhlc(scene.candles.at(-1)!), candleOhlc(first.candles.at(-1)!));
    assert.deepEqual(scene.candles.slice(0, -1), first.candles.slice(0, -1));
  }
});

test("market volatility creates bounded, non-metronomic live contests without rewriting history", () => {
  const input = {
    projectKey: "metadata-live-contest",
    status: workflow,
    tasks: [task("LIVE", {
      status: "in_progress",
      executionState: "active",
      updatedAt: "2026-08-23T10:00:00.000Z",
    })],
  };
  const scenesFor = (volatility: number) => Array.from({ length: 48 }, (_, tick) => buildMarketKlineScene({
    ...input,
    tick,
    styleMetadata: { speed: 100, volatility },
  }));
  const defaultScenes = scenesFor(200);
  const highScenes = scenesFor(1_000);
  const defaultPressure = defaultScenes.map((scene) => scene.livePressure);
  const defaultCloses = defaultScenes.map((scene) => scene.candles.at(-1)!.close);
  const highCloses = highScenes.map((scene) => scene.candles.at(-1)!.close);
  const consecutiveDirections = defaultPressure
    .map((pressure, index) => index === 0 ? 0 : Math.sign(pressure - defaultPressure[index - 1]!))
    .filter(Boolean);
  const defaultRange = Math.max(...defaultCloses) - Math.min(...defaultCloses);
  const highRange = Math.max(...highCloses) - Math.min(...highCloses);
  const implicitDefault = buildMarketKlineScene({ ...input, tick: 19 });

  assert.deepEqual(implicitDefault, defaultScenes[19], "the project default keeps a 200-point live contest");
  assert.ok(defaultPressure.some((pressure) => pressure > 0));
  assert.ok(defaultPressure.some((pressure) => pressure < 0));
  assert.ok(new Set(defaultPressure.map((pressure) => pressure.toFixed(5))).size > 30);
  assert.ok(consecutiveDirections.some((direction, index) => index > 0 && direction === consecutiveDirections[index - 1]), "motion does not alternate like a metronome");
  assert.ok(defaultRange > 0.16, "the default setting has visible intrabar movement");
  assert.ok(highRange > defaultRange * 1.6, "a high user setting materially expands the contest");

  for (const scene of [...defaultScenes, ...highScenes]) {
    assert.ok(scene.livePressure >= -0.82 && scene.livePressure <= 0.82);
  }
  for (const scene of defaultScenes.slice(1)) {
    assert.deepEqual(scene.candles.slice(0, -1), defaultScenes[0]!.candles.slice(0, -1));
  }
  for (const scene of highScenes.slice(1)) {
    assert.deepEqual(scene.candles.slice(0, -1), highScenes[0]!.candles.slice(0, -1));
  }
});

test("a pushed-back live contest prints restrained rejection wicks instead of a needle wall", () => {
  const input = {
    projectKey: "metadata-rejection-wick",
    status: workflow,
    tasks: [task("LIVE", {
      status: "in_progress",
      executionState: "active",
      updatedAt: "2026-08-23T10:00:00.000Z",
    })],
  };
  const sceneAt = (volatility: number, tick: number) => buildMarketKlineScene({
    ...input,
    tick,
    styleMetadata: { speed: 100, volatility },
  });
  const volatileScenes = Array.from({ length: 48 }, (_, tick) => sceneAt(1_000, tick));
  const restingScenes = Array.from({ length: 48 }, (_, tick) => sceneAt(0, tick));
  const upperRejectionIndex = volatileScenes.findIndex((scene, index) => {
    if (index < 2) return false;
    const prior = volatileScenes[index - 1]!.livePressure;
    return prior > volatileScenes[index - 2]!.livePressure
      && prior > scene.livePressure
      && upperShadow(scene.candles.at(-1)!) > upperShadow(restingScenes[index]!.candles.at(-1)!) + 0.01;
  });
  const lowerRejectionIndex = volatileScenes.findIndex((scene, index) => {
    if (index < 2) return false;
    const prior = volatileScenes[index - 1]!.livePressure;
    return prior < volatileScenes[index - 2]!.livePressure
      && prior < scene.livePressure
      && lowerShadow(scene.candles.at(-1)!) > lowerShadow(restingScenes[index]!.candles.at(-1)!) + 0.01;
  });

  assert.ok(upperRejectionIndex >= 0, "a rejected upward push leaves an upper wick");
  assert.ok(lowerRejectionIndex >= 0, "a rejected downward push leaves a lower wick");
  for (const scene of volatileScenes) {
    assert.ok(scene.candles.every((candle) => upperShadow(candle) <= 0.115 + Number.EPSILON));
    assert.ok(scene.candles.every((candle) => lowerShadow(candle) <= 0.115 + Number.EPSILON));
    assert.ok(scene.candles.filter((candle) => candleShadow(candle) > 0.075).length <= 3, "rejections remain sparse");
  }
});
