import type { Task } from "./api.ts";
import {
  DEFAULT_ANIMATION_STYLE_METADATA,
  type MarketTimeframe,
} from "./personalization.ts";
import type {
  AnimationBoardInput,
  AnimationBoardSummary,
  AnimationTaskState,
  MarketActivityKind,
  MarketCandle,
  MarketKlineScene,
  MarketPattern,
  MarketTaskMarker,
} from "./animation-board.ts";

const REFERENCE_PRICE = 100;
const PRICE_FLOOR = 1;
const PRICE_CEILING = 120;
const MAX_EVENT_BUCKETS = 96;
const DEFAULT_STAGNATION_SECONDS = 24 * 60 * 60;

const TIMEFRAME_SECONDS = {
  "1m": 60,
  "5m": 5 * 60,
  "30m": 30 * 60,
  "1h": 60 * 60,
  "1d": 24 * 60 * 60,
} as const satisfies Record<MarketTimeframe, number>;

type TaskMarketEvent = {
  id: string;
  task: Task;
  state: AnimationTaskState;
  kind: MarketActivityKind;
  did: string;
  actor: string;
  time: number;
  synthetic?: boolean;
};

type EventBucket = {
  time: number;
  events: TaskMarketEvent[];
};

type EventProfile = {
  force: number;
  energy: number;
  pattern: MarketPattern;
};

function clamp(value: number, lower: number, upper: number): number {
  return Math.min(upper, Math.max(lower, value));
}

function stableHash(value: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

function stableUnit(value: string): number {
  return stableHash(value) / 0xffffffff;
}

function stableSigned(value: string): number {
  return stableUnit(value) * 2 - 1;
}

function normalize(value: string | undefined): string {
  return value?.trim().toLowerCase().replace(/[\s-]+/g, "_") ?? "";
}

function parseUnixSeconds(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const milliseconds = Date.parse(value);
  return Number.isFinite(milliseconds) ? Math.floor(milliseconds / 1_000) : undefined;
}

function isClosed(task: Task, closed: readonly string[] | undefined): boolean {
  const normalized = new Set((closed ?? []).map(normalize));
  return normalized.has(normalize(task.status));
}

function taskState(task: Task, closed: readonly string[] | undefined): AnimationTaskState {
  if (task.blockerReason?.trim() || normalize(task.status) === "blocked") return "blocked";
  if (isClosed(task, closed)) return "completed";
  if (task.executionState === "active" || ["active", "in_progress", "working", "doing"].includes(normalize(task.status))) return "active";
  return "queued";
}

function eventKind(did: string, closed: ReadonlySet<string>): MarketActivityKind | undefined {
  const value = normalize(did);
  if (!value || value.startsWith("read") || value.startsWith("poll")) return undefined;
  if (["heartbeat", "session_heartbeat", "reorder", "reordered", "lease_renewed", "session_lease_renewed", "lease_auto_released"].includes(value)) return undefined;
  const has = (...parts: string[]) => parts.some((part) => value.includes(part));
  if (has("unblocked", "recovered", "恢复", "解除阻塞")) return "recovered";
  if (has("blocked", "阻塞")) return "blocked";
  if (has("completed", "closed", "完成", "已关闭") || [...closed].some((state) => value.includes(state))) return "completed";
  if (has("lease_claimed", "claimed", "reassigned", "claim_approved", "认领", "领取", "重新分配", "批准认领")) return "claimed";
  if (has("created", "create", "published", "publish", "创建", "发布")) return "published";
  // Notes, check results, transitions and assignment changes are meaningful task
  // actions even when their protocol verb is unknown to this presentation layer.
  return "processing";
}

function eventPriority(kind: MarketActivityKind): number {
  if (kind === "stagnant") return 0;
  if (kind === "recovered") return 1;
  return 2;
}

function taskEvents(task: Task, input: AnimationBoardInput): TaskMarketEvent[] {
  const closed = new Set((input.status.closed ?? []).map(normalize));
  const state = taskState(task, input.status.closed);
  const actual = (task.provenance ?? []).flatMap((entry, index): TaskMarketEvent[] => {
    const time = parseUnixSeconds(entry.editedAt) ?? parseUnixSeconds(entry.at);
    const kind = eventKind(entry.did ?? "", closed);
    if (time === undefined || !kind) return [];
    return [{
      id: `${task.id}:activity:${entry.id ?? index}:${index}`,
      task,
      state,
      kind,
      did: entry.did || kind,
      actor: entry.who?.trim() || task.assignee?.trim() || "Carbon",
      time,
    }];
  }).sort((left, right) => left.time - right.time || left.id.localeCompare(right.id));

  const threshold = Math.max(1, input.status.taskStagnationAfterSeconds ?? DEFAULT_STAGNATION_SECONDS);
  const derived: TaskMarketEvent[] = [];
  for (let index = 1; index < actual.length; index += 1) {
    const previous = actual[index - 1]!;
    const next = actual[index]!;
    const stagnantTime = previous.time + threshold;
    if (stagnantTime >= next.time) continue;
    derived.push({
      id: `${task.id}:stagnant:${stagnantTime}`,
      task,
      state,
      kind: "stagnant",
      did: "activity became stagnant",
      actor: "Carbon",
      time: stagnantTime,
      synthetic: true,
    });
    if (next.kind !== "recovered") {
      derived.push({
        id: `${task.id}:activity-recovered:${next.time}`,
        task,
        state,
        kind: "recovered",
        did: "activity resumed",
        actor: next.actor,
        time: next.time,
        synthetic: true,
      });
    }
  }

  if (task.activityHealth === "stagnant" && !isClosed(task, input.status.closed)) {
    const stagnantTime = parseUnixSeconds(task.stagnantAt);
    const latestActual = actual.at(-1)?.time ?? Number.NEGATIVE_INFINITY;
    if (stagnantTime !== undefined && stagnantTime >= latestActual) {
      const duplicate = derived.some((event) => event.kind === "stagnant" && event.time === stagnantTime);
      if (!duplicate) {
        derived.push({
          id: `${task.id}:stagnant:${stagnantTime}`,
          task,
          state,
          kind: "stagnant",
          did: "activity became stagnant",
          actor: "Carbon",
          time: stagnantTime,
          synthetic: true,
        });
      }
    }
  }

  return [...actual, ...derived].sort((left, right) => (
    left.time - right.time
    || eventPriority(left.kind) - eventPriority(right.kind)
    || left.id.localeCompare(right.id)
  ));
}

function collectEvents(input: AnimationBoardInput): TaskMarketEvent[] {
  return [...input.tasks]
    .sort((left, right) => left.id.localeCompare(right.id))
    .flatMap((task) => taskEvents(task, input))
    .sort((left, right) => (
      left.time - right.time
      || eventPriority(left.kind) - eventPriority(right.kind)
      || left.id.localeCompare(right.id)
    ));
}

function bucketTime(time: number, seconds: number): number {
  return Math.floor(time / seconds) * seconds;
}

function collectBuckets(events: readonly TaskMarketEvent[], seconds: number): EventBucket[] {
  const buckets = new Map<number, TaskMarketEvent[]>();
  for (const event of events) {
    const time = bucketTime(event.time, seconds);
    const current = buckets.get(time) ?? [];
    current.push(event);
    buckets.set(time, current);
  }
  return [...buckets.entries()]
    .sort(([left], [right]) => left - right)
    .map(([time, entries]) => ({
      time,
      events: entries.sort((left, right) => (
        left.time - right.time
        || eventPriority(left.kind) - eventPriority(right.kind)
        || left.id.localeCompare(right.id)
      )),
    }));
}

function structuralPrice(stagnantCount: number): number {
  return Math.max(PRICE_FLOOR, REFERENCE_PRICE - stagnantCount * 20);
}

function eventProfile(event: TaskMarketEvent, seed: string): EventProfile {
  const variation = stableSigned(`${seed}:${event.id}:force`);
  switch (event.kind) {
    case "published":
      return { force: variation * 2.6, energy: 36, pattern: "publish-volatility" };
    case "claimed":
      return { force: variation * 0.55, energy: 18, pattern: "claim-compression" };
    case "processing":
      return { force: variation * 3.2, energy: 42, pattern: "processing-contest" };
    case "completed":
      return { force: 3.4 + Math.abs(variation) * 1.1, energy: 34, pattern: "completion-rally" };
    case "blocked":
      return { force: -(4.2 + Math.abs(variation) * 1.4), energy: 46, pattern: "blocker-selloff" };
    case "recovered":
      return { force: 20, energy: 44, pattern: "recovery-bounce" };
    case "stagnant":
      return { force: -20, energy: 52, pattern: "stagnation-plunge" };
    default:
      return { force: variation * 0.2, energy: 8, pattern: "quiet-drift" };
  }
}

function markerShape(kind: MarketActivityKind): Pick<MarketTaskMarker, "position" | "shape"> {
  switch (kind) {
    case "completed":
    case "recovered":
      return { position: "belowBar", shape: "arrowUp" };
    case "blocked":
    case "stagnant":
      return { position: "aboveBar", shape: "arrowDown" };
    case "claimed":
    case "processing":
      return { position: "belowBar", shape: "circle" };
    default:
      return { position: "aboveBar", shape: "square" };
  }
}

function applyHealthEvent(stagnant: Set<string>, event: TaskMarketEvent): void {
  if (event.kind === "stagnant") stagnant.add(event.task.id);
  else if (event.kind === "recovered") stagnant.delete(event.task.id);
}

function dominantEvent(events: readonly TaskMarketEvent[], seed: string): TaskMarketEvent | undefined {
  return [...events].sort((left, right) => {
    const force = Math.abs(eventProfile(right, seed).force) - Math.abs(eventProfile(left, seed).force);
    return force || left.id.localeCompare(right.id);
  })[0];
}

function livePulse(
  seed: string,
  tick: number,
  summary: AnimationBoardSummary,
  volatility: number,
): number {
  const normalizedVolatility = clamp(volatility, 0, 1_000) / 1_000;
  if (normalizedVolatility === 0 || (summary.active === 0 && summary.stagnant === 0)) return 0;
  const phase = stableUnit(`${seed}:live-phase`) * Math.PI * 2;
  const contest = Math.sin(tick * 1.13 + phase) * 0.58
    + Math.sin(tick * 0.47 + phase * 0.41) * 0.31
    + stableSigned(`${seed}:live:${tick}`) * 0.11;
  const amplitude = summary.active > 0
    ? 0.35 + normalizedVolatility * 3.4
    : 0.08 + normalizedVolatility * 0.72;
  return clamp(contest * amplitude, -4, 4);
}

function compact(value: number): number {
  return Math.round(value * 1_000) / 1_000;
}

function buildCandle(
  bucket: EventBucket,
  open: number,
  stagnant: Set<string>,
  seed: string,
): { candle: MarketCandle; markers: MarketTaskMarker[] } {
  const countBefore = stagnant.size;
  const structuralLevels = [structuralPrice(countBefore)];
  for (const event of bucket.events) {
    applyHealthEvent(stagnant, event);
    if (event.kind === "stagnant" || event.kind === "recovered") structuralLevels.push(structuralPrice(stagnant.size));
  }
  const countAfter = stagnant.size;
  const healthChanged = countAfter !== countBefore;
  const profiles = bucket.events.map((event) => eventProfile(event, seed));
  const ordinaryForce = bucket.events.reduce((sum, event, index) => (
    event.kind === "stagnant" || event.kind === "recovered" ? sum : sum + profiles[index]!.force
  ), 0);
  const target = structuralPrice(countAfter);
  let close = healthChanged
    ? target
    : open + (target - open) * 0.68 + clamp(ordinaryForce, -5.5, 5.5);
  close = clamp(close, PRICE_FLOOR, PRICE_CEILING);

  const dominant = dominantEvent(bucket.events, seed);
  const cause = bucket.events.length > 1 ? "mixed" : dominant?.kind ?? "quiet";
  const pattern = dominant ? eventProfile(dominant, seed).pattern : "quiet-drift";
  const bodyTop = Math.max(open, close);
  const bodyBottom = Math.min(open, close);
  const ordinaryWick = 0.08 + stableUnit(`${seed}:wick:${bucket.time}`) * 0.18;
  let low = Math.max(PRICE_FLOOR, Math.min(bodyBottom - ordinaryWick, ...structuralLevels));
  let high = Math.min(PRICE_CEILING, Math.max(bodyTop + ordinaryWick, ...structuralLevels));
  // A stagnation streak prints one unmistakable capitulation wick. Additional
  // stagnant tasks move the body down by 20 each without making a needle wall.
  if (countBefore === 0 && countAfter > 0) low = PRICE_FLOOR;
  if (countAfter < countBefore) high = Math.max(high, target + 0.35);

  const bullForce = profiles.reduce((sum, profile) => sum + Math.max(0, profile.force), 0);
  const bearForce = profiles.reduce((sum, profile) => sum + Math.max(0, -profile.force), 0);
  const energy = Math.round(clamp(
    8 + Math.sqrt(profiles.reduce((sum, profile) => sum + profile.energy, 0)) * 4 + Math.abs(countAfter - countBefore) * 4,
    8,
    58,
  ));

  const markers = bucket.events.map((event) => {
    const profile = eventProfile(event, seed);
    return {
      id: event.id,
      task: event.task,
      state: event.state,
      candleIndex: -1,
      time: bucket.time,
      ...markerShape(event.kind),
      eventKind: event.kind,
      did: event.did,
      actor: event.actor,
      force: compact(profile.force),
      energy: profile.energy,
      pattern: profile.pattern,
    } satisfies MarketTaskMarker;
  });

  return {
    candle: {
      time: bucket.time,
      open,
      close,
      high: Math.max(high, open, close),
      low: Math.min(low, open, close),
      energy,
      cause,
      bullForce: compact(bullForce),
      bearForce: compact(bearForce),
      eventCount: bucket.events.length,
      pattern,
    },
    markers,
  };
}

/**
 * Builds a compressed event tape: real task actions and derived stagnation turns are
 * bucketed by the selected period, while empty wall-clock intervals are omitted.
 */
export function buildTaskEventMarketScene(
  input: AnimationBoardInput,
  summary: AnimationBoardSummary,
): MarketKlineScene {
  const timeframe = input.marketTimeframe ?? "1h";
  const barSeconds = TIMEFRAME_SECONDS[timeframe];
  const projectKey = input.projectKey?.trim() || "carbon:legacy-project";
  const allBuckets = collectBuckets(collectEvents(input), barSeconds);
  const visibleBuckets = allBuckets.slice(-MAX_EVENT_BUCKETS);
  const visibleTimes = new Set(visibleBuckets.map((bucket) => bucket.time));
  const stagnant = new Set<string>();

  // Preserve the health structure that existed before the visible 96-event window.
  for (const bucket of allBuckets) {
    if (visibleTimes.has(bucket.time)) break;
    for (const event of bucket.events) applyHealthEvent(stagnant, event);
  }

  const candles: MarketCandle[] = [];
  const markers: MarketTaskMarker[] = [];
  let price = structuralPrice(stagnant.size);
  for (const bucket of visibleBuckets) {
    const built = buildCandle(bucket, price, stagnant, projectKey);
    const candleIndex = candles.length;
    candles.push(built.candle);
    markers.push(...built.markers.map((marker) => ({ ...marker, candleIndex })));
    price = built.candle.close;
  }

  const structural = structuralPrice(summary.stagnant);
  let currentLivePressure = 0;
  if (candles.length > 0) {
    const lastIndex = candles.length - 1;
    const last = candles[lastIndex]!;
    const volatility = input.styleMetadata?.volatility ?? DEFAULT_ANIMATION_STYLE_METADATA.volatility;
    currentLivePressure = livePulse(projectKey, Number.isFinite(input.tick) ? Math.trunc(input.tick) : 0, summary, volatility);
    const close = clamp(last.close + currentLivePressure, PRICE_FLOOR, PRICE_CEILING);
    candles[lastIndex] = {
      ...last,
      close,
      high: Math.max(last.high, close),
      low: Math.min(last.low, close),
      energy: Math.round(clamp(last.energy + Math.abs(currentLivePressure) * 3, 8, 58)),
    };
    price = close;
  } else {
    price = structural;
  }

  const dominant = dominantEvent(visibleBuckets.at(-1)?.events ?? [], projectKey);
  const energyRatio = candles.length === 0
    ? 0
    : Math.round(candles.reduce((sum, candle) => sum + candle.energy, 0) / candles.length);
  const regime = summary.stagnant > 0
    ? "stagnant"
    : summary.blocked > 0
      ? "blocked"
      : summary.allInProgress
        ? "all-active"
        : summary.completed > 0
          ? "success"
          : "idle";

  return {
    kind: "market",
    timeframe,
    barSeconds,
    summary,
    regime,
    energyRatio,
    currentPrice: price,
    livePressure: currentLivePressure,
    candles,
    markers,
    dominantEvent: dominant?.kind,
    currentPattern: candles.at(-1)?.pattern ?? "quiet-drift",
    stagnantCount: summary.stagnant,
    structuralPrice: structural,
  };
}
