import {
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type WheelEvent as ReactWheelEvent,
} from "react";
import {
  Activity,
  ArrowRight,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  ClipboardCheck,
  Gauge,
  Layers3,
  List,
  MousePointer2,
  PackageCheck,
  Pause,
  Radio,
  RotateCcw,
  Sparkles,
  UsersRound,
  Wrench,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { type PixelAgentsScene, type PixelTaskAgent, stableHash } from "@/lib/animation-board";
import { useI18n } from "@/lib/i18n";
import type { AnimationStyleMetadata } from "@/lib/personalization";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn } from "@/lib/utils";
import type { Task } from "@/lib/api";
import "./CarbonPixelBoard.css";

/** The public renderer contract is intentionally unchanged. */
export type CarbonPixelBoardProps = {
  scene: PixelAgentsScene;
  onOpenTask: (task: Task) => void;
  compact?: boolean;
  prefersReducedMotion?: boolean;
  metadata: AnimationStyleMetadata;
  controls?: ReactNode;
  className?: string;
};

type WorkZone = "queued" | "active" | "review" | "blocked" | "completed";
type WorkView = "overview" | "queue";
type QueueFilter = "all" | WorkZone | `worker:${string}`;

type WorkTicket = {
  agent: PixelTaskAgent;
  zone: WorkZone;
  energy: number;
  variant: number;
};

type WorkerGroup = {
  id: string;
  name: string;
  initial: string;
  tickets: WorkTicket[];
  energy: number;
};

const ZONES: readonly WorkZone[] = ["queued", "active", "review", "blocked", "completed"];
const REVIEW_STATUSES = new Set(["awaiting_review", "in_review", "review", "qa", "testing", "verification", "verifying"]);
const COMPLETED_STATUSES = new Set(["done", "closed", "complete", "completed", "finished"]);
const BLOCKED_STATUSES = new Set(["blocked", "stalled", "阻塞", "受阻"]);
const MAX_OVERVIEW_TICKETS = 3;
const MAX_QUEUE_TICKETS = 28;
const MAX_WORKERS = 6;

function normalized(value: string | undefined): string {
  return value?.trim().toLowerCase().replace(/[\s-]+/g, "_") ?? "";
}

function ownerOf(agent: PixelTaskAgent): string | undefined {
  const owner = agent.task.assignee?.trim();
  return owner || undefined;
}

function zoneFor(agent: PixelTaskAgent): WorkZone {
  const status = normalized(agent.task.status);
  if (agent.state === "blocked" || agent.task.executionState === "stalled" || BLOCKED_STATUSES.has(status)) return "blocked";
  if (agent.state === "completed" || COMPLETED_STATUSES.has(status)) return "completed";
  if (REVIEW_STATUSES.has(status) || agent.task.executionState === "awaiting_review") return "review";
  if (agent.state === "active" || agent.task.executionState === "active") return "active";
  return "queued";
}

function energyFor(agent: PixelTaskAgent, zone: WorkZone, volatility: number): number {
  const base: Record<WorkZone, number> = { queued: 22, active: 70, review: 48, blocked: 82, completed: 34 };
  const phase = (agent.station.phase % 4) * 3;
  return Math.min(100, Math.max(8, Math.round(base[zone] + phase + Math.min(22, volatility / 55))));
}

function zoneTitle(zone: WorkZone, t: ReturnType<typeof useI18n>["t"]): string {
  switch (zone) {
    case "queued": return t("To be claimed", "待认领");
    case "active": return t("In progress", "处理中");
    case "review": return t("Under review", "审核中");
    case "blocked": return t("Blocked", "受阻");
    case "completed": return t("Delivered", "已交付");
  }
}

function zoneDescription(zone: WorkZone, t: ReturnType<typeof useI18n>["t"]): string {
  switch (zone) {
    case "queued": return t("Waiting for an owner", "等待负责人接手");
    case "active": return t("Agents at their desks", "智能体正在工位处理");
    case "review": return t("Checking the hand-off", "核对交付结果");
    case "blocked": return t("Needs a decision or hand", "需要决定或协助");
    case "completed": return t("Ready to close the loop", "成果已经交付");
  }
}

function zoneAction(zone: WorkZone): "dispatch" | "typing" | "blocked" | "complete" {
  if (zone === "blocked") return "blocked";
  if (zone === "completed") return "complete";
  if (zone === "active" || zone === "review") return "typing";
  return "dispatch";
}

function zoneIcon(zone: WorkZone) {
  switch (zone) {
    case "queued": return <Radio aria-hidden />;
    case "active": return <Wrench aria-hidden />;
    case "review": return <ClipboardCheck aria-hidden />;
    case "blocked": return <CircleAlert aria-hidden />;
    case "completed": return <PackageCheck aria-hidden />;
  }
}

function actionText(zone: WorkZone, t: ReturnType<typeof useI18n>["t"]): string {
  switch (zone) {
    case "queued": return t("Waiting for a teammate", "等待队友接手");
    case "active": return t("Working through the next step", "正在推进下一步");
    case "review": return t("Checking the result", "正在核对结果");
    case "blocked": return t("Waiting for a way forward", "等待问题处理");
    case "completed": return t("Ready to hand over", "准备交付");
  }
}

function readableOwner(ticket: WorkTicket, t: ReturnType<typeof useI18n>["t"]): string {
  return ownerOf(ticket.agent) ?? t("Unassigned", "未认领");
}

function describeScene(tickets: readonly WorkTicket[], workers: readonly WorkerGroup[], t: ReturnType<typeof useI18n>["t"]): string {
  const count = (zone: WorkZone) => tickets.filter((ticket) => ticket.zone === zone).length;
  return t(
    "Work floor: {total} tasks across {workers} agents. {active} in progress, {queued} waiting, {review} under review, {blocked} blocked, {completed} delivered.",
    "工作风：{total} 个任务分布在 {workers} 位智能体之间；处理中 {active}，待认领 {queued}，审核中 {review}，受阻 {blocked}，已交付 {completed}。",
    { total: tickets.length, workers: workers.length, active: count("active"), queued: count("queued"), review: count("review"), blocked: count("blocked"), completed: count("completed") },
  );
}

function clampZoom(value: number): number {
  return Math.min(140, Math.max(70, Math.round(value / 5) * 5));
}

function isWorkZone(value: QueueFilter): value is WorkZone {
  return ZONES.includes(value as WorkZone);
}

export function CarbonPixelBoard({ scene, onOpenTask, compact = false, prefersReducedMotion = false, metadata, controls, className }: CarbonPixelBoardProps) {
  const { t } = useI18n();
  const summaryId = useId();
  const viewportRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ pointerId: number; x: number; y: number; left: number; top: number } | null>(null);
  const [view, setView] = useState<WorkView>("overview");
  const [filter, setFilter] = useState<QueueFilter>("all");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [zoom, setZoom] = useState(100);

  const tickets = useMemo<WorkTicket[]>(() => scene.agents.map((agent) => {
    const zone = zoneFor(agent);
    return { agent, zone, energy: energyFor(agent, zone, metadata.volatility), variant: stableHash(`${agent.task.id}:work-floor`) % 5 };
  // Keep the visual slots deterministic while the live tick updates phase/energy. Sorting by
  // energy made cards jump between slots every tick, which read as a rapid flash rather than
  // workers settling into a rhythm.
  }).sort((left, right) => ZONES.indexOf(left.zone) - ZONES.indexOf(right.zone) || stableHash(left.agent.task.id) - stableHash(right.agent.task.id)), [metadata.volatility, scene.agents]);

  const byZone = useMemo(() => {
    const grouped = new Map<WorkZone, WorkTicket[]>();
    for (const zone of ZONES) grouped.set(zone, []);
    for (const ticket of tickets) grouped.get(ticket.zone)?.push(ticket);
    return grouped;
  }, [tickets]);

  const workers = useMemo<WorkerGroup[]>(() => {
    const grouped = new Map<string, WorkTicket[]>();
    for (const ticket of tickets) {
      const owner = ownerOf(ticket.agent);
      if (!owner) continue;
      grouped.set(owner, [...(grouped.get(owner) ?? []), ticket]);
    }
    return Array.from(grouped, ([name, ownerTickets]) => ({
      id: name,
      name,
      initial: ownerTickets[0]?.agent.workerInitial || name.slice(0, 2).toUpperCase(),
      tickets: ownerTickets,
      energy: Math.round(ownerTickets.reduce((sum, ticket) => sum + ticket.energy, 0) / Math.max(1, ownerTickets.length)),
    })).sort((left, right) => right.tickets.length - left.tickets.length || left.name.localeCompare(right.name));
  }, [tickets]);

  const selected = tickets.find((ticket) => ticket.agent.task.id === selectedId) ?? null;
  const filteredTickets = useMemo(() => {
    if (filter === "all") return tickets;
    if (filter.startsWith("worker:")) return workers.find((worker) => worker.id === filter.slice(7))?.tickets ?? [];
    return isWorkZone(filter) ? (byZone.get(filter) ?? []) : [];
  }, [byZone, filter, tickets, workers]);
  const queueLimit = compact ? 12 : MAX_QUEUE_TICKETS;
  const queueTickets = filteredTickets.slice(0, queueLimit);
  const hiddenQueueCount = Math.max(0, filteredTickets.length - queueTickets.length);
  const visibleWorkers = workers.slice(0, compact ? 3 : MAX_WORKERS);
  const hiddenWorkerCount = Math.max(0, workers.length - visibleWorkers.length);
  const scale = zoom / 100;
  const cycleSeconds = Math.max(0.38, Math.min(3.4, 1.8 * (100 / Math.max(25, metadata.speed))));
  const activity = Math.min(1, Math.max(0.15, 0.16 + metadata.volatility / 1000 * 0.84));
  const motionStyle = { "--work-floor-cycle": `${cycleSeconds}s`, "--work-floor-activity": activity, "--work-floor-scale": scale } as CSSProperties;
  const frameStyle = { width: `${Math.max(100, scale * 100)}%` } as CSSProperties;

  const showQueue = (nextFilter: QueueFilter = "all") => { setFilter(nextFilter); setView("queue"); };
  const onWheel = (event: ReactWheelEvent<HTMLDivElement>) => {
    if (!event.ctrlKey && !event.metaKey) return;
    event.preventDefault();
    setZoom((current) => clampZoom(current + (event.deltaY < 0 ? 5 : -5)));
  };
  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    if (target.closest("button, a, input, [data-no-pan='true']")) return;
    const viewport = viewportRef.current;
    if (!viewport) return;
    dragRef.current = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, left: viewport.scrollLeft, top: viewport.scrollTop };
    viewport.setPointerCapture(event.pointerId);
    viewport.dataset.dragging = "true";
  };
  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    const viewport = viewportRef.current;
    if (!drag || !viewport || drag.pointerId !== event.pointerId) return;
    viewport.scrollLeft = drag.left - (event.clientX - drag.x);
    viewport.scrollTop = drag.top - (event.clientY - drag.y);
  };
  const stopDragging = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.pointerId === event.pointerId) dragRef.current = null;
    const viewport = viewportRef.current;
    if (viewport?.hasPointerCapture(event.pointerId)) viewport.releasePointerCapture(event.pointerId);
    if (viewport) delete viewport.dataset.dragging;
  };
  const onViewportKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    if (event.key === "ArrowRight") { event.preventDefault(); viewport.scrollLeft += 72; }
    if (event.key === "ArrowLeft") { event.preventDefault(); viewport.scrollLeft -= 72; }
    if (event.key === "ArrowDown") { event.preventDefault(); viewport.scrollTop += 72; }
    if (event.key === "ArrowUp") { event.preventDefault(); viewport.scrollTop -= 72; }
    if (event.key === "+" || event.key === "=") { event.preventDefault(); setZoom((current) => clampZoom(current + 5)); }
    if (event.key === "-") { event.preventDefault(); setZoom((current) => clampZoom(current - 5)); }
    if (event.key === "0") { event.preventDefault(); setZoom(100); }
  };

  return (
    <section className={cn("carbon-work-floor", className)} style={motionStyle} data-compact={compact ? "true" : "false"} data-reduced-motion={prefersReducedMotion ? "true" : "false"} data-view={view} aria-label={t("Work-style floor", "工作风工作台")} aria-describedby={summaryId}>
      <p id={summaryId} className="sr-only" aria-live="polite">{describeScene(tickets, workers, t)}</p>
      <header className="carbon-work-floor-header">
        <div className="carbon-work-floor-brand"><span className="carbon-work-floor-mark" aria-hidden><Bot /></span><div className="min-w-0"><p className="carbon-work-floor-eyebrow">CARBON / WORK STYLE</p><h2>{t("Work floor", "工作风")}</h2><p>{t("A tiny control room for the work in motion.", "把正在发生的工作，收进一块小小的控制台。")}</p></div></div>
        <div className="carbon-work-floor-head-actions"><div className="carbon-work-floor-metrics" aria-label={t("Task totals", "任务统计")}><span data-tone="active"><Activity aria-hidden />{scene.summary.active}</span><span data-tone="blocked"><CircleAlert aria-hidden />{scene.summary.blocked}</span><span data-tone="completed"><CheckCircle2 aria-hidden />{scene.summary.completed}</span></div><div className="carbon-work-floor-actions"><div className="carbon-work-floor-view-toggle" role="group" aria-label={t("Work floor view", "工作风视图")}><button type="button" className={cn({ "is-active": view === "overview" })} aria-pressed={view === "overview"} onClick={() => setView("overview")}><Layers3 aria-hidden />{t("Overview", "总览")}</button><button type="button" className={cn({ "is-active": view === "queue" })} aria-pressed={view === "queue"} onClick={() => setView("queue")}><List aria-hidden />{t("Queue", "队列")}</button></div>{controls}</div></div>
      </header>

      <div className="carbon-work-floor-toolbar"><div className="carbon-work-floor-zone-tabs" role="tablist" aria-label={t("Task zones", "任务分区")}><button type="button" role="tab" aria-selected={filter === "all"} className={cn({ "is-active": filter === "all" })} onClick={() => { setFilter("all"); setView("overview"); }}>{t("All", "全部")} <b>{tickets.length}</b></button>{ZONES.map((zone) => <button key={zone} type="button" role="tab" aria-selected={filter === zone} className={cn("carbon-work-floor-zone-tab", { "is-active": filter === zone })} data-zone={zone} onClick={() => showQueue(zone)}>{zoneIcon(zone)}<span>{zoneTitle(zone, t)}</span><b>{byZone.get(zone)?.length ?? 0}</b></button>)}</div><div className="carbon-work-floor-zoom" role="group" aria-label={t("Canvas zoom", "画布缩放")}><Gauge aria-hidden /><button type="button" onClick={() => setZoom((current) => clampZoom(current - 5))} aria-label={t("Zoom out", "缩小")} title={t("Zoom out", "缩小")}><ZoomOut aria-hidden /></button><output>{zoom}%</output><button type="button" onClick={() => setZoom((current) => clampZoom(current + 5))} aria-label={t("Zoom in", "放大")} title={t("放大", "放大")}><ZoomIn aria-hidden /></button><button type="button" onClick={() => setZoom(100)} aria-label={t("Reset zoom", "重置缩放")} title={t("Reset zoom", "重置缩放")}><RotateCcw aria-hidden /></button></div></div>

      {selected && <TaskPreview ticket={selected} onOpenTask={onOpenTask} onDismiss={() => setSelectedId(null)} t={t} />}
      <div ref={viewportRef} className="carbon-work-floor-viewport" tabIndex={0} role="region" aria-label={t("Interactive work floor; drag to pan", "可互动工作台；拖动可以平移")} onWheel={onWheel} onPointerDown={onPointerDown} onPointerMove={onPointerMove} onPointerUp={stopDragging} onPointerCancel={stopDragging} onKeyDown={onViewportKeyDown}>
        <div className="carbon-work-floor-frame" style={frameStyle}><div className="carbon-work-floor-canvas" style={motionStyle}>{view === "overview" ? <OverviewCanvas tickets={tickets} byZone={byZone} workers={workers} selectedId={selectedId} onSelect={setSelectedId} onShowQueue={showQueue} t={t} /> : <QueueCanvas tickets={queueTickets} total={filteredTickets.length} hidden={hiddenQueueCount} filter={filter} workers={visibleWorkers} hiddenWorkers={hiddenWorkerCount} selectedId={selectedId} onSelect={setSelectedId} onShowQueue={showQueue} t={t} />}</div></div>
      </div>
      <footer className="carbon-work-floor-footer"><span><MousePointer2 aria-hidden />{t("Click once for a preview · click Open task to enter", "单击先看摘要 · 在摘要中点“打开任务”进入详情")}</span><span><Sparkles aria-hidden />{t("Drag to pan · Ctrl/⌘ + wheel to zoom", "拖动平移 · Ctrl/⌘ + 滚轮缩放")}</span></footer>
    </section>
  );
}

function OverviewCanvas({ tickets, byZone, workers, selectedId, onSelect, onShowQueue, t }: { tickets: readonly WorkTicket[]; byZone: ReadonlyMap<WorkZone, WorkTicket[]>; workers: readonly WorkerGroup[]; selectedId: string | null; onSelect: (id: string) => void; onShowQueue: (filter: QueueFilter) => void; t: ReturnType<typeof useI18n>["t"] }) {
  return <div className="carbon-work-floor-overview"><section className="carbon-work-floor-stage" aria-label={t("Work zones", "工作分区")}><header className="carbon-work-floor-stage-header"><div><p className="carbon-work-floor-eyebrow">LIVE MINI CONSOLE</p><h3>{t("The pulse of this project", "项目现在的工作脉搏")}</h3></div><span>{t("{count} tasks in view", "当前 {count} 个任务", { count: tickets.length })}</span></header><div className="carbon-work-floor-zone-grid">{ZONES.map((zone) => <ZoneCard key={zone} zone={zone} tickets={byZone.get(zone) ?? []} selectedId={selectedId} onSelect={onSelect} onShowQueue={onShowQueue} t={t} />)}</div></section><section className="carbon-work-floor-roster" aria-label={t("Worker activity", "智能体活动")}><header className="carbon-work-floor-subhead"><div className="carbon-work-floor-section-icon"><UsersRound aria-hidden /></div><div><p>{t("Worker rhythm", "智能体节奏")}</p><h3>{t("A short line for every owner", "每位负责人的一行状态")}</h3></div><b>{workers.length}</b></header><div className="carbon-work-floor-worker-list">{workers.slice(0, MAX_WORKERS).map((worker) => <WorkerRow key={worker.id} worker={worker} onShowQueue={onShowQueue} t={t} />)}{workers.length > MAX_WORKERS && <button type="button" className="carbon-work-floor-more" onClick={() => onShowQueue("all")}>+{workers.length - MAX_WORKERS} {t("more workers", "位智能体")}<ChevronRight aria-hidden /></button>}{workers.length === 0 && <p className="carbon-work-floor-empty">{t("No owner has claimed a task yet.", "还没有智能体认领任务。")}</p>}</div></section></div>;
}

function ZoneCard({ zone, tickets, selectedId, onSelect, onShowQueue, t }: { zone: WorkZone; tickets: readonly WorkTicket[]; selectedId: string | null; onSelect: (id: string) => void; onShowQueue: (filter: QueueFilter) => void; t: ReturnType<typeof useI18n>["t"] }) {
  const visible = tickets.slice(0, MAX_OVERVIEW_TICKETS);
  const hidden = Math.max(0, tickets.length - visible.length);
  const energy = tickets.length === 0 ? 0 : Math.round(tickets.reduce((sum, ticket) => sum + ticket.energy, 0) / tickets.length);
  return <article className="carbon-work-floor-zone" data-zone={zone}><header className="carbon-work-floor-zone-head"><span className="carbon-work-floor-zone-icon" data-zone={zone}>{zoneIcon(zone)}</span><div><h4>{zoneTitle(zone, t)}</h4><p>{zoneDescription(zone, t)}</p></div><b>{tickets.length}</b></header><div className="carbon-work-floor-energy" aria-label={t("Energy {energy}%", "能量 {energy}%", { energy })}><span style={{ "--work-energy": `${energy}%` } as CSSProperties} /></div><div className="carbon-work-floor-zone-tasks">{visible.map((ticket) => <TaskChip key={ticket.agent.task.id} ticket={ticket} selected={ticket.agent.task.id === selectedId} onSelect={onSelect} t={t} />)}{hidden > 0 && <button type="button" className="carbon-work-floor-overflow" onClick={() => onShowQueue(zone)}>+{hidden} {t("more", "个任务")}<ChevronRight aria-hidden /></button>}{tickets.length === 0 && <span className="carbon-work-floor-quiet"><Pause aria-hidden />{t("Quiet", "暂时空闲")}</span>}</div><button type="button" className="carbon-work-floor-zone-open" onClick={() => onShowQueue(zone)}>{t("Open queue", "查看队列")}<ChevronRight aria-hidden /></button></article>;
}

function TaskChip({ ticket, selected, onSelect, t }: { ticket: WorkTicket; selected: boolean; onSelect: (id: string) => void; t: ReturnType<typeof useI18n>["t"] }) {
  const task = ticket.agent.task;
  return <button type="button" className="carbon-work-floor-task-chip" data-zone={ticket.zone} data-selected={selected ? "true" : "false"} onClick={() => onSelect(task.id)} aria-pressed={selected} title={task.title} aria-label={t("Preview {title}", "查看摘要：{title}", { title: task.title })}><PixelAvatar ticket={ticket} compact /><span>{task.title}</span><i aria-hidden /></button>;
}

function WorkerRow({ worker, onShowQueue, t }: { worker: WorkerGroup; onShowQueue: (filter: QueueFilter) => void; t: ReturnType<typeof useI18n>["t"] }) {
  const active = worker.tickets.filter((ticket) => ticket.zone === "active").length;
  const blocked = worker.tickets.filter((ticket) => ticket.zone === "blocked").length;
  const tone = blocked > 0 ? "blocked" : active > 0 ? "active" : "quiet";
  return <button type="button" className="carbon-work-floor-worker-row" onClick={() => onShowQueue(`worker:${worker.id}`)} title={t("Open {name} queue", "打开 {name} 的队列", { name: worker.name })}><span className="carbon-work-floor-worker-initial">{worker.initial.slice(0, 2)}</span><span className="carbon-work-floor-worker-copy"><strong>{worker.name}</strong><small>{t("{count} tasks", "{count} 个任务", { count: worker.tickets.length })}</small></span><span className="carbon-work-floor-worker-signal" data-tone={tone}><i />{blocked > 0 ? t("Needs help", "需要处理") : active > 0 ? t("At work", "处理中") : t("Quiet", "空闲")}</span><ChevronRight aria-hidden /></button>;
}

function QueueCanvas({ tickets, total, hidden, filter, workers, hiddenWorkers, selectedId, onSelect, onShowQueue, t }: { tickets: readonly WorkTicket[]; total: number; hidden: number; filter: QueueFilter; workers: readonly WorkerGroup[]; hiddenWorkers: number; selectedId: string | null; onSelect: (id: string) => void; onShowQueue: (filter: QueueFilter) => void; t: ReturnType<typeof useI18n>["t"] }) {
  const filterLabel = filter === "all" ? t("All tasks", "全部任务") : filter.startsWith("worker:") ? (workers.find((worker) => worker.id === filter.slice(7))?.name ?? t("Worker queue", "智能体队列")) : isWorkZone(filter) ? zoneTitle(filter, t) : t("Task queue", "任务队列");
  return <div className="carbon-work-floor-queue-layout"><section className="carbon-work-floor-queue-panel" aria-label={t("Task queue", "任务队列")}><header className="carbon-work-floor-queue-head"><div><p className="carbon-work-floor-eyebrow">QUEUE / FOCUS</p><h3>{filterLabel}</h3></div><span>{t("{shown} of {total}", "显示 {shown} / {total}", { shown: tickets.length, total })}</span></header><div className="carbon-work-floor-queue-list">{tickets.map((ticket) => <QueueRow key={ticket.agent.task.id} ticket={ticket} selected={ticket.agent.task.id === selectedId} onSelect={onSelect} t={t} />)}{hidden > 0 && <p className="carbon-work-floor-queue-more">+{hidden} {t("tasks stay aggregated to keep this view light.", "个任务已聚合，页面保持轻量。")}</p>}{tickets.length === 0 && <p className="carbon-work-floor-empty">{t("Nothing in this queue right now.", "这个队列暂时没有任务。")}</p>}</div></section><aside className="carbon-work-floor-queue-rail" aria-label={t("Quick filters", "快速筛选")}><header className="carbon-work-floor-subhead"><div className="carbon-work-floor-section-icon"><UsersRound aria-hidden /></div><div><p>{t("Owners", "负责人")}</p><h3>{t("Jump to a worker", "跳转到智能体")}</h3></div></header><div className="carbon-work-floor-rail-list">{workers.map((worker) => <button key={worker.id} type="button" className="carbon-work-floor-rail-worker" onClick={() => onShowQueue(`worker:${worker.id}`)}><span>{worker.initial.slice(0, 2)}</span><strong>{worker.name}</strong><small>{worker.tickets.length}</small></button>)}{hiddenWorkers > 0 && <button type="button" className="carbon-work-floor-more" onClick={() => onShowQueue("all")}>+{hiddenWorkers} {t("more", "位智能体")}</button>}{workers.length === 0 && <p className="carbon-work-floor-empty">{t("No owners yet.", "暂时没有负责人。")}</p>}</div></aside></div>;
}

function QueueRow({ ticket, selected, onSelect, t }: { ticket: WorkTicket; selected: boolean; onSelect: (id: string) => void; t: ReturnType<typeof useI18n>["t"] }) {
  const task = ticket.agent.task;
  const type = carbonTaskTypeLabel(task.type, t) || t("Task", "任务");
  return <button type="button" className="carbon-work-floor-queue-row" data-zone={ticket.zone} data-selected={selected ? "true" : "false"} onClick={() => onSelect(task.id)} aria-pressed={selected}><PixelAvatar ticket={ticket} /><span className="carbon-work-floor-queue-copy"><strong title={task.title}>{task.title}</strong><small>{task.id} · {type} · {readableOwner(ticket, t)}</small><em>{actionText(ticket.zone, t)}</em></span><span className="carbon-work-floor-queue-energy">{ticket.energy}<small>{t("energy", "能量")}</small></span><ChevronRight aria-hidden /></button>;
}

function TaskPreview({ ticket, onOpenTask, onDismiss, t }: { ticket: WorkTicket; onOpenTask: (task: Task) => void; onDismiss: () => void; t: ReturnType<typeof useI18n>["t"] }) {
  const task = ticket.agent.task;
  const type = carbonTaskTypeLabel(task.type, t) || t("Task", "任务");
  return <aside className="carbon-work-floor-preview" data-zone={ticket.zone} aria-live="polite"><div className="carbon-work-floor-preview-main"><PixelAvatar ticket={ticket} /><div className="min-w-0"><p>{t("Quick preview", "快速摘要")} · {zoneTitle(ticket.zone, t)}</p><h3 title={task.title}>{task.title}</h3><span>{task.id} · {type} · {readableOwner(ticket, t)}</span></div></div><div className="carbon-work-floor-preview-action"><span>{actionText(ticket.zone, t)}</span><button type="button" className="carbon-work-floor-dismiss" onClick={onDismiss} aria-label={t("Close preview", "关闭摘要")}><ChevronDown aria-hidden /></button><button type="button" className="carbon-work-floor-open" onClick={() => onOpenTask(task)}>{t("Open task", "打开任务")}<ArrowRight aria-hidden /></button></div></aside>;
}

function PixelAvatar({ ticket, compact = false }: { ticket: WorkTicket; compact?: boolean }) {
  const action = zoneAction(ticket.zone);
  return <span className="carbon-work-floor-avatar" data-zone={ticket.zone} data-action={action} data-variant={ticket.variant} data-compact={compact ? "true" : "false"} aria-hidden><i className="carbon-work-floor-avatar-head" /><i className="carbon-work-floor-avatar-body" /><i className="carbon-work-floor-avatar-arm carbon-work-floor-avatar-arm-a" /><i className="carbon-work-floor-avatar-arm carbon-work-floor-avatar-arm-b" /><i className="carbon-work-floor-avatar-leg carbon-work-floor-avatar-leg-a" /><i className="carbon-work-floor-avatar-leg carbon-work-floor-avatar-leg-b" /></span>;
}
