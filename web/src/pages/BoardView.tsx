import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  pointerWithin,
  rectIntersection,
  useDroppable,
  useSensor,
  useSensors,
  type CollisionDetection,
  type DragEndEvent,
  type DragOverEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  CheckCircle2,
  GitBranch,
  Loader2,
  MoreHorizontal,
  Plus,
  Search,
  SquareKanban,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { SelectItem } from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { StatusIcon } from "@/components/StatusIcon";
import { PriorityIcon, priorityLabel } from "@/components/PriorityIcon";
import { Facet } from "@/components/Facet";
import { SearchableFacet } from "@/components/SearchableFacet";
import { EmptyState } from "@/components/EmptyState";
import { Assignee } from "@/components/Assignee";
import { useDeleteTask, useReorder, useTasks, useTransition } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { effectiveRank } from "@/lib/filter";
import { cn, labelTone, statusLabel } from "@/lib/utils";
import { carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";
import type { Status, Task } from "@/lib/api";

type AsyncBoardActionResult = Promise<unknown> | unknown;

export type KanbanBoardPending = {
  /** Task whose workflow transition is being verified by the server. */
  id?: string;
  /** Destination state, used to choose a verification-specific fallback label. */
  to?: string;
  /** Human-readable progress text shown over the affected card. */
  label?: string;
  /** Task currently being deleted, so its confirmation action stays disabled. */
  deletingId?: string;
};

export type KanbanBoardFilters = {
  query: string;
  priority: string;
  label: string;
  assignee: string;
};

export type KanbanBoardProps = {
  tasks: Task[];
  status: Status;
  onTransition: (id: string, to: string) => AsyncBoardActionResult;
  onReorder: (id: string, rank: number) => AsyncBoardActionResult;
  onDelete?: (id: string) => AsyncBoardActionResult;
  onOpenTask: (id: string) => void;
  onNewTask: () => void;
  pending?: KanbanBoardPending;
  /** Optional bulk-selection adapter. Omit both props for the original Carbon cards. */
  selectedIds?: ReadonlySet<string>;
  onSelectionChange?: (ids: Set<string>) => void;
  /** Pass both filters and onFiltersChange to make the compact toolbar controlled. */
  filters?: KanbanBoardFilters;
  onFiltersChange?: (filters: KanbanBoardFilters) => void;
  /** Saved-view and bulk-action controls rendered before the built-in filters. */
  toolbarExtras?: ReactNode;
};

type Columns = Record<string, string[]>;
type CrossColumnMove = { activeId: string; from: string; to: string; overId: string };
const EMPTY_FILTERS: KanbanBoardFilters = { query: "", priority: "", label: "", assignee: "" };

/**
 * The compact Carbon board, deliberately independent from a query layer. Carbon can use this
 * with its scoped task collection while the legacy view below keeps its existing mutations.
 */
export function KanbanBoard({
  tasks,
  status,
  onTransition,
  onReorder,
  onDelete,
  onOpenTask,
  onNewTask,
  pending,
  selectedIds,
  onSelectionChange,
  filters,
  onFiltersChange,
  toolbarExtras,
}: KanbanBoardProps) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  const [localFilters, setLocalFilters] = useState<KanbanBoardFilters>(EMPTY_FILTERS);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [cols, setCols] = useState<Columns>({});
  const colsRef = useRef<Columns>({});
  const activeIdRef = useRef<string | null>(null);
  const lastOverRef = useRef<string | null>(null);
  const crossColumnMoveRef = useRef<CrossColumnMove | null>(null);
  const crossColumnFrameRef = useRef<number | null>(null);
  const activeFilters = filters ?? localFilters;
  const { query, priority, label, assignee } = activeFilters;

  const states = useMemo(() => status.states ?? [], [status.states]);
  const pendingLabel =
    pending?.label
    ?? (pending?.to && ((status.closed ?? []).includes(pending.to) || status.review === pending.to)
      ? t("Running checks…", "正在运行检查…")
      : t("Updating…", "正在更新…"));
  const resolvedPending = useMemo(
    () => (pending ? { ...pending, label: pendingLabel } : undefined),
    [pending, pendingLabel],
  );

  const updateFilters = useCallback(
    (patch: Partial<KanbanBoardFilters>) => {
      const next = { ...activeFilters, ...patch };
      if (!filters) setLocalFilters(next);
      onFiltersChange?.(next);
    },
    [activeFilters, filters, onFiltersChange],
  );
  const byId = useMemo(() => new Map(tasks.map((task) => [task.id, task])), [tasks]);
  const labelOpts = useMemo(
    () => [...new Set(tasks.flatMap((task) => task.labels ?? []))].sort(),
    [tasks],
  );
  const assigneeOpts = useMemo(
    () => [...new Set(tasks.map((task) => task.assignee).filter(Boolean))].sort() as string[],
    [tasks],
  );
  const prioOpts = useMemo(
    () => ["urgent", "high", "medium", "low"].filter((value) => tasks.some((task) => task.priority === value)),
    [tasks],
  );

  const deferredQuery = useDeferredValue(query);
  const normalizedQuery = deferredQuery.trim().toLowerCase();
  const visible = useMemo(
    () =>
      tasks
        .filter(
          (task) =>
            !normalizedQuery
            || task.id.toLowerCase().includes(normalizedQuery)
            || task.title.toLowerCase().includes(normalizedQuery),
        )
        .filter((task) => !label || (task.labels ?? []).includes(label))
        .filter((task) => !assignee || task.assignee === assignee)
        .filter((task) => !priority || task.priority === priority),
    [assignee, label, normalizedQuery, priority, tasks],
  );

  // Keep the optimistic order across a drop. The next task query is the authoritative
  // reconciliation point; rebuilding while the pointer is down produces a visible snap-back.
  useEffect(() => {
    if (activeIdRef.current) return;
    const next: Columns = {};
    for (const state of states) next[state] = [];
    for (const task of visible) (next[task.status] ??= []).push(task.id);
    for (const state of Object.keys(next)) {
      next[state].sort((left, right) => effectiveRank(byId.get(left)!) - effectiveRank(byId.get(right)!));
    }
    setCols((current) => {
      if (sameColumns(current, next)) return current;
      colsRef.current = next;
      return next;
    });
  }, [byId, states, visible]);

  useEffect(
    () => () => {
      if (crossColumnFrameRef.current !== null) cancelAnimationFrame(crossColumnFrameRef.current);
    },
    [],
  );

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  // pointerWithin resolves an empty column under the cursor (closestCorners misses it);
  // fall back to rectIntersection for the keyboard sensor (no pointer coordinates).
  const collisionDetection: CollisionDetection = useCallback((args) => {
    const pointer = pointerWithin(args);
    return pointer.length ? pointer : rectIntersection(args);
  }, []);

  const containerOf = useCallback((id: string, columns: Columns = colsRef.current): string | undefined => {
    if (id in columns) return id;
    return Object.keys(columns).find((column) => columns[column]?.includes(id));
  }, []);

  const applyCrossColumnMove = useCallback(() => {
    const move = crossColumnMoveRef.current;
    crossColumnMoveRef.current = null;
    if (!move) return;

    const next = moveTaskAcrossColumns(colsRef.current, move);
    if (next === colsRef.current) return;
    colsRef.current = next;
    setCols(next);
  }, []);

  const onDragStart = useCallback((event: DragStartEvent) => {
    const id = String(event.active.id);
    activeIdRef.current = id;
    lastOverRef.current = null;
    setActiveId(id);
  }, []);

  const onDragOver = useCallback(
    (event: DragOverEvent) => {
      const { active, over } = event;
      if (!over) return;
      const activeTaskId = String(active.id);
      const overId = String(over.id);
      const from = containerOf(activeTaskId);
      const to = containerOf(overId);
      if (!from || !to || from === to) return;

      const key = `${activeTaskId}:${from}:${to}:${overId}`;
      if (lastOverRef.current === key) return;
      lastOverRef.current = key;
      crossColumnMoveRef.current = { activeId: activeTaskId, from, to, overId };
      if (crossColumnFrameRef.current !== null) return;
      crossColumnFrameRef.current = requestAnimationFrame(() => {
        crossColumnFrameRef.current = null;
        applyCrossColumnMove();
      });
    },
    [applyCrossColumnMove, containerOf],
  );

  const clearDrag = useCallback(() => {
    if (crossColumnFrameRef.current !== null) {
      cancelAnimationFrame(crossColumnFrameRef.current);
      crossColumnFrameRef.current = null;
    }
    crossColumnMoveRef.current = null;
    lastOverRef.current = null;
    activeIdRef.current = null;
    setActiveId(null);
  }, []);

  const onDragEnd = useCallback(
    (event: DragEndEvent) => {
      if (crossColumnFrameRef.current !== null) {
        cancelAnimationFrame(crossColumnFrameRef.current);
        crossColumnFrameRef.current = null;
      }
      // A fast release can arrive before the scheduled cross-column update. Commit the final
      // queued location once, so rank calculation uses the visible destination rather than
      // stale pre-drag columns.
      applyCrossColumnMove();

      const { active, over } = event;
      const id = String(active.id);
      clearDrag();
      if (!over) return;
      const to = containerOf(String(over.id));
      if (!to) return;

      let list = [...(colsRef.current[to] ?? [])];
      const overIndex = list.indexOf(String(over.id));
      const currentIndex = list.indexOf(id);
      if (overIndex >= 0 && currentIndex >= 0 && overIndex !== currentIndex) {
        list = arrayMove(list, currentIndex, overIndex);
        const next = { ...colsRef.current, [to]: list };
        colsRef.current = next;
        setCols(next);
      }

      const index = list.indexOf(id);
      if (index < 0) return;
      const previousTask = index > 0 ? byId.get(list[index - 1]) : undefined;
      const nextTask = index < list.length - 1 ? byId.get(list[index + 1]) : undefined;
      const lower = previousTask ? effectiveRank(previousTask) : undefined;
      const upper = nextTask ? effectiveRank(nextTask) : undefined;
      const rank =
        lower !== undefined && upper !== undefined
          ? (lower + upper) / 2
          : lower !== undefined
            ? lower + 1
            : upper !== undefined
              ? upper - 1
              : 1;

      const task = byId.get(id);
      const reorder = () => Promise.resolve(onReorder(id, rank)).catch(() => undefined);
      if (task && task.status !== to) {
        void Promise.resolve(onTransition(id, to)).then(reorder).catch(() => undefined);
      } else {
        void reorder();
      }
    },
    [applyCrossColumnMove, byId, clearDrag, containerOf, onReorder, onTransition],
  );

  const onToggleSelection = useCallback(
    (id: string, checked: boolean) => {
      if (!onSelectionChange) return;
      const next = new Set(selectedIds ?? []);
      if (checked) next.add(id);
      else next.delete(id);
      onSelectionChange(next);
    },
    [onSelectionChange, selectedIds],
  );

  const activeTask = activeId ? byId.get(activeId) : undefined;

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2 text-[13px]">
          <span className="font-medium">{status.prefix}</span>
          <span className="text-muted-foreground">{t("Board", "看板")}</span>
        </div>
        <div className="flex items-center gap-2">
          {toolbarExtras}
          {prioOpts.length > 0 && (
            <Facet value={priority} onChange={(value) => updateFilters({ priority: value })} placeholder={t("Priority", "优先级")}>
              {prioOpts.map((value) => (
                <SelectItem key={value} value={value}>
                  <span className="flex items-center gap-2">
                    <PriorityIcon priority={value} /> {priorityLabel(value)}
                  </span>
                </SelectItem>
              ))}
            </Facet>
          )}
          {labelOpts.length > 0 && (
            <SearchableFacet
              value={label}
              onChange={(value) => updateFilters({ label: value })}
              placeholder={t("Label", "标签")}
              options={labelOpts}
            />
          )}
          {assigneeOpts.length > 0 && (
            <Facet value={assignee} onChange={(value) => updateFilters({ assignee: value })} placeholder={t("Assignee", "负责人")}>
              {assigneeOpts.map((value) => (
                <SelectItem key={value} value={value}>
                  {formatWorker(value)}
                </SelectItem>
              ))}
            </Facet>
          )}
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => updateFilters({ query: event.target.value })}
              placeholder={t("Filter…", "筛选…")}
              className="h-7 w-40 pl-7 text-xs"
            />
          </div>
          <Button size="sm" className="h-7 gap-1 px-2.5 text-xs" onClick={onNewTask}>
            <Plus className="size-3.5" /> {t("New task", "新建任务")}
          </Button>
        </div>
      </header>

      <DndContext
        sensors={sensors}
        collisionDetection={collisionDetection}
        onDragStart={onDragStart}
        onDragOver={onDragOver}
        onDragEnd={onDragEnd}
        onDragCancel={clearDrag}
      >
        {tasks.length === 0 ? (
          <EmptyState
            icon={SquareKanban}
            title={t("Your board is empty", "你的看板为空")}
            message={t("Create a task, or let your agent pick up ready work — it'll show up here.", "新建任务，或让智能体接手就绪任务——它们会显示在这里。")}
            action={{ label: t("New task", "新建任务"), icon: Plus, onClick: onNewTask }}
          />
        ) : (
          <div className="flex flex-1 gap-3 overflow-x-auto p-3">
            {states.map((state) => (
              <Column
                key={state}
                status={state}
                info={status}
                cardIds={cols[state] ?? []}
                byId={byId}
                onDelete={onDelete}
                onOpenTask={onOpenTask}
                pending={resolvedPending}
                selectedIds={selectedIds}
                onToggleSelection={onSelectionChange ? onToggleSelection : undefined}
              />
            ))}
          </div>
        )}
        <DragOverlay dropAnimation={null}>
          {activeTask ? <TaskCard task={activeTask} dragging /> : null}
        </DragOverlay>
      </DndContext>
    </div>
  );
}

export function BoardView({
  path,
  status,
  onOpenTask,
  onNewTask,
}: {
  path: string;
  status: Status;
  onOpenTask: (id: string) => void;
  onNewTask: () => void;
}) {
  const { t } = useI18n();
  const { data: tasks } = useTasks(path);
  const {
    mutateAsync: transitionTask,
    isPending: transitionPending,
    variables: transitionVariables,
  } = useTransition(path);
  const { mutateAsync: reorderTask } = useReorder(path);
  const {
    mutateAsync: removeTask,
    isPending: deletePending,
    variables: deleteVariables,
  } = useDeleteTask(path);

  const pendingTo = transitionVariables?.to;
  const pendingLabel =
    pendingTo && ((status.closed ?? []).includes(pendingTo) || status.review === pendingTo)
      ? t("Running checks…", "正在运行检查…")
      : t("Updating…", "正在更新…");
  const handleTransition = useCallback(
    (id: string, to: string) => transitionTask({ id, to }),
    [transitionTask],
  );
  const handleReorder = useCallback(
    (id: string, rank: number) => reorderTask({ id, rank }),
    [reorderTask],
  );
  const handleDelete = useCallback((id: string) => removeTask(id), [removeTask]);
  const pending = useMemo<KanbanBoardPending>(
    () => ({
      id: transitionPending ? transitionVariables?.id : undefined,
      to: transitionPending ? transitionVariables?.to : undefined,
      label: pendingLabel,
      deletingId: deletePending ? deleteVariables : undefined,
    }),
    [deletePending, deleteVariables, pendingLabel, transitionPending, transitionVariables?.id, transitionVariables?.to],
  );

  return (
    <KanbanBoard
      tasks={tasks ?? []}
      status={status}
      onTransition={handleTransition}
      onReorder={handleReorder}
      onDelete={handleDelete}
      onOpenTask={onOpenTask}
      onNewTask={onNewTask}
      pending={pending}
    />
  );
}

const Column = memo(function Column({
  status,
  info,
  cardIds,
  byId,
  onDelete,
  onOpenTask,
  pending,
  selectedIds,
  onToggleSelection,
}: {
  status: string;
  info: Status;
  cardIds: string[];
  byId: Map<string, Task>;
  onDelete?: (id: string) => AsyncBoardActionResult;
  onOpenTask: (id: string) => void;
  pending?: KanbanBoardPending;
  selectedIds?: ReadonlySet<string>;
  onToggleSelection?: (id: string, checked: boolean) => void;
}) {
  const { setNodeRef, isOver } = useDroppable({ id: status });
  return (
    <div className="flex w-72 shrink-0 flex-col">
      <div className="mb-2 flex items-center gap-2 px-1 text-[13px]">
        <StatusIcon status={status} closed={info.closed} initial={info.initial} className="size-3.5" />
        <span className="font-medium">{statusLabel(status)}</span>
        <span className="text-xs text-muted-foreground">{cardIds.length}</span>
      </div>
      <SortableContext items={cardIds} strategy={verticalListSortingStrategy}>
        <div
          ref={setNodeRef}
          className={cn(
            "flex min-h-24 flex-1 flex-col gap-2 rounded-lg p-1 transition-colors",
            isOver && "bg-foreground/[0.04]",
          )}
        >
          {cardIds.map((id) => {
            const task = byId.get(id);
            return task ? (
              <SortableCard
                key={id}
                task={task}
                onDelete={onDelete}
                onOpenTask={onOpenTask}
                busy={id === pending?.id}
                busyLabel={pending?.label}
                deleting={id === pending?.deletingId}
                selected={selectedIds?.has(id)}
                onToggleSelection={onToggleSelection}
              />
            ) : null;
          })}
        </div>
      </SortableContext>
    </div>
  );
});

const SortableCard = memo(function SortableCard({
  task,
  onDelete,
  onOpenTask,
  busy,
  busyLabel,
  deleting,
  selected,
  onToggleSelection,
}: {
  task: Task;
  onDelete?: (id: string) => AsyncBoardActionResult;
  onOpenTask: (id: string) => void;
  busy?: boolean;
  busyLabel?: string;
  deleting?: boolean;
  selected?: boolean;
  onToggleSelection?: (id: string, checked: boolean) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: task.id });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      {...attributes}
      {...listeners}
      onClick={() => !isDragging && onOpenTask(task.id)}
      role="button"
      tabIndex={0}
    >
      {isDragging ? (
        <DropIndicator />
      ) : (
        <TaskCard
          task={task}
          onDelete={onDelete}
          busy={busy}
          busyLabel={busyLabel}
          deleting={deleting}
          selected={selected}
          onToggleSelection={onToggleSelection}
        />
      )}
    </div>
  );
});

// DropIndicator marks where the dragged card will land: a blue rule with a centered ring.
function DropIndicator() {
  return (
    <div className="relative my-1 h-[3px] rounded-full bg-brand">
      <div className="absolute top-1/2 left-1/2 size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-brand bg-panel" />
    </div>
  );
}

const TaskCard = memo(function TaskCard({
  task,
  dragging,
  busy,
  busyLabel,
  deleting,
  onDelete,
  selected,
  onToggleSelection,
}: {
  task: Task;
  dragging?: boolean;
  busy?: boolean;
  busyLabel?: string;
  deleting?: boolean;
  onDelete?: (id: string) => AsyncBoardActionResult;
  selected?: boolean;
  onToggleSelection?: (id: string, checked: boolean) => void;
}) {
  const checks = task.checks ?? [];
  const passed = checks.filter((check) => check.result === "pass").length;
  const allPass = checks.length > 0 && passed === checks.length;
  return (
    <div
      aria-busy={busy}
      className={cn(
        "group/card relative cursor-pointer rounded-lg border bg-panel p-2.5 text-left shadow-xs transition-shadow hover:border-foreground/20",
        dragging && "rotate-2 shadow-md",
      )}
    >
      {busy && (
        <div className="absolute inset-0 z-10 flex items-center justify-center gap-2 rounded-lg bg-panel/70 text-xs text-muted-foreground backdrop-blur-[1px]">
          <Loader2 className="size-3 animate-spin" /> {busyLabel}
        </div>
      )}
      <div className="mb-1 flex items-center gap-1.5">
        {onToggleSelection && (
          <Checkbox
            checked={selected}
            onCheckedChange={(checked) => onToggleSelection(task.id, checked === true)}
            onClick={(event) => event.stopPropagation()}
            onPointerDown={(event) => event.stopPropagation()}
            aria-label={task.id}
            className="size-3.5"
          />
        )}
        <PriorityIcon priority={task.priority} />
        <span className="font-mono text-[11px] text-muted-foreground">{task.id}</span>
        {onDelete && <CardMenu task={task} onDelete={onDelete} pending={deleting} />}
      </div>
      <div className="text-[13px] leading-snug">{task.title}</div>
      <TaskClassification task={task} />
      {(task.labels?.length || checks.length || task.deps?.length || task.assignee) && (
        <div className="mt-2 flex items-center gap-2">
          {task.labels?.slice(0, 2).map((value) => (
            <span key={value} className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
              {value}
            </span>
          ))}
          <span className="flex-1" />
          {task.deps && task.deps.length > 0 && (
            <span className="flex items-center gap-0.5 text-[10px] text-muted-foreground">
              <GitBranch className="size-3" />
              {task.deps.length}
            </span>
          )}
          {checks.length > 0 && (
            <span className={cn("flex items-center gap-0.5 text-[10px]", allPass ? "text-success" : "text-muted-foreground")}>
              <CheckCircle2 className="size-3" />
              {passed}/{checks.length}
            </span>
          )}
          {task.assignee && <Assignee actor={task.assignee} />}
        </div>
      )}
    </div>
  );
});

function TaskClassification({ task }: { task: Task }) {
  const { t } = useI18n();
  if (!task.type && !task.importance) return null;
  return (
    <div className="mt-1 flex min-w-0 items-center gap-1 overflow-hidden">
      {task.type && (
        <span className={cn("carbon-label max-w-full truncate rounded px-1.5 py-0.5 text-[10px] text-muted-foreground", labelTone(task.type))}>
          {carbonTaskTypeLabel(task.type, t)}
        </span>
      )}
      {task.importance && (
        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
          {carbonImportanceLabel(task.importance, t)}
        </span>
      )}
    </div>
  );
}

function CardMenu({
  task,
  onDelete,
  pending,
}: {
  task: Task;
  onDelete: (id: string) => AsyncBoardActionResult;
  pending?: boolean;
}) {
  const { t } = useI18n();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const stop = (event: React.MouseEvent | React.PointerEvent) => event.stopPropagation();
  return (
    <div className="ml-auto" onPointerDown={stop} onClick={stop}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            aria-label={t("Task actions", "任务操作")}
            className="grid size-5 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground group-hover/card:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
          >
            <MoreHorizontal className="size-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuItem variant="destructive" onSelect={() => setConfirmDelete(true)}>
              <Trash2 /> {t("Delete", "删除")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDeleteDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("Delete {id}?", "删除 {id}？", { id: task.id })}
        description={
          <>
            {t("This permanently deletes ", "这将永久删除")}
            <span className="font-medium">{task.title}</span>
            {t(". Tasks with sub-tasks or dependents can't be deleted until those are removed.", "。含有子任务或依赖者的任务，需先移除它们才能删除。")}
          </>
        }
        confirmLabel={t("Delete task", "删除任务")}
        pending={pending}
        onConfirm={() => {
          void Promise.resolve(onDelete(task.id)).then(() => setConfirmDelete(false)).catch(() => undefined);
        }}
      />
    </div>
  );
}

function moveTaskAcrossColumns(columns: Columns, move: CrossColumnMove): Columns {
  const source = columns[move.from] ?? [];
  const destination = columns[move.to] ?? [];
  const sourceIndex = source.indexOf(move.activeId);
  if (sourceIndex < 0) return columns;

  const nextSource = [...source];
  nextSource.splice(sourceIndex, 1);
  const nextDestination = [...destination];
  const overIndex = nextDestination.indexOf(move.overId);
  nextDestination.splice(overIndex >= 0 ? overIndex : nextDestination.length, 0, move.activeId);
  return { ...columns, [move.from]: nextSource, [move.to]: nextDestination };
}

function sameColumns(left: Columns, right: Columns): boolean {
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  if (leftKeys.length !== rightKeys.length) return false;
  return leftKeys.every((key) => {
    const leftIds = left[key] ?? [];
    const rightIds = right[key] ?? [];
    return leftIds.length === rightIds.length && leftIds.every((id, index) => id === rightIds[index]);
  });
}
