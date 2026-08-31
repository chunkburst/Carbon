import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type CSSProperties,
  type KeyboardEvent,
  type ReactElement,
} from "react";
import {
  Activity,
  ArrowUpRight,
  BarChart3,
  Bot,
  CandlestickChart,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  CircleDotDashed,
  Clock3,
  Hand,
  ListFilter,
  Megaphone,
  Minus,
  RotateCcw,
  SlidersHorizontal,
  Sparkles,
  Swords,
} from "lucide-react";
import { CarbonPixelBoard } from "@/components/CarbonPixelBoard";
import {
  CandlestickSeries,
  ColorType,
  CrosshairMode,
  HistogramSeries,
  LineStyle,
  createChart,
  createSeriesMarkers,
  type IChartApi,
  type ISeriesApi,
  type MouseEventParams,
  type AutoscaleInfoProvider,
  type SeriesMarker,
  type Time,
  type UTCTimestamp,
} from "lightweight-charts";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Popover,
  PopoverAnchor,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Field, FieldContent, FieldDescription, FieldGroup, FieldTitle } from "@/components/ui/field";
import { Slider } from "@/components/ui/slider";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  buildAnimationBoardModel,
  getAnimationBoardRenderer,
  type AnimationBoardModel,
  type AnimationBoardSummary,
  type AnimationTaskState,
  type MarketActivityKind,
  type MarketCandle,
  type MarketKlineScene,
  type MarketPattern,
  type MarketTaskMarker,
} from "@/lib/animation-board";
import { useI18n } from "@/lib/i18n";
import {
  ANIMATION_SPEED_MAX,
  ANIMATION_SPEED_MIN,
  ANIMATION_VOLATILITY_MAX,
  ANIMATION_VOLATILITY_MIN,
  DEFAULT_ANIMATION_STYLE_METADATA,
  getAnimationBoardStyle,
  getAnimationStyleMetadata,
  getMarketTimeframe,
  PERSONALIZATION_EVENT,
  setAnimationStyleMetadata,
  setMarketTimeframe,
  type AnimationBoardStyle,
  type AnimationStyleMetadata,
  type MarketTimeframe,
} from "@/lib/personalization";
import { cn } from "@/lib/utils";
import type { Status, Task } from "@/lib/api";
import "./CarbonAnimationBoard.css";

type CarbonAnimationBoardProps = {
  /** Stable project/scope identity keeps otherwise identical boards independent. */
  projectKey: string;
  tasks: Task[];
  status: Status;
  /** When supplied, the surrounding board toolbar owns the active animation style. */
  style?: AnimationBoardStyle;
  /** Keeps the same scene semantics inside the movable mini-board. */
  compact?: boolean;
  onOpenTask: (task: Task) => void;
  prefersReducedMotion: boolean;
};

type AnimationSceneRendererProps = {
  model: AnimationBoardModel;
  marketTimeframe: MarketTimeframe;
  onMarketTimeframeChange: (value: MarketTimeframe) => void;
  styleMetadata: AnimationStyleMetadata;
  onStyleMetadataChange: (value: AnimationStyleMetadata) => void;
  onOpenTask: (task: Task) => void;
  compact: boolean;
  prefersReducedMotion: boolean;
};

type MarketHover = {
  candle: MarketCandle;
  markers: MarketTaskMarker[];
};

type MarketEventChip = {
  marker: MarketTaskMarker;
  taskCount: number;
  left: number;
  top: number;
  side: "above" | "below";
};

type MarketPreviewPoint = {
  left: number;
  top: number;
};

type ChartPalette = {
  surface: string;
  text: string;
  muted: string;
  border: string;
  grid: string;
  crosshair: string;
  up: string;
  down: string;
  volumeUp: string;
  volumeDown: string;
  waiting: string;
  active: string;
};

type MarketChartRefs = {
  chart: IChartApi;
  candles: ISeriesApi<"Candlestick", Time>;
  volume: ISeriesApi<"Histogram", Time>;
  markers: { setMarkers: (markers: SeriesMarker<Time>[]) => void };
};

/** Scene registry stays paired with the pure model registry in animation-board.ts. */
const ANIMATION_BOARD_SCENE_RENDERERS: Record<AnimationBoardStyle, ComponentType<AnimationSceneRendererProps>> = {
  "pixel-agents": PixelAgentsRenderer,
  "market-kline": MarketKlineRenderer,
};

function useAnimationTick(prefersReducedMotion: boolean, speed: number): number {
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (prefersReducedMotion) return undefined;
    const boundedSpeed = Math.min(ANIMATION_SPEED_MAX, Math.max(ANIMATION_SPEED_MIN, speed));
    const cadence = Math.min(2_400, Math.max(180, Math.round(580 * 100 / boundedSpeed)));
    const interval = window.setInterval(() => setTick((current) => current + 1), cadence);
    return () => window.clearInterval(interval);
  }, [prefersReducedMotion, speed]);

  return tick;
}

function AnimationStyleControls({
  style,
  metadata,
  onChange,
  compact = false,
}: {
  style: AnimationBoardStyle;
  metadata: AnimationStyleMetadata;
  onChange: (value: AnimationStyleMetadata) => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  const speedLabel = `${(metadata.speed / 100).toFixed(2).replace(/0+$/, "").replace(/\.$/, "")}×`;
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          size={compact ? "icon-xs" : "xs"}
          variant="ghost"
          aria-label={t("Animation tuning", "动画参数")}
          title={t("Animation tuning", "动画参数")}
        >
          <SlidersHorizontal data-icon={compact ? undefined : "inline-start"} />
          {!compact && t("Tune", "参数")}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="carbon-animation-tuning">
        <div className="carbon-animation-tuning-heading">
          <div>
            <strong>{t("Animation tuning", "动画参数")}</strong>
            <span>{t("Saved only for this project and style.", "仅保存到当前项目与当前风格。")}</span>
          </div>
          <Button
            type="button"
            size="xs"
            variant="ghost"
            onClick={() => onChange({ ...DEFAULT_ANIMATION_STYLE_METADATA })}
          >
            {t("Reset", "重置")}
          </Button>
        </div>
        <FieldGroup className="gap-4">
          <Field>
            <div className="carbon-animation-tuning-label">
              <FieldContent>
                <FieldTitle>{t("Playback speed", "动画速度")}</FieldTitle>
                <FieldDescription>{t("Changes movement and live market cadence.", "控制角色动作与实时行情推进节奏。")}</FieldDescription>
              </FieldContent>
              <output>{speedLabel}</output>
            </div>
            <Slider
              value={[metadata.speed]}
              min={ANIMATION_SPEED_MIN}
              max={ANIMATION_SPEED_MAX}
              step={5}
              aria-label={t("Playback speed", "动画速度")}
              onValueChange={(value) => onChange({ ...metadata, speed: value[0] ?? metadata.speed })}
            />
          </Field>
          <Field>
            <div className="carbon-animation-tuning-label">
              <FieldContent>
                <FieldTitle>{style === "market-kline" ? t("Battle range", "博弈幅度") : t("Floor activity", "现场活跃度")}</FieldTitle>
                <FieldDescription>
                  {style === "market-kline"
                    ? t("0 is still; 1000 allows the strongest live contest.", "0 为静止，1000 允许最强实时博弈。")
                    : t("Controls roaming distance and ambient task energy.", "控制角色巡游距离与现场任务能量。")}
                </FieldDescription>
              </FieldContent>
              <output>{metadata.volatility}</output>
            </div>
            <Slider
              value={[metadata.volatility]}
              min={ANIMATION_VOLATILITY_MIN}
              max={ANIMATION_VOLATILITY_MAX}
              step={10}
              aria-label={style === "market-kline" ? t("Battle range", "博弈幅度") : t("Floor activity", "现场活跃度")}
              onValueChange={(value) => onChange({ ...metadata, volatility: value[0] ?? metadata.volatility })}
            />
          </Field>
        </FieldGroup>
      </PopoverContent>
    </Popover>
  );
}

function taskStateLabel(state: AnimationTaskState, t: ReturnType<typeof useI18n>["t"]): string {
  switch (state) {
    case "active":
      return t("Working", "工作中");
    case "completed":
      return t("Completed", "已完成");
    case "blocked":
      return t("Blocked", "受阻");
    default:
      return t("Waiting", "待处理");
  }
}

function marketRegimeLabel(regime: MarketKlineScene["regime"], t: ReturnType<typeof useI18n>["t"]): string {
  switch (regime) {
    case "success":
      return t("Deliveries are lifting the market", "交付正在带动上涨");
    case "blocked":
      return t("Blockers are weighing on progress", "阻塞正在拖慢进度");
    case "stagnant":
      return t("Stagnant tasks are pressing the baseline", "停滞任务正在压低基准");
    case "all-active":
      return t("Work is moving on several fronts", "多项任务正在推进");
    default:
      return t("A quiet stretch", "暂时没有新动作");
  }
}

function summaryLabel(summary: AnimationBoardSummary, t: ReturnType<typeof useI18n>["t"]): string {
  return t(
    "{total} tasks: {active} working, {completed} completed, {blocked} blocked, {stagnant} stagnant, {queued} waiting.",
    "共 {total} 个任务：{active} 个工作中，{completed} 个已完成，{blocked} 个受阻，{stagnant} 个停滞，{queued} 个待处理。",
    {
      total: summary.total,
      active: summary.active,
      completed: summary.completed,
      blocked: summary.blocked,
      stagnant: summary.stagnant,
      queued: summary.queued,
    },
  );
}

function stateBadgeVariant(state: AnimationTaskState): "secondary" | "destructive" | "outline" {
  if (state === "blocked") return "destructive";
  if (state === "queued") return "outline";
  return "secondary";
}

function markerTaskGroups(markers: readonly MarketTaskMarker[]): Map<number, MarketTaskMarker[]> {
  const grouped = new Map<number, MarketTaskMarker[]>();
  for (const marker of markers) {
    const existing = grouped.get(marker.time) ?? [];
    existing.push(marker);
    grouped.set(marker.time, existing);
  }
  for (const group of grouped.values()) {
    group.sort((left, right) => marketMarkerPriority(right) - marketMarkerPriority(left) || left.id.localeCompare(right.id));
  }
  return grouped;
}

function resolveChartToken(token: string, opacity = 1): string {
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const context = canvas.getContext("2d");
  if (!context) return "rgb(0 0 0)";
  const value = getComputedStyle(document.documentElement).getPropertyValue(token).trim() || "rgb(0 0 0)";
  context.fillStyle = value;
  context.fillRect(0, 0, 1, 1);
  const [red, green, blue, alpha] = context.getImageData(0, 0, 1, 1).data;
  return `rgba(${red}, ${green}, ${blue}, ${(alpha / 255) * opacity})`;
}

function readChartPalette(): ChartPalette {
  return {
    surface: resolveChartToken("--card"),
    text: resolveChartToken("--foreground"),
    muted: resolveChartToken("--muted-foreground"),
    border: resolveChartToken("--border"),
    grid: resolveChartToken("--border", 0.22),
    crosshair: resolveChartToken("--muted-foreground", 0.44),
    up: resolveChartToken("--success"),
    down: resolveChartToken("--destructive"),
    volumeUp: resolveChartToken("--success", 0.28),
    volumeDown: resolveChartToken("--destructive", 0.26),
    waiting: resolveChartToken("--warning"),
    active: resolveChartToken("--brand"),
  };
}

function markerColor(marker: MarketTaskMarker, palette: ChartPalette): string {
  switch (marker.eventKind) {
    case "completed":
    case "recovered":
      return palette.up;
    case "blocked":
    case "stagnant":
      return palette.down;
    case "processing":
    case "claimed":
      return palette.active;
    case "published":
      return palette.waiting;
    default:
      return palette.muted;
  }
}

function marketMarkerPriority(marker: MarketTaskMarker): number {
  switch (marker.eventKind) {
    case "blocked":
      return 7;
    case "stagnant":
      return 8;
    case "recovered":
      return 6;
    case "completed":
      return 5;
    case "published":
      return 4;
    case "processing":
      return 3;
    case "claimed":
      return 2;
    default:
      return 1;
  }
}

function visibleMarketMarkers(
  markers: readonly MarketTaskMarker[],
  selectedMarkerId: string | undefined,
  compact: boolean,
): MarketTaskMarker[] {
  // Chips carry the provenance now. Series markers are only a quiet visual cue.
  const limit = compact ? 1 : 2;
  const selected = selectedMarkerId ? markers.find((marker) => marker.id === selectedMarkerId) : undefined;
  const prioritized = [...markers].sort((left, right) => (
    marketMarkerPriority(right) - marketMarkerPriority(left)
    || right.candleIndex - left.candleIndex
    || left.task.id.localeCompare(right.task.id)
  ));
  const chosen: MarketTaskMarker[] = [];
  const seen = new Set<string>();
  for (const marker of selected ? [selected, ...prioritized] : prioritized) {
    const lane = `${marker.candleIndex}`;
    if (seen.has(lane)) continue;
    seen.add(lane);
    chosen.push(marker);
    if (chosen.length >= limit) break;
  }
  return chosen.sort((left, right) => left.candleIndex - right.candleIndex);
}

function compressMarketVolume(energy: number): number {
  // Keep the raw energy in the readout. The pane is only a relative texture, so
  // lift quiet bars and tame the biggest bursts without flattening their order.
  const normalized = Math.min(1, Math.max(0, energy / 100));
  return 6 + Math.pow(normalized, 0.62) * 30;
}

const stableMarketAutoscaleInfo: AutoscaleInfoProvider = (baseImplementation) => {
  const base = baseImplementation();
  const range = base?.priceRange;
  const minimum = range?.minValue ?? 99;
  const maximum = range?.maxValue ?? 101;
  const rawSpan = Math.max(0.01, maximum - minimum);
  const padding = Math.max(0.16, rawSpan * 0.08);
  return {
    priceRange: {
      minValue: Math.max(0.5, minimum - padding),
      maxValue: maximum + padding,
    },
  };
};

function setMarketSeriesData(refs: MarketChartRefs, scene: MarketKlineScene, palette: ChartPalette): void {
  refs.candles.setData(scene.candles.map((candle) => ({
    time: candle.time as UTCTimestamp,
    open: candle.open,
    high: candle.high,
    low: candle.low,
    close: candle.close,
  })));
  refs.volume.setData(scene.candles.map((candle) => ({
    time: candle.time as UTCTimestamp,
    value: compressMarketVolume(candle.energy),
    color: candle.close >= candle.open ? palette.volumeUp : palette.volumeDown,
  })));
}

function setMarketChartMarkers(
  refs: MarketChartRefs,
  markers: readonly MarketTaskMarker[],
  selectedMarkerId: string | undefined,
  palette: ChartPalette,
  compact: boolean,
): void {
  refs.markers.setMarkers(visibleMarketMarkers(markers, selectedMarkerId, compact).map((marker) => ({
    id: marker.id,
    time: marker.time as UTCTimestamp,
    position: marker.position,
    // The chart markers deliberately stay secondary to the labelled event chips.
    shape: "circle",
    color: markerColor(marker, palette),
    size: marker.id === selectedMarkerId ? (compact ? 0.5 : 0.58) : (compact ? 0.34 : 0.42),
  })));
}

function marketMarkerChartPoint(
  refs: MarketChartRefs,
  scene: MarketKlineScene,
  marker: MarketTaskMarker,
): MarketPreviewPoint | null {
  const candle = scene.candles[marker.candleIndex];
  if (!candle) return null;
  const spread = Math.max(0.08, candle.high - candle.low);
  const anchorPrice = marker.position === "aboveBar"
    ? candle.high + spread * 0.12
    : candle.low - spread * 0.12;
  const left = refs.chart.timeScale().timeToCoordinate(marker.time as UTCTimestamp);
  const top = refs.candles.priceToCoordinate(anchorPrice);
  if (left === null || top === null || !Number.isFinite(left) || !Number.isFinite(top)) return null;
  return { left, top };
}

function sameCandle(left: MarketCandle, right: MarketCandle): boolean {
  return left.time === right.time
    && left.open === right.open
    && left.high === right.high
    && left.low === right.low
    && left.close === right.close
    && left.energy === right.energy;
}

function sameMarkers(left: readonly MarketTaskMarker[], right: readonly MarketTaskMarker[]): boolean {
  return left.length === right.length && left.every((marker, index) => {
    const next = right[index];
    return marker.id === next?.id
      && marker.task.id === next.task.id
      && marker.state === next.state
      && marker.time === next.time
      && marker.candleIndex === next.candleIndex
      && marker.eventKind === next.eventKind
      && marker.did === next.did
      && marker.actor === next.actor
      && marker.force === next.force
      && marker.energy === next.energy
      && marker.pattern === next.pattern;
  });
}

function sameCandleTimeline(previous: MarketKlineScene, next: MarketKlineScene): boolean {
  return previous.candles.length === next.candles.length
    && previous.candles.every((candle, index) => candle.time === next.candles[index]?.time);
}

function canUpdateOnlyLiveCandle(previous: MarketKlineScene, next: MarketKlineScene): boolean {
  if (previous.candles.length !== next.candles.length || !sameMarkers(previous.markers, next.markers)) return false;
  return previous.candles.slice(0, -1).every((candle, index) => sameCandle(candle, next.candles[index]));
}

function updateLiveCandle(refs: MarketChartRefs, candle: MarketCandle, palette: ChartPalette): void {
  refs.candles.update({
    time: candle.time as UTCTimestamp,
    open: candle.open,
    high: candle.high,
    low: candle.low,
    close: candle.close,
  });
  refs.volume.update({
    time: candle.time as UTCTimestamp,
    value: compressMarketVolume(candle.energy),
    color: candle.close >= candle.open ? palette.volumeUp : palette.volumeDown,
  });
}

function interpolateMarketCandle(from: MarketCandle, to: MarketCandle, progress: number): MarketCandle {
  const eased = 1 - Math.pow(1 - progress, 3);
  const interpolate = (start: number, end: number) => start + (end - start) * eased;
  const open = interpolate(from.open, to.open);
  const close = interpolate(from.close, to.close);
  return {
    ...to,
    open,
    close,
    high: Math.max(open, close, interpolate(from.high, to.high)),
    low: Math.min(open, close, interpolate(from.low, to.low)),
    energy: interpolate(from.energy, to.energy),
  };
}

function interpolateMarketCandles(previous: MarketKlineScene, next: MarketKlineScene, progress: number): MarketKlineScene {
  return {
    ...next,
    candles: next.candles.map((candle, index) => {
      const before = previous.candles[index];
      return before ? interpolateMarketCandle(before, candle, progress) : candle;
    }),
  };
}

function interpolateEventTimeline(previous: MarketKlineScene, next: MarketKlineScene, progress: number): MarketKlineScene {
  const previousByTime = new Map(previous.candles.map((candle) => [candle.time, candle]));
  const previousClose = previous.candles.at(-1)?.close ?? 100;
  return {
    ...next,
    candles: next.candles.map((candle) => {
      const before = previousByTime.get(candle.time);
      if (before) return interpolateMarketCandle(before, candle, progress);
      const collapsed: MarketCandle = {
        ...candle,
        open: previousClose,
        close: previousClose,
        high: previousClose,
        low: previousClose,
        energy: 0,
      };
      return interpolateMarketCandle(collapsed, candle, progress);
    }),
  };
}

function marketActivityLabel(kind: MarketActivityKind, t: ReturnType<typeof useI18n>["t"]): string {
  switch (kind) {
    case "published":
      return t("A new task entered", "新任务入场");
    case "claimed":
      return t("An agent picked it up", "智能体已接手");
    case "processing":
      return t("Work is moving", "任务正在推进");
    case "completed":
      return t("Delivery landed", "交付已完成");
    case "blocked":
      return t("Work hit a blocker", "任务被卡住了");
    case "stagnant":
      return t("Task became stagnant", "任务进入停滞");
    case "recovered":
      return t("Work started moving again", "任务重新动起来了");
    default:
      return t("No new moves", "暂无新动作");
  }
}

function marketPatternLabel(pattern: MarketPattern, t: ReturnType<typeof useI18n>["t"]): string {
  switch (pattern) {
    case "publish-volatility":
      return t("The market is finding direction", "行情正在寻找方向");
    case "claim-compression":
      return t("Volatility is settling", "波动开始收敛");
    case "processing-contest":
      return t("Task is blocked", "任务受阻");
    case "completion-rally":
      return t("Delivery added momentum", "交付带来上行动能");
    case "blocker-selloff":
      return t("The blocker added pressure", "阻塞带来下行压力");
    case "stagnation-plunge":
      return t("Stagnation pulled the baseline down", "停滞触发基准下移");
    case "recovery-bounce":
      return t("Recovery brought a rebound", "恢复带来反弹");
    case "quiet-drift":
      return t("A gentle idle drift", "行情轻轻摆动");
    default:
      return t("Several things moved together", "几件事同时发生");
  }
}

function MarketActivityGlyph({ kind }: { kind: MarketActivityKind }) {
  switch (kind) {
    case "published":
      return <Megaphone />;
    case "claimed":
      return <Hand />;
    case "processing":
      return <Activity />;
    case "completed":
      return <CheckCircle2 />;
    case "blocked":
      return <CircleAlert />;
    case "stagnant":
      return <Clock3 />;
    case "recovered":
      return <RotateCcw />;
    default:
      return <Minus />;
  }
}

function marketActivityBadgeVariant(kind: MarketActivityKind): "secondary" | "destructive" | "outline" {
  if (kind === "blocked" || kind === "stagnant") return "destructive";
  if (kind === "quiet" || kind === "claimed") return "outline";
  return "secondary";
}

function taskIsBlocked(task: Task): boolean {
  const status = task.status.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const execution = task.executionState?.trim().toLowerCase().replace(/[\s-]+/g, "_");
  return status === "blocked" || status === "stalled" || execution === "blocked" || execution === "stalled";
}

function markerActivityLabel(marker: MarketTaskMarker, t: ReturnType<typeof useI18n>["t"]): string {
  if (marker.eventKind === "stagnant") return marketActivityLabel("stagnant", t);
  return taskIsBlocked(marker.task) ? t("Task is blocked", "任务受阻") : marketActivityLabel(marker.eventKind, t);
}

function MarketTaskPreviewCard({
  marker,
  onOpenTask,
}: {
  marker: MarketTaskMarker;
  onOpenTask: (task: Task) => void;
}) {
  const { t } = useI18n();
  const assignee = marker.task.assignee || marker.actor || t("Unassigned", "未认领");
  return (
    <Card size="sm" className="carbon-market-task-preview-card">
      <CardHeader>
        <CardDescription className="carbon-market-preview-event">
          <MarketActivityGlyph kind={marker.eventKind} />
          {markerActivityLabel(marker, t)}
        </CardDescription>
        <CardTitle className="carbon-market-preview-title">{marker.task.title}</CardTitle>
        <CardDescription className="carbon-market-preview-id">{marker.task.id}</CardDescription>
      </CardHeader>
      <CardContent className="carbon-market-preview-facts">
        <div>
          <span>{t("Status", "状态")}</span>
          <Badge variant={stateBadgeVariant(marker.state)}>{taskStateLabel(marker.state, t)}</Badge>
        </div>
        <div>
          <span>{t("Owner", "负责人")}</span>
          <strong title={assignee}>{assignee}</strong>
        </div>
        <div>
          <span>{t("Energy", "量能")}</span>
          <strong>{marker.energy}</strong>
        </div>
      </CardContent>
      <CardFooter className="carbon-market-preview-footer">
        <Button type="button" size="sm" variant="secondary" className="carbon-market-preview-open" onClick={() => onOpenTask(marker.task)}>
          {t("Open task", "打开任务")}<ArrowUpRight data-icon="inline-end" />
        </Button>
      </CardFooter>
    </Card>
  );
}

function MarketTaskPreviewPopover({
  marker,
  onOpenTask,
  children,
  side = "top",
  align = "center",
}: {
  marker: MarketTaskMarker;
  onOpenTask: (task: Task) => void;
  children: ReactElement;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>{children}</PopoverTrigger>
      <PopoverContent side={side} align={align} className="carbon-market-task-preview p-0">
        <MarketTaskPreviewCard marker={marker} onOpenTask={onOpenTask} />
      </PopoverContent>
    </Popover>
  );
}

function MarketChartTaskPreview({
  marker,
  point,
  onOpenTask,
  onClose,
}: {
  marker: MarketTaskMarker;
  point: MarketPreviewPoint;
  onOpenTask: (task: Task) => void;
  onClose: () => void;
}) {
  return (
    <Popover open onOpenChange={(open) => { if (!open) onClose(); }}>
      <PopoverAnchor asChild>
        <span className="carbon-market-preview-anchor" style={{ left: point.left, top: point.top }} aria-hidden />
      </PopoverAnchor>
      <PopoverContent
        side={marker.position === "aboveBar" ? "bottom" : "top"}
        align="center"
        className="carbon-market-task-preview p-0"
      >
        <MarketTaskPreviewCard marker={marker} onOpenTask={onOpenTask} />
      </PopoverContent>
    </Popover>
  );
}

function formatPrice(value: number): string {
  return value.toFixed(2);
}

function formatMarketTime(time: number, locale: "en-US" | "zh-CN", timeframe: MarketTimeframe): string {
  const date = new Date(time * 1_000);
  if (timeframe === "1d") {
    return new Intl.DateTimeFormat(locale, {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "numeric",
      day: "numeric",
    }).format(date);
  }
  return new Intl.DateTimeFormat(locale, {
    timeZone: "Asia/Shanghai",
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

function chartTimeDate(time: Time): Date {
  if (typeof time === "number") return new Date(Number(time) * 1_000);
  if (typeof time === "string") return new Date(time);
  return new Date(Date.UTC(time.year, time.month - 1, time.day));
}

function formatBeijingChartTime(
  time: Time,
  locale: "en-US" | "zh-CN",
  timeframe: MarketTimeframe,
  axis: boolean,
): string {
  const date = chartTimeDate(time);
  if (timeframe === "1d") {
    return new Intl.DateTimeFormat(locale, {
      timeZone: "Asia/Shanghai",
      ...(axis ? {} : { year: "numeric" as const }),
      month: "numeric",
      day: "numeric",
    }).format(date);
  }
  return new Intl.DateTimeFormat(locale, {
    timeZone: "Asia/Shanghai",
    ...(axis ? {} : { month: "numeric" as const, day: "numeric" as const }),
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

function marketTimeframeLabel(timeframe: MarketTimeframe, t: ReturnType<typeof useI18n>["t"]): string {
  switch (timeframe) {
    case "1m":
      return t("Minute", "分钟");
    case "5m":
      return t("5 min", "5分钟");
    case "30m":
      return t("30 min", "30分钟");
    case "1h":
      return t("Hour", "小时");
    case "1d":
      return t("Day", "日");
  }
}

function useChartThemeVersion(): number {
  const [version, setVersion] = useState(0);

  useEffect(() => {
    const observer = new MutationObserver(() => setVersion((current) => current + 1));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class", "style"] });
    return () => observer.disconnect();
  }, []);

  return version;
}

export function CarbonAnimationBoard({
  projectKey,
  tasks,
  status,
  style: controlledStyle,
  compact = false,
  onOpenTask,
  prefersReducedMotion,
}: CarbonAnimationBoardProps) {
  const { t } = useI18n();
  const summaryId = useId();
  const [storedStyle, setStoredStyle] = useState<AnimationBoardStyle>(getAnimationBoardStyle);
  const activeStyle = controlledStyle ?? storedStyle;
  const renderer = getAnimationBoardRenderer(activeStyle);
  const [metadataPreference, setMetadataPreference] = useState(() => ({
    projectKey,
    style: activeStyle,
    value: getAnimationStyleMetadata(projectKey, activeStyle),
  }));
  const styleMetadata = metadataPreference.projectKey === projectKey && metadataPreference.style === activeStyle
    ? metadataPreference.value
    : getAnimationStyleMetadata(projectKey, activeStyle);
  const tick = useAnimationTick(prefersReducedMotion, styleMetadata.speed);
  const [marketPreference, setMarketPreference] = useState(() => ({
    projectKey,
    value: getMarketTimeframe(projectKey),
  }));
  const marketTimeframe = marketPreference.projectKey === projectKey
    ? marketPreference.value
    : getMarketTimeframe(projectKey);
  const changeMarketTimeframe = useCallback((value: MarketTimeframe) => {
    setMarketTimeframe(projectKey, value);
    setMarketPreference({ projectKey, value });
  }, [projectKey]);
  const changeStyleMetadata = useCallback((value: AnimationStyleMetadata) => {
    setAnimationStyleMetadata(projectKey, activeStyle, value);
    setMetadataPreference({ projectKey, style: activeStyle, value });
  }, [activeStyle, projectKey]);
  const model = useMemo(
    () => buildAnimationBoardModel(activeStyle, { projectKey, tasks, status, tick, marketTimeframe, styleMetadata }),
    [activeStyle, marketTimeframe, projectKey, status, styleMetadata, tasks, tick],
  );
  const SceneRenderer = ANIMATION_BOARD_SCENE_RENDERERS[activeStyle];

  useEffect(() => {
    if (controlledStyle) return undefined;
    const syncStyle = () => setStoredStyle(getAnimationBoardStyle());
    window.addEventListener(PERSONALIZATION_EVENT, syncStyle);
    window.addEventListener("storage", syncStyle);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncStyle);
      window.removeEventListener("storage", syncStyle);
    };
  }, [controlledStyle]);

  useEffect(() => {
    const syncTimeframe = () => setMarketPreference({ projectKey, value: getMarketTimeframe(projectKey) });
    syncTimeframe();
    window.addEventListener(PERSONALIZATION_EVENT, syncTimeframe);
    window.addEventListener("storage", syncTimeframe);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncTimeframe);
      window.removeEventListener("storage", syncTimeframe);
    };
  }, [projectKey]);

  useEffect(() => {
    const syncMetadata = () => setMetadataPreference({
      projectKey,
      style: activeStyle,
      value: getAnimationStyleMetadata(projectKey, activeStyle),
    });
    syncMetadata();
    window.addEventListener(PERSONALIZATION_EVENT, syncMetadata);
    window.addEventListener("storage", syncMetadata);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncMetadata);
      window.removeEventListener("storage", syncMetadata);
    };
  }, [activeStyle, projectKey]);

  return (
    <section
      aria-label={t("Visual task board", "可视化任务看板")}
      aria-describedby={summaryId}
      className={cn(
        "carbon-animation-board flex h-full min-h-0 w-full flex-1 flex-col gap-2 px-3 py-2 sm:px-4",
        compact && "carbon-animation-board-compact",
      )}
      data-style={activeStyle}
      data-reduced-motion={prefersReducedMotion ? "true" : "false"}
    >
      <p id={summaryId} className="sr-only" aria-live="polite">
        {summaryLabel(model.summary, t)}
      </p>
      {!compact && (
        <header className="flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card px-3 py-2 shadow-sm">
          <div className="flex min-w-0 items-center gap-2">
            <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-brand/10 text-brand">
              {activeStyle === "pixel-agents" ? <Bot className="size-4" /> : <CandlestickChart className="size-4" />}
            </span>
            <div className="min-w-0">
              <p className="truncate text-sm font-medium">{t(renderer.label.english, renderer.label.chinese)}</p>
              <p className="truncate text-xs text-muted-foreground">{t(renderer.description.english, renderer.description.chinese)}</p>
            </div>
          </div>
          <Badge variant="outline"><Sparkles data-icon="inline-start" />{t("Switch styles from the Board toolbar", "可在看板工具栏切换风格")}</Badge>
        </header>
      )}

      <div className="carbon-animation-summary flex flex-wrap items-center gap-1.5" aria-label={summaryLabel(model.summary, t)}>
        <Badge variant="secondary"><Activity data-icon="inline-start" />{t("Working", "工作中")} {model.summary.active}</Badge>
        <Badge variant="secondary"><CheckCircle2 data-icon="inline-start" />{t("Completed", "已完成")} {model.summary.completed}</Badge>
        <Badge variant={model.summary.blocked > 0 ? "destructive" : "secondary"}><CircleAlert data-icon="inline-start" />{t("Blocked", "受阻")} {model.summary.blocked}</Badge>
        <Badge variant={model.summary.stagnant > 0 ? "destructive" : "secondary"}><Clock3 data-icon="inline-start" />{t("Stagnant", "停滞")} {model.summary.stagnant}</Badge>
        <Badge variant="outline"><Clock3 data-icon="inline-start" />{t("Waiting", "待处理")} {model.summary.queued}</Badge>
      </div>

      <SceneRenderer
        model={model}
        marketTimeframe={marketTimeframe}
        onMarketTimeframeChange={changeMarketTimeframe}
        styleMetadata={styleMetadata}
        onStyleMetadataChange={changeStyleMetadata}
        onOpenTask={onOpenTask}
        compact={compact}
        prefersReducedMotion={prefersReducedMotion}
      />
    </section>
  );
}

function PixelAgentsRenderer({
  model,
  onOpenTask,
  compact,
  prefersReducedMotion,
  styleMetadata,
  onStyleMetadataChange,
}: AnimationSceneRendererProps) {
  if (model.kind !== "pixel") return null;
  return (
    <CarbonPixelBoard
      scene={model}
      onOpenTask={onOpenTask}
      compact={compact}
      prefersReducedMotion={prefersReducedMotion}
      metadata={styleMetadata}
      controls={(
        <AnimationStyleControls
          style="pixel-agents"
          metadata={styleMetadata}
          onChange={onStyleMetadataChange}
          compact={compact}
        />
      )}
    />
  );
}

function MarketKlineRenderer({
  model,
  marketTimeframe,
  onMarketTimeframeChange,
  styleMetadata,
  onStyleMetadataChange,
  onOpenTask,
  compact,
  prefersReducedMotion,
}: AnimationSceneRendererProps) {
  if (model.kind !== "market") return null;
  return (
    <MarketKlineCanvas
      scene={model}
      timeframe={marketTimeframe}
      onTimeframeChange={onMarketTimeframeChange}
      styleMetadata={styleMetadata}
      onStyleMetadataChange={onStyleMetadataChange}
      onOpenTask={onOpenTask}
      compact={compact}
      prefersReducedMotion={prefersReducedMotion}
    />
  );
}

function MarketKlineCanvas({
  scene,
  timeframe,
  onTimeframeChange,
  styleMetadata,
  onStyleMetadataChange,
  onOpenTask,
  compact,
  prefersReducedMotion,
}: {
  scene: MarketKlineScene;
  timeframe: MarketTimeframe;
  onTimeframeChange: (value: MarketTimeframe) => void;
  styleMetadata: AnimationStyleMetadata;
  onStyleMetadataChange: (value: AnimationStyleMetadata) => void;
  onOpenTask: (task: Task) => void;
  compact: boolean;
  prefersReducedMotion: boolean;
}) {
  const { t, locale } = useI18n();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRefs = useRef<MarketChartRefs | null>(null);
  const paletteRef = useRef<ChartPalette | null>(null);
  const sceneRef = useRef(scene);
  const previousSceneRef = useRef<MarketKlineScene | null>(null);
  const animationFrameRef = useRef<number | null>(null);
  const chipLayoutFrameRef = useRef<number | null>(null);
  const markerGroupsRef = useRef(markerTaskGroups(scene.markers));
  const openMarketPreviewRef = useRef<(marker: MarketTaskMarker) => void>(() => undefined);
  const selectedMarkerIdRef = useRef<string | undefined>(scene.markers.at(-1)?.id);
  const chartPreviewMarkerIdRef = useRef<string | null>(null);
  const showEventsRef = useRef(true);
  const [showVolume, setShowVolume] = useState(true);
  const [showEvents, setShowEvents] = useState(true);
  const [hovered, setHovered] = useState<MarketHover | null>(null);
  const [selectedMarkerIndex, setSelectedMarkerIndex] = useState(() => Math.max(0, scene.markers.length - 1));
  const [eventChips, setEventChips] = useState<MarketEventChip[]>([]);
  const [chartPreviewMarkerId, setChartPreviewMarkerId] = useState<string | null>(null);
  const [chartPreviewPoint, setChartPreviewPoint] = useState<MarketPreviewPoint | null>(null);
  const [animatedPrice, setAnimatedPrice] = useState(scene.currentPrice);
  const themeVersion = useChartThemeVersion();
  const markerGroups = useMemo(() => markerTaskGroups(scene.markers), [scene.markers]);
  const markerSignature = useMemo(
    () => scene.markers.map((marker) => [
      marker.id,
      marker.task.id,
      marker.eventKind,
      marker.state,
      marker.time,
      marker.candleIndex,
      marker.did,
      marker.actor,
      marker.force,
      marker.energy,
      marker.pattern,
    ].join(":" )).join("|"),
    [scene.markers],
  );
  const markerIdentitySignature = useMemo(
    () => scene.markers.map((marker) => `${marker.id}:${marker.task.id}:${marker.time}:${marker.candleIndex}`).join("|"),
    [scene.markers],
  );
  const markerCount = scene.markers.length;
  const latestMarkerId = scene.markers.at(-1)?.id;
  useEffect(() => {
    sceneRef.current = scene;
    markerGroupsRef.current = markerGroups;
  }, [markerGroups, scene]);

  useEffect(() => {
    showEventsRef.current = showEvents;
  }, [showEvents]);

  const scheduleEventChipLayout = useCallback(() => {
    if (chipLayoutFrameRef.current !== null) cancelAnimationFrame(chipLayoutFrameRef.current);
    chipLayoutFrameRef.current = requestAnimationFrame(() => {
      chipLayoutFrameRef.current = null;
      const refs = chartRefs.current;
      const container = containerRef.current;
      const current = sceneRef.current;
      if (!refs || !container || !showEventsRef.current) {
        setEventChips([]);
        if (chartPreviewMarkerIdRef.current) setChartPreviewPoint(null);
        return;
      }

      const width = Math.max(container.clientWidth, 1);
      const height = Math.max(container.clientHeight, 1);
      const selectedId = selectedMarkerIdRef.current;
      const selected = selectedId ? current.markers.find((marker) => marker.id === selectedId) : undefined;
      const prioritized = [...current.markers].sort((left, right) => (
        marketMarkerPriority(right) - marketMarkerPriority(left)
        || right.candleIndex - left.candleIndex
        || left.task.id.localeCompare(right.task.id)
      ));
      const markersByCandle = new Map<number, MarketTaskMarker[]>();
      for (const marker of current.markers) {
        const group = markersByCandle.get(marker.candleIndex) ?? [];
        group.push(marker);
        markersByCandle.set(marker.candleIndex, group);
      }
      const chips: MarketEventChip[] = [];
      const seenCandles = new Set<number>();
      const horizontalGap = compact ? 84 : 112;
      const halfWidth = compact ? 45 : 62;
      const chipHeight = compact ? 19 : 21;
      const chartCapacity = Math.max(4, Math.floor(width / horizontalGap));
      const timeframeLimit = timeframe === "1d" ? 12 : timeframe === "1h" ? 10 : timeframe === "30m" ? 9 : 8;
      const maxChips = compact ? 2 : Math.min(timeframeLimit, chartCapacity);

      for (const marker of selected ? [selected, ...prioritized] : prioritized) {
        if (seenCandles.has(marker.candleIndex)) continue;
        const point = marketMarkerChartPoint(refs, current, marker);
        if (!point || point.left < 0 || point.left > width || point.top < 0 || point.top > height) continue;
        const left = Math.min(width - halfWidth, Math.max(halfWidth, point.left));
        const side = marker.position === "aboveBar" ? "above" : "below";
        let top: number | undefined;
        for (let lane = 0; lane < (compact ? 1 : 2); lane += 1) {
          const distance = (compact ? 17 : 26) + lane * (chipHeight + 7);
          const offset = side === "above" ? -distance : distance;
          const candidateTop = Math.min(height - chipHeight, Math.max(46, point.top + offset));
          const collides = chips.some((chip) => (
            Math.abs(chip.left - left) < horizontalGap
            && Math.abs(chip.top - candidateTop) < chipHeight + 5
          ));
          if (!collides) {
            top = candidateTop;
            break;
          }
        }
        if (top === undefined) continue;
        const taskCount = new Set((markersByCandle.get(marker.candleIndex) ?? [marker]).map((entry) => entry.task.id)).size;
        seenCandles.add(marker.candleIndex);
        chips.push({ marker, taskCount, left, top, side });
        if (chips.length >= maxChips) break;
      }

      chips.sort((left, right) => left.left - right.left);
      setEventChips((previous) => {
        const unchanged = previous.length === chips.length && previous.every((chip, index) => {
          const next = chips[index];
          return chip.marker.id === next?.marker.id
            && chip.taskCount === next?.taskCount
            && Math.abs(chip.left - next.left) < 0.5
            && Math.abs(chip.top - next.top) < 0.5
            && chip.side === next.side;
        });
        return unchanged ? previous : chips;
      });

      const previewId = chartPreviewMarkerIdRef.current;
      if (previewId) {
        const previewMarker = current.markers.find((marker) => marker.id === previewId);
        const point = previewMarker ? marketMarkerChartPoint(refs, current, previewMarker) : null;
        setChartPreviewPoint(point);
      }
    });
  }, [compact, timeframe]);

  const closeMarketPreview = useCallback(() => {
    chartPreviewMarkerIdRef.current = null;
    setChartPreviewMarkerId(null);
    setChartPreviewPoint(null);
  }, []);

  const openMarketPreview = useCallback((marker: MarketTaskMarker) => {
    const current = sceneRef.current;
    const markerIndex = current.markers.findIndex((entry) => entry.id === marker.id);
    if (markerIndex >= 0) {
      selectedMarkerIdRef.current = marker.id;
      setSelectedMarkerIndex(markerIndex);
    }
    chartPreviewMarkerIdRef.current = marker.id;
    setChartPreviewMarkerId(marker.id);
    const refs = chartRefs.current;
    if (refs) setChartPreviewPoint(marketMarkerChartPoint(refs, current, marker));
    scheduleEventChipLayout();
  }, [scheduleEventChipLayout]);

  useEffect(() => {
    openMarketPreviewRef.current = openMarketPreview;
  }, [openMarketPreview]);

  useEffect(() => {
    setSelectedMarkerIndex(markerCount === 0 ? 0 : markerCount - 1);
    selectedMarkerIdRef.current = latestMarkerId;
    setHovered(null);
    closeMarketPreview();
    chartRefs.current?.chart.clearCrosshairPosition();
    scheduleEventChipLayout();
  }, [closeMarketPreview, latestMarkerId, markerCount, markerIdentitySignature, scheduleEventChipLayout]);

  useEffect(() => {
    if (!showEvents) closeMarketPreview();
    scheduleEventChipLayout();
  }, [closeMarketPreview, scheduleEventChipLayout, showEvents]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return undefined;
    const palette = readChartPalette();
    paletteRef.current = palette;
    const chart = createChart(container, {
      autoSize: true,
      localization: {
        locale,
        timeFormatter: (time: Time) => formatBeijingChartTime(time, locale, timeframe, false),
      },
      layout: {
        background: { type: ColorType.Solid, color: palette.surface },
        textColor: palette.muted,
        fontFamily: "Inter Variable, Inter, sans-serif",
        fontSize: 11,
        attributionLogo: false,
      },
      grid: {
        vertLines: { color: palette.grid },
        horzLines: { color: palette.grid },
      },
      rightPriceScale: {
        borderColor: palette.border,
        scaleMargins: { top: 0.1, bottom: 0.2 },
      },
      leftPriceScale: { visible: false },
      timeScale: {
        borderColor: palette.border,
        timeVisible: true,
        secondsVisible: false,
        tickMarkFormatter: (time: Time) => formatBeijingChartTime(time, locale, timeframe, true),
        rightOffset: compact ? 2 : 3,
        barSpacing: compact ? 5 : 8,
        minBarSpacing: 3,
        lockVisibleTimeRangeOnResize: true,
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: palette.crosshair, width: 1, style: LineStyle.Dashed, labelBackgroundColor: palette.text },
        horzLine: { color: palette.crosshair, width: 1, style: LineStyle.Dashed, labelBackgroundColor: palette.text },
      },
      handleScroll: { mouseWheel: true, pressedMouseMove: true, horzTouchDrag: true, vertTouchDrag: false },
      handleScale: { mouseWheel: true, pinch: true, axisPressedMouseMove: true, axisDoubleClickReset: true },
    });
    const candles = chart.addSeries(CandlestickSeries, {
      upColor: palette.up,
      downColor: palette.down,
      borderVisible: false,
      wickUpColor: palette.up,
      wickDownColor: palette.down,
      priceLineVisible: true,
      priceLineColor: palette.muted,
      priceLineStyle: LineStyle.Dashed,
      lastValueVisible: true,
      // The range follows real event values: ordinary work stays near 100 while
      // stagnation may legitimately drive the structural baseline down to 1.
      autoscaleInfoProvider: stableMarketAutoscaleInfo,
    });
    const volume = chart.addSeries(HistogramSeries, {
      priceScaleId: "",
      lastValueVisible: false,
      priceLineVisible: false,
    });
    // Preserve a thin, quiet volume pane; its values are visually compressed
    // before reaching this series, while the readout keeps the original number.
    volume.priceScale().applyOptions({ scaleMargins: { top: 0.91, bottom: 0 } });
    const markers = createSeriesMarkers(candles, [], { autoScale: false, zOrder: "aboveSeries" });
    const refs: MarketChartRefs = { chart, candles, volume, markers };
    chartRefs.current = refs;
    setMarketSeriesData(refs, sceneRef.current, palette);
    scheduleEventChipLayout();
    previousSceneRef.current = sceneRef.current;

    const onCrosshairMove = (event: MouseEventParams<Time>) => {
      const time = typeof event.time === "number" ? Number(event.time) : undefined;
      if (time === undefined) {
        setHovered(null);
        return;
      }
      const current = sceneRef.current;
      const candle = current.candles.find((entry) => entry.time === time);
      if (!candle) {
        setHovered(null);
        return;
      }
      setHovered({ candle, markers: markerGroupsRef.current.get(time) ?? [] });
    };
    const onClick = (event: MouseEventParams<Time>) => {
      if (!showEventsRef.current) return;
      const time = typeof event.time === "number" ? Number(event.time) : undefined;
      const marker = time === undefined ? undefined : markerGroupsRef.current.get(time)?.[0];
      if (marker) openMarketPreviewRef.current(marker);
    };
    const onViewportChanged = () => scheduleEventChipLayout();
    chart.subscribeCrosshairMove(onCrosshairMove);
    chart.subscribeClick(onClick);
    chart.timeScale().subscribeVisibleLogicalRangeChange(onViewportChanged);

    const observer = new ResizeObserver(() => {
      chart.resize(Math.max(container.clientWidth, 1), Math.max(container.clientHeight, 1));
      scheduleEventChipLayout();
    });
    observer.observe(container);

    return () => {
      if (animationFrameRef.current !== null) cancelAnimationFrame(animationFrameRef.current);
      if (chipLayoutFrameRef.current !== null) cancelAnimationFrame(chipLayoutFrameRef.current);
      observer.disconnect();
      chart.unsubscribeCrosshairMove(onCrosshairMove);
      chart.unsubscribeClick(onClick);
      chart.timeScale().unsubscribeVisibleLogicalRangeChange(onViewportChanged);
      chart.remove();
      if (chartRefs.current?.chart === chart) chartRefs.current = null;
    };
  }, [compact, locale, scheduleEventChipLayout, themeVersion, timeframe]);

  useEffect(() => {
    const refs = chartRefs.current;
    const palette = paletteRef.current;
    if (!refs || !palette) return;
    const previous = previousSceneRef.current;
    if (animationFrameRef.current !== null) {
      cancelAnimationFrame(animationFrameRef.current);
      animationFrameRef.current = null;
    }
    const previousLiveCandle = previous?.candles.at(-1);
    const liveCandle = scene.candles.at(-1);
    const hasStableTimeline = previous ? sameCandleTimeline(previous, scene) : false;
    if (previous && previousLiveCandle && liveCandle) {
      if (prefersReducedMotion) {
        setMarketSeriesData(refs, scene, palette);
        setAnimatedPrice(scene.currentPrice);
      } else {
        const startedAt = performance.now();
        const onlyLiveCandleChanged = hasStableTimeline && canUpdateOnlyLiveCandle(previous, scene);
        const authoredDuration = onlyLiveCandleChanged ? 360 : hasStableTimeline ? 420 : 560;
        const duration = Math.min(1_600, Math.max(150, Math.round(authoredDuration * 100 / styleMetadata.speed)));
        const animate = (now: number) => {
          const progress = Math.min(1, Math.max(0, (now - startedAt) / duration));
          if (onlyLiveCandleChanged) {
            const interpolated = interpolateMarketCandle(previousLiveCandle, liveCandle, progress);
            updateLiveCandle(refs, interpolated, palette);
            setAnimatedPrice(interpolated.close);
          } else if (hasStableTimeline) {
            const interpolated = interpolateMarketCandles(previous, scene, progress);
            setMarketSeriesData(refs, interpolated, palette);
            setAnimatedPrice(interpolated.candles.at(-1)?.close ?? scene.currentPrice);
          } else {
            const interpolated = interpolateEventTimeline(previous, scene, progress);
            setMarketSeriesData(refs, interpolated, palette);
            setAnimatedPrice(interpolated.candles.at(-1)?.close ?? scene.currentPrice);
          }
          scheduleEventChipLayout();
          if (progress < 1) animationFrameRef.current = requestAnimationFrame(animate);
          else {
            animationFrameRef.current = null;
            setAnimatedPrice(scene.currentPrice);
          }
        };
        animationFrameRef.current = requestAnimationFrame(animate);
      }
    } else {
      setMarketSeriesData(refs, scene, palette);
      setAnimatedPrice(scene.currentPrice);
      scheduleEventChipLayout();
    }
    previousSceneRef.current = scene;
  }, [prefersReducedMotion, scene, scheduleEventChipLayout, styleMetadata.speed]);

  const selectedMarker = scene.markers[Math.min(selectedMarkerIndex, Math.max(0, scene.markers.length - 1))];
  const selectedMarkerId = selectedMarker?.id;
  const chartPreviewMarker = chartPreviewMarkerId
    ? scene.markers.find((marker) => marker.id === chartPreviewMarkerId)
    : undefined;

  useEffect(() => {
    const refs = chartRefs.current;
    const palette = paletteRef.current;
    if (!refs || !palette) return;
    if (!showEvents) {
      refs.markers.setMarkers([]);
      scheduleEventChipLayout();
      return;
    }
    setMarketChartMarkers(refs, sceneRef.current.markers, selectedMarkerId, palette, compact);
    scheduleEventChipLayout();
  }, [compact, markerSignature, scheduleEventChipLayout, selectedMarkerId, showEvents, themeVersion]);

  useEffect(() => {
    chartRefs.current?.volume.applyOptions({ visible: showVolume });
  }, [showVolume, themeVersion]);

  useEffect(() => {
    const chart = chartRefs.current?.chart;
    if (!chart) return;
    chart.timeScale().fitContent();
    scheduleEventChipLayout();
  }, [compact, scheduleEventChipLayout, themeVersion, timeframe]);

  const selectMarker = useCallback((index: number) => {
    if (scene.markers.length === 0) return;
    const normalized = (index + scene.markers.length) % scene.markers.length;
    const marker = scene.markers[normalized];
    const candle = scene.candles[marker.candleIndex];
    selectedMarkerIdRef.current = marker.id;
    setSelectedMarkerIndex(normalized);
    if (candle) setHovered({ candle, markers: markerGroupsRef.current.get(marker.time) ?? [] });
    const refs = chartRefs.current;
    if (refs && candle) refs.chart.setCrosshairPosition(candle.close, marker.time as UTCTimestamp, refs.candles);
    scheduleEventChipLayout();
  }, [scheduleEventChipLayout, scene]);

  const onChartKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      event.preventDefault();
      selectMarker(selectedMarkerIndex + 1);
      return;
    }
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      event.preventDefault();
      selectMarker(selectedMarkerIndex - 1);
      return;
    }
    if (event.key === "Enter" || event.key === " ") {
      const marker = scene.markers[selectedMarkerIndex];
      if (marker) {
        event.preventDefault();
        openMarketPreview(marker);
      }
    }
  }, [openMarketPreview, scene.markers, selectMarker, selectedMarkerIndex]);

  const regimeLabel = marketRegimeLabel(scene.regime, t);
  const previousClose = scene.candles.at(-2)?.close ?? scene.candles[0]?.open ?? scene.currentPrice;
  const liveChange = animatedPrice - previousClose;
  const liveChangePercent = previousClose === 0 ? 0 : (liveChange / previousClose) * 100;
  const displayCandle = hovered?.candle ?? scene.candles.at(-1);
  const displayMarkers = showEvents ? (hovered?.markers ?? []) : [];
  const driverMarker = displayMarkers[0] ?? selectedMarker;
  const driverCandle = driverMarker ? scene.candles[driverMarker.candleIndex] : displayCandle;
  const driverKind: MarketActivityKind = driverMarker?.eventKind ?? (
    driverCandle?.cause === "mixed" ? (scene.dominantEvent ?? "quiet") : (driverCandle?.cause ?? "quiet")
  );
  const driverPattern = driverMarker?.pattern ?? driverCandle?.pattern ?? scene.currentPattern;
  const driverTaskIsBlocked = driverMarker?.eventKind === "blocked";
  const rawBullForce = Math.max(0, driverCandle?.bullForce ?? 0) + Math.max(0, driverMarker?.force ?? 0);
  const rawBearForce = Math.max(0, driverCandle?.bearForce ?? 0) + Math.max(0, -(driverMarker?.force ?? 0));
  const forceTotal = rawBullForce + rawBearForce;
  const staticBullShare = forceTotal > 0
    ? Math.round((rawBullForce / forceTotal) * 100)
    : driverKind === "quiet" ? 44 : 50;
  const liveShareSwing = scene.summary.active > 0 || scene.summary.blocked > 0
    ? Math.round(scene.livePressure * 42)
    : Math.round(scene.livePressure * 18);
  const bullShare = Math.min(96, Math.max(4, staticBullShare + liveShareSwing));
  const bearShare = 100 - bullShare;
  const driverEnergy = driverMarker?.energy ?? Math.round(driverCandle?.energy ?? scene.energyRatio);
  const selectedPosition = selectedMarker ? scene.markers.indexOf(selectedMarker) + 1 : 0;
  const marketMotionMs = Math.min(900, Math.max(120, Math.round(360 * 100 / styleMetadata.speed)));
  return (
    <div
      className="carbon-market-scene"
      data-compact={compact ? "true" : "false"}
      style={{ "--carbon-market-motion": `${marketMotionMs}ms` } as CSSProperties}
    >
      <div className="carbon-market-toolbar">
        <div className="carbon-market-primary">
          <div className="carbon-market-symbol">
            <span>CARBON / TASK</span>
            <small>{regimeLabel} · {marketTimeframeLabel(timeframe, t)}</small>
          </div>
          <div className="carbon-market-quote" aria-label={t("Latest task market price", "最新任务行情") }>
            <strong>{formatPrice(animatedPrice)}</strong>
            <span className={liveChange >= 0 ? "carbon-market-up" : "carbon-market-down"}>
              {liveChange >= 0 ? "+" : ""}{formatPrice(liveChange)} ({liveChangePercent >= 0 ? "+" : ""}{liveChangePercent.toFixed(2)}%)
            </span>
          </div>
          <div className="carbon-market-status-strip" aria-label={summaryLabel(scene.summary, t)}>
            <span><i data-state="active" />{t("Working", "工作")} {scene.summary.active}</span>
            <span><i data-state="completed" />{t("Done", "完成")} {scene.summary.completed}</span>
            <span><i data-state="blocked" />{t("Blocked", "阻塞")} {scene.summary.blocked}</span>
            <span><i data-state="stagnant" />{t("Stagnant", "停滞")} {scene.summary.stagnant}</span>
            <span><i data-state="queued" />{t("Waiting", "待处理")} {scene.summary.queued}</span>
          </div>
        </div>
        <div className="carbon-market-controls">
          <ToggleGroup
            type="single"
            value={timeframe}
            variant="outline"
            size="sm"
            spacing={0}
            aria-label={t("Candlestick period", "K 线周期")}
            onValueChange={(value) => value && onTimeframeChange(value as MarketTimeframe)}
          >
            {(["1m", "5m", "30m", "1h", "1d"] as const).map((value) => (
              <ToggleGroupItem key={value} value={value} aria-label={marketTimeframeLabel(value, t)}>
                {marketTimeframeLabel(value, t)}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <Button
            type="button"
            size="xs"
            variant={showVolume ? "secondary" : "ghost"}
            aria-pressed={showVolume}
            onClick={() => setShowVolume((value) => !value)}
          >
            <BarChart3 data-icon="inline-start" />VOL
          </Button>
          <Button
            type="button"
            size="xs"
            variant={showEvents ? "secondary" : "ghost"}
            aria-pressed={showEvents}
            onClick={() => setShowEvents((value) => !value)}
          >
            <ListFilter data-icon="inline-start" />{t("Events", "事件")} {scene.markers.length}
          </Button>
          <AnimationStyleControls
            style="market-kline"
            metadata={styleMetadata}
            onChange={onStyleMetadataChange}
            compact={compact}
          />
        </div>
      </div>
      <div className="carbon-market-driver-strip" data-event={driverKind}>
        {driverMarker ? (
          <MarketTaskPreviewPopover marker={driverMarker} onOpenTask={onOpenTask} side="bottom" align="start">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="carbon-market-driver min-w-0"
              title={`${driverMarker.task.id} · ${driverMarker.task.title}\n${driverMarker.did}\n${driverMarker.actor}`}
              aria-label={t("Preview task {id}: {title}", "预览任务 {id}：{title}", { id: driverMarker.task.id, title: driverMarker.task.title })}
            >
              <span className="carbon-market-driver-icon" aria-hidden><MarketActivityGlyph kind={driverKind} /></span>
              <span className="carbon-market-driver-copy">
                <span className="carbon-market-driver-kicker">
                  {driverTaskIsBlocked ? t("Task is blocked", "任务受阻") : (
                    <>
                      {marketActivityLabel(driverKind, t)}
                      <span aria-hidden>·</span>
                      {marketPatternLabel(driverPattern, t)}
                    </>
                  )}
                </span>
                <span className="carbon-market-driver-task">
                  <code>{driverMarker.task.id}</code>
                  <span aria-hidden>·</span>
                  <span>{driverMarker.task.title}</span>
                </span>
              </span>
              <span className="carbon-market-driver-meta">
                <strong>{driverMarker.force > 0 ? "+" : ""}{driverMarker.force.toFixed(2)}</strong>
                <small>{driverMarker.actor}</small>
              </span>
              <ChevronRight className="carbon-market-driver-open" aria-hidden />
            </Button>
          </MarketTaskPreviewPopover>
        ) : (
          <div className="carbon-market-driver carbon-market-driver-empty">
            <span className="carbon-market-driver-icon" aria-hidden><CircleDotDashed /></span>
            <span className="carbon-market-driver-copy">
              <span className="carbon-market-driver-kicker">{marketActivityLabel("quiet", t)} · {marketPatternLabel("quiet-drift", t)}</span>
              <span className="carbon-market-driver-task">{t("No task action has produced a chart point yet.", "还没有任务动作可以绘制，时间轴保持为空。")}</span>
            </span>
          </div>
        )}
        <div
          className="carbon-market-force-strip"
          aria-label={t(
            "Task force: {bull}% bullish, {bear}% bearish, energy {energy}",
            "任务力量：多方 {bull}%，空方 {bear}%，量能 {energy}",
            { bull: bullShare, bear: bearShare, energy: driverEnergy },
          )}
        >
          <span className="carbon-market-force-side carbon-market-force-bull">{t("BULL", "多")} {bullShare}</span>
          <span className="carbon-market-force-meter" aria-hidden>
            <span className="carbon-market-force-fill-bull" style={{ width: `${bullShare}%` }} />
            <span className="carbon-market-force-center" />
            <span className="carbon-market-force-fill-bear" style={{ width: `${bearShare}%` }} />
          </span>
          <span className="carbon-market-force-side carbon-market-force-bear">{t("BEAR", "空")} {bearShare}</span>
          <span className="carbon-market-energy"><Swords aria-hidden />{t("Energy", "量能")} {driverEnergy}</span>
        </div>
      </div>
      <div
        className="carbon-market-chart-shell"
        role="region"
        tabIndex={0}
        aria-label={t("Interactive task momentum K-line. Drag to pan, scroll to zoom, and use arrow keys to select task events.", "可交互任务势能 K 线。拖动平移、滚轮缩放，并可用方向键选择任务事件。")}
        onKeyDown={onChartKeyDown}
      >
        <div ref={containerRef} className="carbon-market-chart" />
        {scene.candles.length === 0 && (
          <div className="carbon-market-empty-tape">
            <CircleDotDashed />
            <strong>{t("No task activity yet", "暂无任务动作")}</strong>
            <span>{t("Carbon will add a point when a task is created, claimed, updated, blocked, completed, or becomes stagnant.", "创建、认领、推进、阻塞、完成或进入停滞时，Carbon 才会在这里增加一个时间点。")}</span>
          </div>
        )}
        <div className="carbon-market-readout" aria-label={t("OHLC and volume", "开高低收与量能") }>
          {displayCandle && (
            <>
              <span className="carbon-market-readout-time">{formatMarketTime(displayCandle.time, locale, timeframe)}</span>
              <span>O <strong>{formatPrice(displayCandle.open)}</strong></span>
              <span>H <strong>{formatPrice(displayCandle.high)}</strong></span>
              <span>L <strong>{formatPrice(displayCandle.low)}</strong></span>
              <span>C <strong>{formatPrice(displayCandle.close)}</strong></span>
              <span>VOL <strong>{Math.round(displayCandle.energy)}</strong></span>
              <span>ER <strong>{scene.energyRatio}%</strong></span>
              {displayMarkers[0] && (
                <span className="carbon-market-readout-event">
                  {markerActivityLabel(displayMarkers[0], t)} · {displayMarkers[0].task.id} · {displayMarkers[0].task.title}{displayMarkers.length > 1 ? ` +${displayMarkers.length - 1}` : ""}
                </span>
              )}
            </>
          )}
        </div>
        {showEvents && eventChips.length > 0 && (
          <div className="carbon-market-event-chips" aria-label={t("Task events on the chart", "图中的任务事件") }>
            {eventChips.map((chip) => (
              <MarketTaskPreviewPopover
                key={chip.marker.id}
                marker={chip.marker}
                onOpenTask={onOpenTask}
                side={chip.side === "above" ? "top" : "bottom"}
                align="center"
              >
                <Button
                  type="button"
                  size="xs"
                  variant="secondary"
                  className="carbon-market-event-chip"
                  data-event={chip.marker.eventKind}
                  data-side={chip.side}
                  style={{ left: chip.left, top: chip.top }}
                  title={`${chip.marker.task.id} · ${chip.marker.task.title}${chip.taskCount > 1 ? ` · +${chip.taskCount - 1}` : ""}`}
                  aria-label={t("Preview task {id}: {title}", "预览任务 {id}：{title}", { id: chip.marker.task.id, title: chip.marker.task.title })}
                >
                  <MarketActivityGlyph kind={chip.marker.eventKind} />
                  <span>{chip.marker.task.title}</span>
                  {chip.taskCount > 1 && <small>+{chip.taskCount - 1}</small>}
                </Button>
              </MarketTaskPreviewPopover>
            ))}
          </div>
        )}
        {chartPreviewMarker && chartPreviewPoint && (
          <MarketChartTaskPreview
            marker={chartPreviewMarker}
            point={chartPreviewPoint}
            onOpenTask={onOpenTask}
            onClose={closeMarketPreview}
          />
        )}
      </div>
      <div className="carbon-market-footer">
        <div className="carbon-market-event-rail" aria-label={t("Task event navigation", "任务事件导航") }>
          <Button
            type="button"
            size="icon-xs"
            variant="ghost"
            disabled={scene.markers.length === 0}
            aria-label={t("Previous task event", "上一个任务事件")}
            onClick={() => selectMarker(selectedMarkerIndex - 1)}
          >
            <ChevronLeft />
          </Button>
          {selectedMarker ? (
            <MarketTaskPreviewPopover marker={selectedMarker} onOpenTask={onOpenTask} side="top" align="center">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="carbon-market-event-current min-w-0 flex-1 justify-start"
                title={`${selectedMarker.task.id} · ${selectedMarker.task.title}`}
                aria-label={t("Preview task {id}: {title}", "预览任务 {id}：{title}", { id: selectedMarker.task.id, title: selectedMarker.task.title })}
              >
                <span className="carbon-market-event-position">{selectedPosition}/{scene.markers.length}</span>
                <span className="carbon-market-marker-dot" data-event={selectedMarker.eventKind} aria-hidden />
                <Badge variant={selectedMarker.eventKind === "blocked" ? "destructive" : marketActivityBadgeVariant(selectedMarker.eventKind)}>{markerActivityLabel(selectedMarker, t)}</Badge>
                <span className="carbon-market-event-id">{selectedMarker.task.id}</span>
                <span className="carbon-market-event-title">{selectedMarker.task.title}</span>
                <span className="carbon-market-event-meta">{selectedMarker.eventKind === "blocked" ? "" : `${marketPatternLabel(selectedMarker.pattern, t)} · `}{t("Energy", "量能")} {selectedMarker.energy}</span>
                <ChevronRight data-icon="inline-end" />
              </Button>
            </MarketTaskPreviewPopover>
          ) : (
            <span className="carbon-market-event-empty">{t("No task events", "暂无任务事件")}</span>
          )}
          <Button
            type="button"
            size="icon-xs"
            variant="ghost"
            disabled={scene.markers.length === 0}
            aria-label={t("Next task event", "下一个任务事件")}
            onClick={() => selectMarker(selectedMarkerIndex + 1)}
          >
            <ChevronRight />
          </Button>
        </div>
      </div>
    </div>
  );
}
