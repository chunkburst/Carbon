import type { Status, Task } from "./api.ts";
import {
  DEFAULT_ANIMATION_STYLE_METADATA,
  type AnimationBoardStyle,
  type AnimationStyleMetadata,
  type MarketTimeframe,
} from "./personalization.ts";
import { buildTaskEventMarketScene } from "./task-event-market.ts";

export type AnimationTaskState = "active" | "completed" | "blocked" | "queued";
export type MarketRegime = "success" | "idle" | "blocked" | "stagnant" | "all-active";
export type MarketActivityKind = "published" | "claimed" | "processing" | "completed" | "blocked" | "stagnant" | "recovered" | "quiet";
export type MarketPattern = "publish-volatility" | "claim-compression" | "processing-contest" | "completion-rally" | "blocker-selloff" | "stagnation-plunge" | "recovery-bounce" | "quiet-drift" | "mixed-flow";
export type MarketCandleCause = MarketActivityKind | "mixed";

export type AnimationBoardInput = {
  tasks: readonly Task[];
  status: Pick<Status, "states" | "closed" | "initial" | "taskStagnationAfterSeconds">;
  /** Stable project scope for market seeding. Omit only for legacy callers. */
  projectKey?: string;
  /** Candle period for the task market. Defaults to the balanced one-hour tape. */
  marketTimeframe?: MarketTimeframe;
  /** Project/style-local presentation tuning. It never changes persisted task data. */
  styleMetadata?: AnimationStyleMetadata;
  /** A monotonically increasing UI tick; it is never a source of randomness. */
  tick: number;
};

export type AnimationBoardSummary = {
  total: number;
  active: number;
  completed: number;
  blocked: number;
  stagnant: number;
  queued: number;
  allInProgress: boolean;
};

export type PixelAgentLane = {
  state: AnimationTaskState;
  count: number;
  top: number;
};

export type PixelWorkstationAction = "dispatch" | "typing" | "blocked" | "complete";

export type PixelTaskStation = {
  /** Stable slot values make a task keep its workstation while the scene ticks. */
  slot: number;
  column: number;
  row: number;
  /** A four-frame motion phase. This is the only per-tick pixel scene change. */
  phase: number;
};

export type PixelTaskAgent = {
  task: Task;
  state: AnimationTaskState;
  lane: AnimationTaskState;
  x: number;
  y: number;
  station: PixelTaskStation;
  action: PixelWorkstationAction;
  workerInitial: string;
  taskType: string;
};

export type PixelAgentsScene = {
  kind: "pixel";
  summary: AnimationBoardSummary;
  lanes: PixelAgentLane[];
  columns: number;
  rows: number;
  agents: PixelTaskAgent[];
};

export type MarketCandle = {
  /** UTC seconds. Lightweight Charts uses this as its time axis value. */
  time: number;
  open: number;
  close: number;
  high: number;
  low: number;
  /** Deterministic task-market volume, compressed to [10, 72]. */
  energy: number;
  cause: MarketCandleCause;
  bullForce: number;
  bearForce: number;
  eventCount: number;
  pattern: MarketPattern;
};

export type MarketTaskMarker = {
  /** Stable event id; one task can contribute several provenance events. */
  id: string;
  task: Task;
  state: AnimationTaskState;
  candleIndex: number;
  time: number;
  position: "aboveBar" | "belowBar";
  shape: "circle" | "square" | "arrowUp" | "arrowDown";
  eventKind: MarketActivityKind;
  did: string;
  actor: string;
  /** Signed local contribution before candle-window aggregation. */
  force: number;
  energy: number;
  pattern: MarketPattern;
};

export type MarketKlineScene = {
  kind: "market";
  /** The selected, project-persisted chart period. */
  timeframe: MarketTimeframe;
  /** Seconds represented by one candle in this scene. */
  barSeconds: number;
  summary: AnimationBoardSummary;
  regime: MarketRegime;
  energyRatio: number;
  currentPrice: number;
  /** Signed live contest pressure for synchronizing the force meter with price. */
  livePressure: number;
  candles: MarketCandle[];
  markers: MarketTaskMarker[];
  dominantEvent?: MarketActivityKind;
  currentPattern: MarketPattern;
  /** Current task-health pressure. Five or more stagnant tasks settle at 1. */
  stagnantCount: number;
  structuralPrice: number;
};

export type AnimationBoardModel = PixelAgentsScene | MarketKlineScene;

export type AnimationBoardStyleDefinition = {
  id: AnimationBoardStyle;
  label: { english: string; chinese: string };
  description: { english: string; chinese: string };
  build: (input: AnimationBoardInput) => AnimationBoardModel;
};

const WORKING_STATES = new Set(["active", "in_progress", "in-progress", "working", "doing"]);
const PIXEL_COLUMNS = 4;
export const MARKET_CANDLE_COUNT = 96;
/**
 * Fixed, real-world buckets. The market must never stretch its candle period to
 * fit history: a minute chart is always a minute chart and old events simply
 * leave the 96-bar viewport when they no longer fit.
 */
export const MARKET_TIMEFRAME_SECONDS = {
  "1m": 60,
  "5m": 5 * 60,
  "30m": 30 * 60,
  "1h": 60 * 60,
  "1d": 24 * 60 * 60,
} as const satisfies Record<MarketTimeframe, number>;
// A task market is an indicator, not a score counter. Keep it in a narrow,
// familiar quote range so task events read as local price action instead of a
// cumulative line that can run away after a large release.
const MARKET_REFERENCE_PRICE = 100;
const MARKET_PRICE_FLOOR = 90;
const MARKET_PRICE_CEILING = 110;
// A synthetic task market still needs a stable time scale. Keeping a fixed start
// makes the same project replay exactly the same visual timeline on every device.
const MARKET_START_TIME = Math.floor(Date.UTC(2025, 0, 6, 9, 0, 0) / 1000);
const PIXEL_STATE_ORDER: AnimationTaskState[] = ["queued", "active", "blocked", "completed"];

function normalizeState(value: string | undefined): string {
  return value?.trim().toLowerCase().replace(/\s+/g, "_") ?? "";
}

function clamp(value: number, lower: number, upper: number): number {
  return Math.min(upper, Math.max(lower, value));
}

function finiteTick(value: number): number {
  return Number.isFinite(value) ? Math.trunc(value) : 0;
}

function orderedTasks(tasks: readonly Task[]): Task[] {
  return [...tasks].sort((left, right) => {
    if (left.id === right.id) return 0;
    return left.id < right.id ? -1 : 1;
  });
}

/**
 * A compact FNV-1a hash shared by every scene. It turns task identity and a UI
 * tick into repeatable visual variation without relying on unseeded randomness.
 */
export function stableHash(value: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

/** A repeatable number in [0, 1] for a supplied seed. */
export function stableUnit(seed: string): number {
  return stableHash(seed) / 0xffffffff;
}

function stableSigned(seed: string): number {
  return stableUnit(seed) * 2 - 1;
}

/** A decorrelated deterministic variation for adjacent numeric tape slots. */
function stableJitter(seed: string): number {
  let value = stableHash(seed);
  value = Math.imul(value ^ (value >>> 16), 0x45d9f3b);
  value = Math.imul(value ^ (value >>> 16), 0x45d9f3b);
  value ^= value >>> 16;
  return (value >>> 0) / 0xffffffff * 2 - 1;
}

function taskIsClosed(task: Task, status: Pick<Status, "closed">): boolean {
  const closed = new Set((status.closed ?? []).map(normalizeState));
  return closed.has(normalizeState(task.status));
}

function taskIsWorking(task: Task): boolean {
  return task.executionState === "active" || WORKING_STATES.has(normalizeState(task.status));
}

export function animationTaskState(task: Task, status: Pick<Status, "closed">): AnimationTaskState {
  if (task.blockerReason?.trim() || task.executionState === "stalled" || normalizeState(task.status) === "blocked") {
    return "blocked";
  }
  if (taskIsClosed(task, status)) return "completed";
  if (taskIsWorking(task)) return "active";
  return "queued";
}

export function summarizeAnimationBoard(input: Pick<AnimationBoardInput, "tasks" | "status">): AnimationBoardSummary {
  let active = 0;
  let completed = 0;
  let blocked = 0;
  let stagnant = 0;
  let queued = 0;

  for (const task of input.tasks) {
    if (task.activityHealth === "stagnant") stagnant += 1;
    switch (animationTaskState(task, input.status)) {
      case "active":
        active += 1;
        break;
      case "completed":
        completed += 1;
        break;
      case "blocked":
        blocked += 1;
        break;
      default:
        queued += 1;
    }
  }

  return {
    total: input.tasks.length,
    active,
    completed,
    blocked,
    stagnant,
    queued,
    allInProgress: input.tasks.length > 0 && input.tasks.every(taskIsWorking),
  };
}

function pixelAction(state: AnimationTaskState): PixelWorkstationAction {
  switch (state) {
    case "active":
      return "typing";
    case "completed":
      return "complete";
    case "blocked":
      return "blocked";
    default:
      return "dispatch";
  }
}

function workerInitial(value: string | undefined): string {
  return Array.from(value?.trim() ?? "")[0]?.toUpperCase() || "·";
}

/**
 * Each workflow state owns one visible floor column. Within a column tasks are sorted
 * by a stable task-id hash, so a refresh or animation tick cannot reshuffle desks.
 * A state transition deliberately moves the task to the corresponding workflow area;
 * that movement is the task-distribution signal the pixel scene is meant to expose.
 */
function pixelSlots(
  tasks: readonly Task[],
  status: Pick<Status, "closed">,
): { rows: number; slots: Map<string, number> } {
  const slots = new Map<string, number>();
  const groups = PIXEL_STATE_ORDER.map((state) => orderedTasks(tasks)
    .filter((task) => animationTaskState(task, status) === state)
    .sort((left, right) => stableHash(`${left.id}:pixel-station`) - stableHash(`${right.id}:pixel-station`)));
  const rows = Math.max(1, ...groups.map((group) => group.length));

  for (const [column, group] of groups.entries()) {
    for (const [row, task] of group.entries()) {
      slots.set(task.id, row * PIXEL_COLUMNS + column);
    }
  }

  return { rows, slots };
}

/** Builds a stable task/agent scene. Tick changes only agent sprite phase. */
export function buildPixelAgentsScene(input: AnimationBoardInput): PixelAgentsScene {
  const summary = summarizeAnimationBoard(input);
  const tick = finiteTick(input.tick);
  const tasks = orderedTasks(input.tasks);
  const { rows, slots } = pixelSlots(tasks, input.status);
  const lanes = PIXEL_STATE_ORDER.map((state, index) => ({
    state,
    count: tasks.filter((task) => animationTaskState(task, input.status) === state).length,
    top: 12 + index * 24,
  }));
  const agents = tasks.map((task) => {
    const state = animationTaskState(task, input.status);
    const slot = slots.get(task.id) ?? 0;
    const column = slot % PIXEL_COLUMNS;
    const row = Math.floor(slot / PIXEL_COLUMNS);
    const phase = (stableHash(`${task.id}:pixel-phase`) + tick) % 4;

    return {
      task,
      state,
      lane: state,
      // These are useful to non-grid consumers and intentionally exclude tick.
      x: 8 + ((column + 0.5) / PIXEL_COLUMNS) * 84,
      y: 12 + ((row + 0.5) / rows) * 76,
      station: { slot, column, row, phase },
      action: pixelAction(state),
      workerInitial: workerInitial(task.assignee),
      taskType: task.type?.trim() || "task",
    } satisfies PixelTaskAgent;
  });

  return { kind: "pixel", summary, lanes, columns: PIXEL_COLUMNS, rows, agents };
}

export function marketRegime(summary: AnimationBoardSummary): MarketRegime {
  if (summary.stagnant > 0) return "stagnant";
  if (summary.blocked > 0) return "blocked";
  if (summary.allInProgress) return "all-active";
  if (summary.completed > 0) return "success";
  return "idle";
}

function parseUnixSeconds(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const milliseconds = Date.parse(value);
  return Number.isFinite(milliseconds) ? Math.floor(milliseconds / 1_000) : undefined;
}

type MarketActivityEvent = {
  id: string;
  task: Task;
  state: AnimationTaskState;
  kind: MarketActivityKind;
  did: string;
  actor: string;
  time?: number;
};

type MarketTimeline = {
  start: number;
  step: number;
  end: number;
};

type ActivityProfile = {
  bull: number;
  bear: number;
  energy: number;
  volatility: number;
  convergence: number;
  pattern: MarketPattern;
};

type PreparedMarketMarker = MarketTaskMarker & ActivityProfile;

type MarketBucketGroup = {
  /** Stable event identity; viewport indices must never affect market shape. */
  seed: string;
  kind: MarketActivityKind;
  count: number;
  bull: number;
  bear: number;
  energy: number;
  volatility: number;
  convergence: number;
  pattern: MarketPattern;
  intensity: number;
};

type MarketBucket = {
  markers: PreparedMarketMarker[];
  groups: MarketBucketGroup[];
  bull: number;
  bear: number;
  energy: number;
  volatility: number;
  convergence: number;
  cause: MarketCandleCause;
  pattern: MarketPattern;
  dominant?: MarketActivityKind;
};

function normalizeActivityText(value: string | undefined): string {
  return value?.trim().toLowerCase().replace(/[\s-]+/g, "_").replace(/[^a-z0-9_\u4e00-\u9fff]+/g, "_") ?? "";
}

function latestKnownTaskTime(task: Task): number | undefined {
  const candidates = [
    parseUnixSeconds(task.updatedAt),
    ...(task.provenance ?? []).flatMap((entry) => [parseUnixSeconds(entry.at), parseUnixSeconds(entry.editedAt)]),
  ].filter((value): value is number => value !== undefined);
  return candidates.length > 0 ? Math.max(...candidates) : undefined;
}

function activityKindForProvenance(did: string, closedStates: Set<string>): MarketActivityKind | undefined {
  const normalized = normalizeActivityText(did);
  const raw = did.trim().toLowerCase();
  const has = (...terms: string[]) => terms.some((term) => normalized.includes(term) || raw.includes(term));
  const isTransition = has("transition", "transitioned", "moved", "state_change", "状态");
  const hasClosedDestination = [...closedStates].some((state) => normalized.includes(state) || raw.includes(state));

  // Notes/messages intentionally carry no market force. The list endpoint may
  // include them in a compact provenance projection, so they must not become
  // fake liquidity or price action.
  if (has("note", "comment", "message", "draft", "备注", "笔记", "评论", "消息")) return undefined;
  if (has("unblocked", "recovered", "恢复", "解除阻塞")) return "recovered";
  if (has("blocked", "stalled", "阻塞", "停滞")) return "blocked";
  if ((isTransition && hasClosedDestination) || has("completed", "closed", "完成", "已关闭")) return "completed";
  if (has("lease_claimed", "claimed", "reassigned", "claim_approved", "认领", "领取", "重新分配", "批准认领")) return "claimed";
  if (
    has(
      "began_session",
      "started_session",
      "updated",
      "ran_checks",
      "in_progress",
      "awaiting_review",
      "review",
      "开始会话",
      "更新",
      "运行检查",
      "进行中",
      "审核",
    )
  ) return "processing";
  if (has("created", "create", "published", "publish", "创建", "发布")) return "published";
  return undefined;
}

function snapshotActivityKind(state: AnimationTaskState): MarketActivityKind {
  switch (state) {
    case "completed":
      return "completed";
    case "blocked":
      return "blocked";
    case "active":
      return "processing";
    default:
      return "quiet";
  }
}

/**
 * List rows may contain compact provenance only (`who`, `at`, `did`). Convert
 * recognized operational transitions into an event tape and fall back to one
 * timestamped snapshot only when a task has no useful event history. An
 * untimed task must never invent a point in the historical tape.
 */
function extractMarketActivityEvents(
  tasks: readonly Task[],
  status: Pick<Status, "closed">,
): MarketActivityEvent[] {
  const closedStates = new Set((status.closed ?? []).map(normalizeState));
  const events: MarketActivityEvent[] = [];

  for (const task of orderedTasks(tasks)) {
    const state = animationTaskState(task, status);
    let recognized = 0;
    for (const [index, entry] of (task.provenance ?? []).entries()) {
      const did = entry.did?.trim() ?? "";
      const kind = activityKindForProvenance(did, closedStates);
      if (!kind) continue;
      recognized += 1;
      events.push({
        id: `${task.id}:provenance:${entry.id ?? index}:${index}`,
        task,
        state,
        kind,
        did: did || kind,
        actor: entry.who?.trim() || task.assignee?.trim() || "Carbon",
        time: parseUnixSeconds(entry.at) ?? parseUnixSeconds(entry.editedAt),
      });
    }

    const snapshotTime = latestKnownTaskTime(task);
    if (recognized === 0 && snapshotTime !== undefined) {
      const kind = snapshotActivityKind(state);
      events.push({
        id: `${task.id}:snapshot:${kind}`,
        task,
        state,
        kind,
        did: `snapshot:${kind}`,
        actor: task.assignee?.trim() || "Carbon",
        time: snapshotTime,
      });
    }
  }

  return events.sort((left, right) => (
    (left.time ?? Number.POSITIVE_INFINITY) - (right.time ?? Number.POSITIVE_INFINITY)
    || left.id.localeCompare(right.id)
  ));
}

function floorToMarketBar(time: number, step: number): number {
  return Math.floor(time / step) * step;
}

/**
 * The legacy market caps its viewport at 96 bars. The current event market
 * keeps the same cap while omitting empty wall-clock buckets entirely.
 */
function buildMarketTimeline(events: readonly MarketActivityEvent[], step: number): MarketTimeline {
  const actualTimes = events.flatMap((event) => event.time === undefined ? [] : [event.time]);
  if (actualTimes.length === 0) {
    const start = floorToMarketBar(MARKET_START_TIME, step);
    return { start, step, end: start + step * (MARKET_CANDLE_COUNT - 1) };
  }

  const latest = Math.max(...actualTimes);
  const end = floorToMarketBar(latest, step) + step;
  return { start: end - step * (MARKET_CANDLE_COUNT - 1), step, end };
}

/**
 * A marker is evidence, not decoration: it must occupy its real timestamped
 * bar or disappear when it falls outside the fixed viewport. Untimed legacy
 * tasks still contribute to board statistics, but never fabricate a historic
 * market event at a hash-derived location.
 */
function eventCandleIndex(event: MarketActivityEvent, timeline: MarketTimeline): number | undefined {
  if (event.time === undefined) return undefined;
  const index = Math.floor((event.time - timeline.start) / timeline.step);
  return index >= 0 && index < MARKET_CANDLE_COUNT ? index : undefined;
}

function activityProfile(kind: MarketActivityKind, seed: string): ActivityProfile {
  const variation = stableSigned(`${seed}:profile`);
  switch (kind) {
    case "published":
      return { bull: 1.08 + variation * 0.12, bear: 1.05 - variation * 0.12, energy: 58, volatility: 1, convergence: 0, pattern: "publish-volatility" };
    case "claimed":
      return { bull: 0.2, bear: 0.23, energy: 25, volatility: 0.24, convergence: 0.62, pattern: "claim-compression" };
    case "processing":
      return { bull: 1.16 + variation * 0.16, bear: 1.13 - variation * 0.16, energy: 66, volatility: 1.12, convergence: 0.04, pattern: "processing-contest" };
    case "completed":
      return { bull: 1.24 + variation * 0.12, bear: 0.07, energy: 50, volatility: 0.42, convergence: 0, pattern: "completion-rally" };
    case "blocked":
      return { bull: 0.05, bear: 1.62 + variation * 0.16, energy: 70, volatility: 0.7, convergence: 0, pattern: "blocker-selloff" };
    case "recovered":
      return { bull: 1.36 + variation * 0.12, bear: 0.12, energy: 60, volatility: 0.56, convergence: 0, pattern: "recovery-bounce" };
    default:
      return { bull: 0.02, bear: 0.04, energy: 8, volatility: 0.05, convergence: 0.18, pattern: "quiet-drift" };
  }
}

function markerPresentation(kind: MarketActivityKind): Pick<MarketTaskMarker, "position" | "shape"> {
  switch (kind) {
    case "completed":
    case "recovered":
      return { position: "belowBar", shape: "arrowUp" };
    case "blocked":
      return { position: "aboveBar", shape: "arrowDown" };
    case "claimed":
    case "processing":
      return { position: "belowBar", shape: "circle" };
    default:
      return { position: "aboveBar", shape: "square" };
  }
}

function compactForce(value: number): number {
  return Math.round(value * 1_000) / 1_000;
}

function buildPreparedMarkers(
  events: readonly MarketActivityEvent[],
  timeline: MarketTimeline,
  seed: string,
): PreparedMarketMarker[] {
  return events.flatMap((event) => {
    const candleIndex = eventCandleIndex(event, timeline);
    // Never pin stale timestamped history to the first visible candle. A fixed
    // market viewport behaves like a real chart: out-of-range events are gone.
    if (candleIndex === undefined) return [];
    const profile = activityProfile(event.kind, `${seed}:${event.id}`);
    const { energy, pattern, ...marketShape } = profile;
    return [{
      ...marketShape,
      id: event.id,
      task: event.task,
      state: event.state,
      candleIndex,
      time: timeline.start + candleIndex * timeline.step,
      eventKind: event.kind,
      did: event.did,
      actor: event.actor,
      force: compactForce(marketShape.bull - marketShape.bear),
      energy: Math.round(energy),
      pattern,
      ...markerPresentation(event.kind),
    } satisfies PreparedMarketMarker];
  }).sort((left, right) => left.candleIndex - right.candleIndex || left.id.localeCompare(right.id));
}

function publicMarketMarker(marker: PreparedMarketMarker): MarketTaskMarker {
  return {
    id: marker.id,
    task: marker.task,
    state: marker.state,
    candleIndex: marker.candleIndex,
    time: marker.time,
    position: marker.position,
    shape: marker.shape,
    eventKind: marker.eventKind,
    did: marker.did,
    actor: marker.actor,
    force: marker.force,
    energy: marker.energy,
    pattern: marker.pattern,
  };
}

function groupIntensity(count: number): number {
  return clamp(1 + Math.log2(Math.max(1, count)) * 0.32, 1, 2.15);
}

/** Groups simultaneous events without turning a large release into 24 stacked needles. */
function buildMarketBuckets(markers: readonly PreparedMarketMarker[]): Map<number, MarketBucket> {
  const buckets = new Map<number, MarketBucket>();

  for (const marker of markers) {
    const bucket = buckets.get(marker.candleIndex) ?? {
      markers: [],
      groups: [],
      bull: 0,
      bear: 0,
      energy: 0,
      volatility: 0,
      convergence: 0,
      cause: "mixed" as MarketCandleCause,
      pattern: "mixed-flow" as MarketPattern,
    };
    bucket.markers.push(marker);
    buckets.set(marker.candleIndex, bucket);
  }

  for (const bucket of buckets.values()) {
    const byKind = new Map<MarketActivityKind, PreparedMarketMarker[]>();
    for (const marker of bucket.markers) {
      const group = byKind.get(marker.eventKind) ?? [];
      group.push(marker);
      byKind.set(marker.eventKind, group);
    }

    let strongest = -Infinity;
    for (const [kind, group] of byKind) {
      const count = group.length;
      const average = (field: "bull" | "bear" | "energy" | "volatility" | "convergence") => (
        group.reduce((sum, marker) => sum + marker[field], 0) / count
      );
      const intensity = groupIntensity(count);
      const entry: MarketBucketGroup = {
        seed: group.map((marker) => marker.id).sort().join("|"),
        kind,
        count,
        bull: average("bull") * intensity,
        bear: average("bear") * intensity,
        energy: average("energy") * (1 + Math.log2(Math.max(1, count)) * 0.42),
        volatility: average("volatility") * intensity,
        convergence: average("convergence"),
        pattern: group[0]!.pattern,
        intensity,
      };
      bucket.groups.push(entry);
      bucket.bull += entry.bull;
      bucket.bear += entry.bear;
      bucket.energy += entry.energy;
      bucket.volatility = Math.max(bucket.volatility, entry.volatility);
      bucket.convergence = Math.max(bucket.convergence, entry.convergence);
      const strength = entry.bull + entry.bear + entry.energy * 0.016;
      if (strength > strongest) {
        strongest = strength;
        bucket.dominant = kind;
        bucket.pattern = entry.pattern;
      }
    }
    bucket.cause = byKind.size === 1 ? bucket.dominant! : "mixed";
    if (byKind.size > 1) bucket.pattern = "mixed-flow";
  }

  return buckets;
}

type MarketImpact = {
  /** Incremental price pressure for this bar, never an absolute target price. */
  pressure: number;
  energy: number;
  range: number;
  bull: number;
  bear: number;
  cause?: MarketActivityKind;
  pattern?: MarketPattern;
  strength: number;
};

function emptyMarketImpact(): MarketImpact {
  return { pressure: 0, energy: 0, range: 0, bull: 0, bear: 0, strength: 0 };
}

/**
 * Events have a duration in wall-clock time, not a fixed count of candles.
 * A 45-minute contest therefore spans many one-minute bars, several five-minute
 * bars, and only one or two hourly bars. This is the key distinction between
 * periods: changing the period aggregates the same task activity instead of
 * redrawing an identical silhouette with different labels.
 */
function waveLength(kind: MarketActivityKind, seed: string, barSeconds: number): number {
  const variation = stableHash(`${seed}:wave-duration:${kind}`);
  let durationMinutes: number;
  let maximumBars: number;
  switch (kind) {
    case "published":
      durationMinutes = 42 + variation % 9;
      maximumBars = 16;
      break;
    case "processing":
      durationMinutes = 46 + variation % 11;
      maximumBars = 18;
      break;
    case "completed":
    case "recovered":
      durationMinutes = 28 + variation % 8;
      maximumBars = 10;
      break;
    case "blocked":
      durationMinutes = 32 + variation % 10;
      maximumBars = 11;
      break;
    case "claimed":
      durationMinutes = 15 + variation % 6;
      maximumBars = 6;
      break;
    default:
      return 0;
  }
  return clamp(Math.ceil((durationMinutes * 60) / barSeconds), 1, maximumBars);
}

function resampledWaveValue(shape: readonly number[], offset: number, length: number): number {
  if (length <= 1) return shape.reduce((sum, value) => sum + value, 0) / Math.max(1, shape.length);
  const start = Math.floor((offset / length) * shape.length);
  const end = Math.max(start + 1, Math.ceil(((offset + 1) / length) * shape.length));
  const section = shape.slice(start, end);
  const average = section.reduce((sum, value) => sum + value, 0) / Math.max(1, section.length);
  // Aggregated periods get a little more weight per bar, but never turn one
  // hour/day candle into the towering pillar that dominated the old chart.
  const aggregation = 1 + Math.log2(Math.max(1, shape.length / length)) * 0.1;
  return average * aggregation;
}

/**
 * Publishing and active work are a compact auction: a small setup candle,
 * two-sided expansion, then a visible settling leg. The shapes deliberately
 * include short same-side runs so they do not read as a mechanical red/green
 * saw tooth.
 */
function activeWavePressure(group: MarketBucketGroup, offset: number, seed: string, length: number): number {
  const canonical = group.kind === "processing"
    ? [-0.18, 0.26, 0.48, 0.58, 0.42, -0.24, -0.48, -0.56, -0.31, 0.22, 0.46, 0.54, 0.28, -0.25, -0.18, 0.16, 0.1, -0.08]
    : [0.16, 0.34, 0.46, 0.3, -0.22, -0.44, -0.5, -0.28, 0.2, 0.42, 0.48, 0.24, -0.2, -0.14, 0.12, 0.08];
  const shape = resampledWaveValue(canonical, offset, length);
  const totalForce = group.bull + group.bear;
  const balance = totalForce <= 0 ? 0 : (group.bull - group.bear) / totalForce;
  const scale = clamp(0.5 + group.intensity * 0.11 + group.energy * 0.0009, 0.62, 0.86);
  const direction = stableHash(`${seed}:wave-direction:${group.kind}:${group.seed}`) % 2 === 0 ? 1 : -1;
  const bias = balance * 0.12 * (offset < length - 2 ? 1 : 0.5);
  return clamp((shape * direction + bias) * scale, -0.72, 0.72);
}

/** Delivery and recovery produce a net rise; blockers produce the inverse. */
function directionalWavePressure(group: MarketBucketGroup, offset: number, length: number): number {
  const canonical = group.kind === "blocked"
    ? [0.18, 0.32, 0.46, 0.38, -0.12, 0.28, 0.2, 0.12, 0.08, 0.05, 0.03]
    : [0.14, 0.28, 0.42, 0.36, -0.1, 0.26, 0.2, 0.14, 0.08, 0.04];
  const shape = resampledWaveValue(canonical, offset, length);
  const scale = clamp(
    0.56 + group.intensity * 0.11 + group.energy * 0.0009 + (group.kind === "blocked" ? 0.04 : 0),
    0.68,
    0.9,
  );
  const sign = group.kind === "blocked" ? -1 : 1;
  return clamp(shape * scale * sign, -0.74, 0.74);
}

/** Claiming calms a release into a narrow, intentionally compressed handoff. */
function claimWavePressure(group: MarketBucketGroup, offset: number, seed: string, length: number): number {
  const shape = resampledWaveValue([0.07, -0.09, -0.06, 0.055, 0.035, -0.02], offset, length);
  const amplitude = clamp(0.72 + group.intensity * 0.06 + group.energy * 0.0006, 0.78, 0.94);
  const sign = stableHash(`${seed}:claim-direction:${group.seed}`) % 2 === 0 ? 1 : -1;
  return clamp(shape * amplitude * sign, -0.12, 0.12);
}

function addMarketImpact(
  impact: MarketImpact,
  group: MarketBucketGroup,
  offset: number,
  length: number,
  value: number,
): void {
  const decay = clamp(1 - (offset / Math.max(1, length - 1)) * 0.44, 0.56, 1);
  const strength = (group.bull + group.bear + group.energy * 0.016) * decay;
  impact.pressure = clamp(impact.pressure + value, -0.88, 0.88);
  impact.energy += group.energy * decay;
  impact.range = Math.max(impact.range, Math.abs(value) + group.volatility * 0.16);
  impact.bull += group.bull * decay;
  impact.bear += group.bear * decay;
  if (strength > impact.strength) {
    impact.strength = strength;
    impact.cause = group.kind;
    impact.pattern = group.pattern;
  }
}

/**
 * Every task event writes a short, bounded price-pressure window. The loop
 * consumes pressure incrementally, so a wave has room to build, contest, and
 * settle instead of snapping each next bar back to a fixed reference price.
 */
function buildMarketImpacts(buckets: ReadonlyMap<number, MarketBucket>, seed: string, barSeconds: number): MarketImpact[] {
  const impacts = Array.from({ length: MARKET_CANDLE_COUNT }, emptyMarketImpact);
  for (const [index, bucket] of buckets) {
    for (const group of bucket.groups) {
      const length = waveLength(group.kind, `${seed}:${group.seed}`, barSeconds);
      for (let offset = 0; offset < length; offset += 1) {
        const target = index + offset;
        if (target >= MARKET_CANDLE_COUNT) break;
        let value = 0;
        switch (group.kind) {
          case "published":
          case "processing":
            value = activeWavePressure(group, offset, seed, length);
            break;
          case "claimed":
            value = claimWavePressure(group, offset, seed, length);
            break;
          case "completed":
          case "blocked":
          case "recovered":
            value = directionalWavePressure(group, offset, length);
            break;
          default:
            break;
        }
        addMarketImpact(impacts[target]!, group, offset, length, value);
      }
    }
  }
  return impacts;
}

/** One restrained liquidity sweep is enough; ordinary task flow never needs a needle wall. */
function longNeedleIndex(buckets: ReadonlyMap<number, MarketBucket>, seed: string): number | undefined {
  const candidates = [...buckets.entries()]
    .filter(([, bucket]) => {
      const hasHighEnergyWork = bucket.groups.some((group) => group.kind === "published" || group.kind === "processing");
      return hasHighEnergyWork && bucket.markers.length >= 8 && bucket.energy >= 180;
    })
    .map(([index, bucket]) => ({
      index,
      markerSeed: bucket.markers.map((marker) => marker.id).sort().join("|"),
      score: bucket.energy + bucket.markers.length * 10 + stableUnit(`${seed}:needle-score:${bucket.markers.map((marker) => marker.id).sort().join("|")}`) * 0.01,
    }))
    .sort((left, right) => right.score - left.score || left.markerSeed.localeCompare(right.markerSeed));
  return candidates[0]?.index;
}

function marketReference(seed: string): number {
  return MARKET_REFERENCE_PRICE + stableSigned(`${seed}:reference`) * 0.18;
}

function marketMicroLevel(seed: string, time: number, step: number): number {
  const slot = Math.floor(time / step);
  const slowPhase = stableUnit(`${seed}:micro-slow-phase`) * Math.PI * 2;
  const quickPhase = stableUnit(`${seed}:micro-quick-phase`) * Math.PI * 2;
  // Absolute, project-scoped background levels make a shared historical bar
  // reproduce exactly even if a newer event shifts the 96-bar viewport.
  const slow = Math.sin((slot + 1) * 0.19 + slowPhase) * 0.11;
  const quick = Math.sin((slot + 1) * 0.43 + quickPhase) * 0.036;
  const texture = Math.sin((slot + 1) * 0.79 + slowPhase * 0.43) * 0.012;
  const counterFlow = Math.sin((slot + 1) * 1.11 + quickPhase * 0.62) * 0.018;
  // A project-scoped two-bar texture breaks up unusually long same-colour
  // runs while leaving each small local patch visually correlated.
  const blockTexture = stableJitter(`${seed}:micro-block:${Math.floor(slot / 2)}`) * 0.05;
  return slow + quick + texture + counterFlow + blockTexture;
}

function denseTapeMove(seed: string, time: number, step: number, density: number): number {
  if (density === 0) return 0;
  const slot = Math.floor(time / step);
  const phase = stableUnit(`${seed}:dense-phase`) * Math.PI * 2;
  return Math.sin((slot + 1) * 1.31 + phase) * (0.018 + density * 0.06);
}

function quietTapeLevel(seed: string, time: number, step: number): number {
  const slot = Math.floor(time / step);
  const driftPhase = stableUnit(`${seed}:quiet-drift-phase`) * Math.PI * 2;
  const reliefPhase = stableUnit(`${seed}:quiet-relief-phase`) * Math.PI * 2;
  const drift = Math.sin((slot + 1) * 0.071 + driftPhase) * 0.09;
  const relief = Math.sin((slot + 1) * 0.137 + reliefPhase) * 0.042;
  // A no-news tape sits slightly below the reference, with connected relief
  // waves that create green candles naturally rather than every fixed N bars.
  return -0.12 + drift + relief;
}

function marketBaselineLevel(seed: string, time: number, step: number, reference: number): number {
  return reference + marketMicroLevel(seed, time, step) + quietTapeLevel(seed, time, step);
}

function constrainDirectEventMove(cause: MarketCandleCause, move: number, seed: string): number {
  const sign = move === 0 ? (stableHash(`${seed}:body-direction`) % 2 === 0 ? 1 : -1) : Math.sign(move);
  switch (cause) {
    case "published":
    case "processing": {
      const floor = 0.15 + stableUnit(`${seed}:active-body-floor`) * 0.08;
      return clamp(sign * Math.max(Math.abs(move), floor), -0.72, 0.72);
    }
    case "claimed": {
      const floor = 0.04 + stableUnit(`${seed}:claim-body-floor`) * 0.035;
      return clamp(sign * Math.max(Math.abs(move), floor), -0.13, 0.13);
    }
    case "completed":
      return clamp(Math.max(move, 0.18 + stableUnit(`${seed}:completed-floor`) * 0.08), -0.08, 0.72);
    case "blocked":
      return clamp(Math.min(move, -(0.2 + stableUnit(`${seed}:blocked-floor`) * 0.09)), -0.76, 0.08);
    case "recovered":
      return clamp(Math.max(move, 0.19 + stableUnit(`${seed}:recovered-floor`) * 0.08), -0.08, 0.72);
    case "quiet":
      return clamp(move, -0.12, 0.12);
    default:
      return clamp(move, -0.82, 0.82);
  }
}

function softenMarketMove(value: number, limit: number): number {
  return Math.tanh(value / Math.max(0.001, limit)) * limit;
}

function readableQuietMove(value: number, seed: string): number {
  const bounded = clamp(value, -0.12, 0.12);
  if (Math.abs(bounded) >= 0.018) return bounded;
  const sign = bounded === 0
    ? (stableHash(`${seed}:quiet-direction`) % 2 === 0 ? 1 : -1)
    : Math.sign(bounded);
  return sign * (0.018 + stableUnit(`${seed}:quiet-body`) * 0.018);
}

function smoothMarketStep(value: number): number {
  const bounded = clamp(value, 0, 1);
  return bounded * bounded * (3 - 2 * bounded);
}

/**
 * Fast deterministic intrabar motion for the current candle. A slowly moving
 * segment anchor stops the contest from replaying around one fixed point, while
 * the two rhythms retain the punch of a liquid tape. Volatility is user-owned
 * on a 0–1000 scale; zero disables the live contest completely.
 */
function liveMarketPulse(
  seed: string,
  time: number,
  tick: number,
  summary: AnimationBoardSummary,
  volatility: number,
): number {
  const normalized = clamp(volatility, 0, 1_000) / 1_000;
  if (normalized === 0) return 0;

  const phase = stableUnit(`${seed}:live-phase`) * Math.PI * 2;
  const segmentLength = 7 + stableHash(`${seed}:live-segment-length`) % 5;
  const segment = Math.floor(tick / segmentLength);
  const segmentProgress = smoothMarketStep((tick % segmentLength) / segmentLength);
  const anchorFrom = stableSigned(`${seed}:live-anchor:${time}:${segment}`);
  const anchorTo = stableSigned(`${seed}:live-anchor:${time}:${segment + 1}`);
  const anchor = anchorFrom + (anchorTo - anchorFrom) * segmentProgress;
  const fastRate = 1.18 + stableUnit(`${seed}:live-fast-rate`) * 0.46;
  const slowRate = 0.39 + stableUnit(`${seed}:live-slow-rate`) * 0.31;
  const rhythm = Math.sin(tick * fastRate + phase) * 0.5
    + Math.sin(tick * slowRate + phase * 0.37) * 0.24;
  const tapeNoise = stableSigned(`${seed}:live-tape:${time}:${tick}`) * 0.09;
  const beat = 5 + stableHash(`${seed}:live-beat`) % 7;
  const impulse = tick % beat === 0
    ? stableSigned(`${seed}:live-impulse:${time}:${Math.floor(tick / beat)}`) * 0.22
    : 0;
  const contest = anchor * 0.44 + rhythm + tapeNoise + impulse;
  const amplitude = 0.025 + Math.pow(normalized, 0.72) * 0.66;

  if (summary.blocked > 0) return clamp(contest * amplitude - amplitude * 0.18, -0.82, 0.56);
  if (summary.active > 0) return clamp(contest * amplitude, -0.78, 0.78);
  if (summary.queued > 0) return clamp(contest * amplitude * 0.56, -0.42, 0.42);
  if (summary.completed > 0) return clamp(contest * amplitude * 0.32 + amplitude * 0.06, -0.24, 0.28);
  return clamp(contest * amplitude * 0.18 - amplitude * 0.025, -0.14, 0.12);
}

function dominantActivity(markers: readonly PreparedMarketMarker[]): MarketActivityKind | undefined {
  const scores = new Map<MarketActivityKind, number>();
  for (const marker of markers) {
    scores.set(marker.eventKind, (scores.get(marker.eventKind) ?? 0) + Math.abs(marker.force) + marker.energy * 0.01);
  }
  return [...scores.entries()].sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0]))[0]?.[0];
}

/**
 * Produces a deterministic event market from compact task provenance. Each event
 * contributes a bounded force window (rather than changing a cumulative score),
 * so publishing, work, delivery, setbacks, and recovery read like a liquid tape.
 */
export function buildLegacyMarketKlineScene(input: AnimationBoardInput): MarketKlineScene {
  const summary = summarizeAnimationBoard(input);
  const regime = marketRegime(summary);
  const tick = finiteTick(input.tick);
  const tasks = orderedTasks(input.tasks);
  const timeframe = input.marketTimeframe ?? "1h";
  const volatility = input.styleMetadata?.volatility ?? DEFAULT_ANIMATION_STYLE_METADATA.volatility;
  const barSeconds = MARKET_TIMEFRAME_SECONDS[timeframe];
  const projectKey = input.projectKey?.trim() || "carbon:legacy-project";
  // Background microstructure is deliberately project-scoped only. Adding an
  // event may alter its own force window, never rehash every older candle.
  const seed = projectKey;
  const events = extractMarketActivityEvents(tasks, input.status);
  const timeline = buildMarketTimeline(events, barSeconds);
  const preparedMarkers = buildPreparedMarkers(events, timeline, seed);
  const markers = preparedMarkers.map(publicMarketMarker);
  const buckets = buildMarketBuckets(preparedMarkers);
  const impacts = buildMarketImpacts(buckets, seed, barSeconds);
  const needleAt = longNeedleIndex(buckets, seed);
  const reference = marketReference(seed);
  const candles: MarketCandle[] = [];
  const openingTime = timeline.start - timeline.step;
  let price = marketBaselineLevel(seed, openingTime, timeline.step, reference);
  // This level carries only local event pressure. It decays after a wave and
  // therefore creates a settling leg rather than a one-bar reference snap.
  let carriedPressure = 0;
  let previousMove = 0;
  let lastRejectionIndex = -8;
  let rejectionWicksPrinted = 0;
  let currentLivePressure = 0;

  for (let index = 0; index < MARKET_CANDLE_COUNT; index += 1) {
    const open = price;
    const time = timeline.start + index * timeline.step;
    const direct = buckets.get(index);
    const impact = impacts[index]!;
    const bullForce = impact.bull;
    const bearForce = impact.bear;
    const isLiveCandle = index === MARKET_CANDLE_COUNT - 1;
    const activeDensity = direct ? clamp(Math.log2(direct.markers.length + 1) / 8, 0, 0.22) : 0;
    const livePulse = isLiveCandle ? liveMarketPulse(seed, time, tick, summary, volatility) : 0;
    if (isLiveCandle) currentLivePressure = livePulse;
    const hasPressure = Math.abs(impact.pressure) > 0.0001;
    const wasCarryingPressure = Math.abs(carriedPressure) > 0.002;
    const carryDecay = hasPressure ? 0.92 : 0.78;
    carriedPressure = clamp(carriedPressure * carryDecay + impact.pressure, -1.05, 1.05);
    const background = marketBaselineLevel(seed, time, timeline.step, reference);
    const targetLevel = background
      + carriedPressure
      + denseTapeMove(seed, time, timeline.step, activeDensity)
      + livePulse;
    // The tape follows a level, not a one-bar event offset. The small amount
    // of friction is the market's mean reversion: quiet bars track their
    // project baseline exactly, while a shock eases back over several bars.
    const tracking = hasPressure || wasCarryingPressure ? 0.82 : 1;
    let move = (targetLevel - open) * tracking;
    if (direct) move = constrainDirectEventMove(direct.cause, softenMarketMove(move, 0.78), `${seed}:event:${time}`);
    else if (hasPressure || wasCarryingPressure) move = softenMarketMove(move, 0.72);
    else move = readableQuietMove(move, `${seed}:${time}`);

    const close = clamp(open + move, MARKET_PRICE_FLOOR, MARKET_PRICE_CEILING);
    // Compact shadows keep task energy legible through bodies and volume. One
    // dense, high-energy cluster may print a single controlled special needle.
    const baseWick = 0.006 + stableUnit(`${seed}:wick:${time}`) * 0.016;
    const localWick = Math.min(0.055, impact.range * 0.018 + Math.abs(impact.pressure) * 0.025);
    const wickLeansUp = stableHash(`${seed}:wick-side:${time}`) % 2 === 0;
    let upperWick = clamp(baseWick * (0.62 + stableUnit(`${seed}:wick-upper:${time}`) * 0.22) + localWick * (wickLeansUp ? 0.82 : 0.24), 0.004, 0.085);
    let lowerWick = clamp(baseWick * (0.62 + stableUnit(`${seed}:wick-lower:${time}`) * 0.22) + localWick * (wickLeansUp ? 0.24 : 0.82), 0.004, 0.085);
    const reversedDirection = previousMove !== 0
      && move !== 0
      && Math.sign(previousMove) !== Math.sign(move)
      && Math.abs(previousMove) >= 0.05
      && Math.abs(move) >= 0.035;
    if (reversedDirection && rejectionWicksPrinted < 3 && index - lastRejectionIndex >= 6) {
      const rejectionWick = clamp(0.025 + (Math.abs(previousMove) + Math.abs(move)) * 0.12, 0.025, 0.075);
      if (previousMove > 0) upperWick = clamp(upperWick + rejectionWick, 0.004, 0.115);
      else lowerWick = clamp(lowerWick + rejectionWick, 0.004, 0.115);
      rejectionWicksPrinted += 1;
      lastRejectionIndex = index;
    }
    if (isLiveCandle && tick > 0) {
      const recentPulses = Array.from({ length: Math.min(4, tick) }, (_, offset) => (
        liveMarketPulse(seed, time, tick - offset - 1, summary, volatility)
      ));
      const recentHigh = Math.max(livePulse, ...recentPulses);
      const recentLow = Math.min(livePulse, ...recentPulses);
      upperWick = clamp(upperWick + Math.max(0, recentHigh - livePulse) * 0.48, 0.004, 0.115);
      lowerWick = clamp(lowerWick + Math.max(0, livePulse - recentLow) * 0.48, 0.004, 0.115);
    }
    if (index === needleAt) {
      const spike = 0.075 + stableUnit(`${seed}:high-energy-needle:${time}`) * 0.035;
      if (impact.pressure >= 0) upperWick = clamp(upperWick + spike, 0, 0.16);
      else lowerWick = clamp(lowerWick + spike, 0, 0.16);
    }
    const low = clamp(Math.min(open, close) - lowerWick, MARKET_PRICE_FLOOR + 0.02, MARKET_PRICE_CEILING - 0.02);
    const high = clamp(Math.max(open, close) + upperWick, MARKET_PRICE_FLOOR + 0.02, MARKET_PRICE_CEILING - 0.02);
    const rawEnergy = Math.max(0, impact.energy + (bullForce + bearForce) * 8);
    const energy = Math.round(clamp(
      12
        + Math.sqrt(rawEnergy) * 4.35
        + stableSigned(`${seed}:volume:${time}`) * 1.6
        + (isLiveCandle ? Math.abs(livePulse) * 26 + stableSigned(`${seed}:live-energy:${time}:${tick}`) * 1.4 : 0),
      10,
      72,
    ));
    const cause = direct?.cause ?? impact.cause ?? "quiet";
    const pattern = direct?.pattern ?? impact.pattern ?? "quiet-drift";
    candles.push({
      time,
      open,
      close,
      high,
      low,
      energy,
      cause,
      bullForce: compactForce(bullForce),
      bearForce: compactForce(bearForce),
      eventCount: direct?.markers.length ?? 0,
      pattern,
    });
    price = close;
    previousMove = move;
  }

  const energyRatio = Math.round(candles.reduce((sum, candle) => sum + candle.energy, 0) / Math.max(1, candles.length));
  return {
    kind: "market",
    timeframe,
    barSeconds,
    summary,
    regime,
    energyRatio,
    currentPrice: candles.at(-1)?.close ?? price,
    livePressure: currentLivePressure,
    candles,
    markers,
    dominantEvent: dominantActivity(preparedMarkers),
    currentPattern: candles.at(-1)?.pattern ?? "quiet-drift",
    stagnantCount: summary.stagnant,
    structuralPrice: Math.max(1, 100 - summary.stagnant * 20),
  };
}

/** The current task market uses real task events and omits empty wall-clock bars. */
export function buildMarketKlineScene(input: AnimationBoardInput): MarketKlineScene {
  const summary = summarizeAnimationBoard(input);
  return buildTaskEventMarketScene(input, summary);
}

/**
 * Renderer/style registry. Adding a scene means adding one definition here and one
 * matching visual renderer in CarbonAnimationBoard; all controls and persistence then
 * discover the style through this shared contract.
 */
export const ANIMATION_BOARD_STYLE_REGISTRY = {
  "pixel-agents": {
    id: "pixel-agents",
    label: { english: "Work floor", chinese: "工作风" },
    description: { english: "A live studio where agents and their task queues stay visible", chinese: "用经营模拟工作室呈现智能体、工位与任务流" },
    build: buildPixelAgentsScene,
  },
  "market-kline": {
    id: "market-kline",
    label: { english: "Market K-line", chinese: "行情 K 线" },
    description: { english: "Task momentum as an interactive OHLC market", chinese: "以可交互 OHLC 行情展示任务势能" },
    build: buildMarketKlineScene,
  },
} satisfies Record<AnimationBoardStyle, AnimationBoardStyleDefinition>;

export function getAnimationBoardRenderer(style: AnimationBoardStyle): AnimationBoardStyleDefinition {
  return ANIMATION_BOARD_STYLE_REGISTRY[style];
}

export function buildAnimationBoardModel(style: AnimationBoardStyle, input: AnimationBoardInput): AnimationBoardModel {
  return getAnimationBoardRenderer(style).build(input);
}
