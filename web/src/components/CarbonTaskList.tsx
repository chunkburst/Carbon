import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent, type PointerEvent, type ReactNode } from "react";
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
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Bot, CandlestickChart, CheckCircle2, ChevronRight, GitBranch, GripVertical, Inbox, LayoutGrid, List, Loader2, MoreHorizontal, Plus, Search, Trash2, UserPlus, X } from "lucide-react";
import { Assignee } from "@/components/Assignee";
import { CarbonAnimationBoard } from "@/components/CarbonAnimationBoard";
import { BoardBackgroundContextMenu } from "@/components/BoardBackgroundContextMenu";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { CarbonTaskContextMenu } from "@/components/CarbonTaskContextMenu";
import { EmptyState } from "@/components/EmptyState";
import { Facet } from "@/components/Facet";
import { SearchableFacet } from "@/components/SearchableFacet";
import { PriorityIcon, priorityLabel } from "@/components/PriorityIcon";
import { SessionStatus } from "@/components/SessionStatus";
import { StatusIcon } from "@/components/StatusIcon";
import { TaskMetadata } from "@/components/TaskMetadata";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { SelectItem } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  compareTaskOrder,
  moveTaskAcrossStatuses,
  moveTaskForDropPreview,
  rankForTaskDrop,
  type TaskDropPreviewMove,
  type TaskColumns,
} from "@/lib/task-order";
import { useI18n, type Translate } from "@/lib/i18n";
import {
  getAnimationBoardStyle,
  getBoardStatusSectionOpen,
  getTaskListPresentation,
  isAnimationBoardStyle,
  PERSONALIZATION_EVENT,
  setAnimationBoardStyle,
  setBoardStatusSectionOpen,
  setTaskListPresentation,
  type AnimationBoardStyle,
  type BoardPresentation,
  type TaskListPresentation,
} from "@/lib/personalization";
import { carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, labelTone, statusLabel, timeAgo } from "@/lib/utils";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";
import type { Status, Task } from "@/lib/api";

export type CarbonTaskListFilters = {
  query: string;
  priority: string;
  label: string;
  assignee: string;
};

export type CarbonTaskListSurface = "tasks" | "agent-work" | "board";

type AsyncTaskActionResult = Promise<unknown> | unknown;

const CARBON_TASK_DROP_ANIMATION = {
  duration: 180,
  easing: "cubic-bezier(0.22, 1, 0.36, 1)",
};

function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}

function usePrefersReducedMotion(): boolean {
  const [prefersReducedMotion, setPrefersReducedMotion] = useState(
    () => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );

  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const sync = () => setPrefersReducedMotion(query.matches);
    sync();
    query.addEventListener("change", sync);
    return () => query.removeEventListener("change", sync);
  }, []);

  return prefersReducedMotion;
}

type CarbonTaskListProps = {
  /** Stable Home/project key used for locally persisted board affordances. */
  storageKey: string;
  /** Explicit task-query scope used to seed an independent market per project/cluster view. */
  marketKey?: string;
  tasks: Task[];
  status: Status;
  loading?: boolean;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onNewTask: () => void;
  onRefresh?: () => void;
  onTransition: (task: Task, to: string) => AsyncTaskActionResult;
  onReorder: (task: Task, rank: number) => AsyncTaskActionResult;
  onTrashTask?: (task: Task) => void;
  transitioningId?: string;
  filters: CarbonTaskListFilters;
  onFiltersChange: (filters: CarbonTaskListFilters) => void;
  toolbarExtras?: ReactNode;
  /** Functional lists and the visual board are intentionally separate surfaces. */
  surface?: CarbonTaskListSurface;
  bulkMode?: boolean;
  selectedIds?: ReadonlySet<string>;
  onSelectionChange?: (ids: Set<string>) => void;
};

/**
 * Carbon keeps workflow sections vertical while letting people choose an information-
 * dense row or card presentation. The scoped transport stays outside this component
 * so drag-and-drop cannot widen a project's read/write boundary.
 */
export function CarbonTaskList({
  storageKey,
  marketKey = storageKey,
  tasks,
  status,
  loading = false,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onNewTask,
  onRefresh,
  onTransition,
  onReorder,
  onTrashTask,
  transitioningId,
  filters,
  onFiltersChange,
  toolbarExtras,
  surface = "tasks",
  bulkMode = false,
  selectedIds,
  onSelectionChange,
}: CarbonTaskListProps) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  const searchRef = useRef<HTMLInputElement>(null);
  const [keyboardIndex, setKeyboardIndex] = useState(0);
  const [pendingTrash, setPendingTrash] = useState<Task | null>(null);
  const [listPresentation, setListPresentation] = useState<TaskListPresentation>(getTaskListPresentation);
  const [animationStyle, setAnimationStyle] = useState<AnimationBoardStyle>(getAnimationBoardStyle);
  const [sectionCommand, setSectionCommand] = useState<{ revision: number; open: boolean; scopeKey: string } | null>(null);
  const [columns, setColumns] = useState<TaskColumns>({});
  const [activeId, setActiveId] = useState<string | null>(null);
  const [settlingDrop, setSettlingDrop] = useState(false);
  const columnsRef = useRef<TaskColumns>({});
  const activeIdRef = useRef<string | null>(null);
  const settlingDropRef = useRef(false);
  const lastOverRef = useRef<string | null>(null);
  const hasCrossedStatusRef = useRef(false);
  const crossStatusMoveRef = useRef<TaskDropPreviewMove | null>(null);
  const crossStatusFrameRef = useRef<number | null>(null);
  const suppressOpenIdRef = useRef<string | null>(null);
  const prefersReducedMotion = usePrefersReducedMotion();
  const presentation: BoardPresentation = surface === "board" ? "animation" : listPresentation;
  const states = useMemo(() => status.states ?? [], [status.states]);
  const closed = useMemo(() => new Set(status.closed ?? []), [status.closed]);
  const deferredQuery = useDeferredValue(filters.query);
  const debouncedQuery = useDebouncedValue(deferredQuery, 150);
  const needle = debouncedQuery.trim().toLowerCase();
  const labelOptions = useMemo(() => [...new Set(tasks.flatMap((task) => task.labels ?? []))].sort(), [tasks]);
  const assigneeOptions = useMemo(
    () => [...new Set(tasks.map((task) => task.assignee).filter(Boolean))].sort() as string[],
    [tasks],
  );
  const priorityOptions = useMemo(
    () => ["urgent", "high", "medium", "low"].filter((value) => tasks.some((task) => task.priority === value)),
    [tasks],
  );
  const visible = useMemo(
    () => tasks.filter((task) =>
      (!needle || task.id.toLowerCase().includes(needle) || task.title.toLowerCase().includes(needle))
      && (!filters.priority || task.priority === filters.priority)
      && (!filters.label || (task.labels ?? []).includes(filters.label))
      && (!filters.assignee || task.assignee === filters.assignee),
    ),
    [filters.assignee, filters.label, filters.priority, needle, tasks],
  );
  const orderedStates = useMemo(
    () => [...new Set([...states, ...visible.map((task) => task.status).filter(Boolean)])],
    [states, visible],
  );
  const byId = useMemo(() => new Map(visible.map((task) => [task.id, task])), [visible]);
  const desiredColumns = useMemo(
    () => createTaskColumns(orderedStates, visible),
    [orderedStates, visible],
  );
  const displayColumns = useMemo(
    () => Object.keys(columns).length > 0 ? columns : desiredColumns,
    [columns, desiredColumns],
  );
  const groups = useMemo(
    () => orderedStates
      .map((state) => [state, (displayColumns[state] ?? []).map((id) => byId.get(id)).filter((task): task is Task => Boolean(task))] as const)
      .filter(([, list]) => list.length > 0 || activeId !== null),
    [activeId, byId, displayColumns, orderedStates],
  );
  const flat = useMemo(() => groups.flatMap(([, list]) => list), [groups]);
  const focusedTaskId = flat[Math.min(keyboardIndex, Math.max(flat.length - 1, 0))]?.id;
  const activeFilters = Boolean(filters.query || filters.priority || filters.label || filters.assignee);
  const selected = useMemo(() => visible.filter((task) => selectedIds?.has(task.id)), [selectedIds, visible]);
  const allSelected = visible.length > 0 && selected.length === visible.length;

  useEffect(() => setKeyboardIndex(0), [filters.assignee, filters.label, filters.priority, debouncedQuery]);

  useEffect(() => {
    const syncPresentation = () => {
      setListPresentation(getTaskListPresentation());
      setAnimationStyle(getAnimationBoardStyle());
    };
    window.addEventListener(PERSONALIZATION_EVENT, syncPresentation);
    window.addEventListener("storage", syncPresentation);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncPresentation);
      window.removeEventListener("storage", syncPresentation);
    };
  }, []);

  useEffect(() => {
    // Keep a dropped arrangement in place until the scoped mutations settle. Rebuilding
    // from the query while the pointer is down (or while its request is in flight)
    // makes cards visibly jump back to their former status/rank.
    if (activeIdRef.current || settlingDropRef.current) return;
    setColumns((current) => {
      if (sameTaskColumns(current, desiredColumns)) {
        columnsRef.current = current;
        return current;
      }
      columnsRef.current = desiredColumns;
      return desiredColumns;
    });
  }, [activeId, desiredColumns, settlingDrop]);

  useEffect(
    () => () => {
      if (crossStatusFrameRef.current !== null) window.cancelAnimationFrame(crossStatusFrameRef.current);
    },
    [],
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
      if (document.querySelector('[role="dialog"],[role="listbox"],[role="menu"]')) return;
      if (event.key === "j") {
        event.preventDefault();
        setKeyboardIndex((index) => Math.min(Math.max(flat.length - 1, 0), index + 1));
      } else if (event.key === "k") {
        event.preventDefault();
        setKeyboardIndex((index) => Math.max(0, index - 1));
      } else if (event.key === "Enter" || event.key === "o") {
        const task = flat[Math.min(keyboardIndex, Math.max(flat.length - 1, 0))];
        if (task) {
          event.preventDefault();
          onOpenTask(task);
        }
      } else if (event.key === "c") {
        event.preventDefault();
        onNewTask();
      } else if (event.key === "/") {
        event.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [flat, keyboardIndex, onNewTask, onOpenTask]);

  useEffect(() => {
    if (presentation !== "animation" && focusedTaskId) {
      document.getElementById(`row-${focusedTaskId}`)?.scrollIntoView({ block: "nearest" });
    }
  }, [focusedTaskId, presentation]);

  const updateFilters = useCallback((patch: Partial<CarbonTaskListFilters>) => onFiltersChange({ ...filters, ...patch }), [filters, onFiltersChange]);
  const clearFilters = useCallback(() => onFiltersChange({ query: "", priority: "", label: "", assignee: "" }), [onFiltersChange]);
  const toggleSelection = useCallback((id: string, checked: boolean) => {
    if (!onSelectionChange) return;
    const next = new Set(selectedIds ?? []);
    if (checked) next.add(id);
    else next.delete(id);
    onSelectionChange(next);
  }, [onSelectionChange, selectedIds]);
  const toggleAllSelection = useCallback((checked: boolean) => {
    onSelectionChange?.(checked ? new Set(visible.map((task) => task.id)) : new Set());
  }, [onSelectionChange, visible]);
  const requestTrash = useCallback((task: Task) => setPendingTrash(task), []);
  const changePresentation = useCallback((next: TaskListPresentation) => {
    setListPresentation(next);
    setTaskListPresentation(next);
  }, []);
  const changeBoardSurface = useCallback((value: string) => {
    if (value === "row" || value === "card") {
      if (surface !== "board") changePresentation(value);
      return;
    }
    if (surface !== "board" || !isAnimationBoardStyle(value)) return;
    setAnimationStyle(value);
    setAnimationBoardStyle(value);
  }, [changePresentation, surface]);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const collisionDetection: CollisionDetection = useCallback((args) => {
    const pointer = pointerWithin(args);
    return pointer.length ? pointer : rectIntersection(args);
  }, []);
  const containerOf = useCallback((id: string, source: TaskColumns = columnsRef.current): string | undefined => {
    if (id in source) return id;
    return Object.keys(source).find((state) => source[state]?.includes(id));
  }, []);
  const applyCrossStatusMove = useCallback(() => {
    const move = crossStatusMoveRef.current;
    crossStatusMoveRef.current = null;
    if (!move) return;
    const next = moveTaskForDropPreview(columnsRef.current, move);
    if (next === columnsRef.current) return;
    columnsRef.current = next;
    setColumns(next);
  }, []);
  const clearDrag = useCallback(() => {
    if (crossStatusFrameRef.current !== null) {
      window.cancelAnimationFrame(crossStatusFrameRef.current);
      crossStatusFrameRef.current = null;
    }
    crossStatusMoveRef.current = null;
    lastOverRef.current = null;
    hasCrossedStatusRef.current = false;
    activeIdRef.current = null;
    setActiveId(null);
  }, []);
  const onDragStart = useCallback((event: DragStartEvent) => {
    const id = String(event.active.id);
    if (Object.keys(columnsRef.current).length === 0) columnsRef.current = desiredColumns;
    activeIdRef.current = id;
    lastOverRef.current = null;
    hasCrossedStatusRef.current = false;
    setActiveId(id);
  }, [desiredColumns]);
  const onDragOver = useCallback((event: DragOverEvent) => {
    const { active, over } = event;
    if (!over) return;
    const activeTaskId = String(active.id);
    const overId = String(over.id);
    const from = containerOf(activeTaskId);
    const to = containerOf(overId);
    if (!from || !to) return;
    // Same-status drags are rendered by SortableContext and committed on release. Once a
    // gesture has crossed a status, however, its preview must keep following later hover
    // targets even if it returns to the original column.
    if (from !== to) hasCrossedStatusRef.current = true;
    if (from === to && !hasCrossedStatusRef.current) return;

    const after = dropAfterOverItem(event, presentation);
    const key = `${activeTaskId}:${from}:${to}:${overId}:${after ? "after" : "before"}`;
    if (lastOverRef.current === key) return;
    lastOverRef.current = key;
    crossStatusMoveRef.current = {
      activeId: activeTaskId,
      from,
      to,
      overId,
      after,
      hasCrossedStatus: hasCrossedStatusRef.current,
    };
    if (crossStatusFrameRef.current !== null) return;
    crossStatusFrameRef.current = window.requestAnimationFrame(() => {
      crossStatusFrameRef.current = null;
      applyCrossStatusMove();
    });
  }, [applyCrossStatusMove, containerOf, presentation]);
  const guardedOpenTask = useCallback((task: Task) => {
    if (suppressOpenIdRef.current === task.id) return;
    onOpenTask(task);
  }, [onOpenTask]);
  const onDragEnd = useCallback((event: DragEndEvent) => {
    if (crossStatusFrameRef.current !== null) {
      window.cancelAnimationFrame(crossStatusFrameRef.current);
      crossStatusFrameRef.current = null;
    }
    // A fast release can happen before the next animation frame. Commit the queued
    // cross-status preview before calculating rank so the request matches the UI.
    applyCrossStatusMove();

    const { active, over } = event;
    const id = String(active.id);
    const task = byId.get(id);
    const to = over ? containerOf(String(over.id)) : undefined;
    if (!over || !to || !task) {
      clearDrag();
      return;
    }
    const statusChanged = task.status !== to;
    const usedCrossStatusPreview = hasCrossedStatusRef.current;

    let nextColumns = columnsRef.current;
    const currentState = containerOf(id, nextColumns);
    if (currentState && currentState !== to) {
      nextColumns = moveTaskAcrossStatuses(nextColumns, {
        activeId: id,
        from: currentState,
        to,
        overId: String(over.id),
        after: dropAfterOverItem(event, presentation),
      });
    }

    let list = [...(nextColumns[to] ?? [])];
    const overIndex = list.indexOf(String(over.id));
    const currentIndex = list.indexOf(id);
    // Cross-status preview already inserted on the correct before/after side. Running
    // same-column arrayMove again would invert that final placement on pointer release.
    if (!usedCrossStatusPreview && overIndex >= 0 && currentIndex >= 0 && overIndex !== currentIndex) {
      list = arrayMove(list, currentIndex, overIndex);
      nextColumns = { ...nextColumns, [to]: list };
    }
    if (nextColumns !== columnsRef.current) {
      columnsRef.current = nextColumns;
      setColumns(nextColumns);
    }

    const index = list.indexOf(id);
    if (index < 0) {
      clearDrag();
      return;
    }
    if (!statusChanged && sameTaskOrder(list, desiredColumns[to] ?? [])) {
      clearDrag();
      return;
    }
    // Visible filters define the interaction surface, but hidden tasks still define
    // the durable rank boundaries. This keeps a filtered drop from colliding with or
    // unexpectedly jumping across tasks that reappear when the filter is cleared.
    const rank = rankForTaskDrop(id, to, list, tasks);

    // Suppress only the synthetic click that follows a pointer drag; a normal click
    // remains the task-detail affordance in both presentations.
    suppressOpenIdRef.current = id;
    window.setTimeout(() => {
      if (suppressOpenIdRef.current === id) suppressOpenIdRef.current = null;
    }, 0);
    settlingDropRef.current = true;
    setSettlingDrop(true);
    clearDrag();
    void (async () => {
      try {
        // Status is a server-validated workflow transition. Never calculate a
        // destination rank against a state that failed to accept the task.
        if (statusChanged) await Promise.resolve(onTransition(task, to));
        await Promise.resolve(onReorder(task, rank));
      } catch {
        // Both scoped mutations restore/refetch their query caches on failure.
      } finally {
        settlingDropRef.current = false;
        setSettlingDrop(false);
      }
    })();
  }, [applyCrossStatusMove, byId, clearDrag, containerOf, desiredColumns, onReorder, onTransition, presentation, tasks]);
  const activeTask = activeId ? byId.get(activeId) : undefined;
  const surfaceTitle = surface === "board"
    ? t("Board", "看板")
    : surface === "agent-work"
      ? t("Agent work", "智能体工作")
      : t("Tasks", "任务");

  return (
    <>
      <div className="flex h-full min-w-0 flex-col">
      <header className="flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2 sm:flex-nowrap sm:py-0">
        <div className="flex min-w-0 items-center gap-1.5 text-[13px]">
          {bulkMode && (
            <Checkbox
              checked={allSelected ? true : selected.length ? "indeterminate" : false}
              aria-label={t("Select all visible tasks", "选择所有可见任务")}
              onCheckedChange={(checked) => toggleAllSelection(checked === true)}
            />
          )}
          <span className="font-medium">{status.prefix}</span>
          <ChevronRight className="size-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">{surfaceTitle}</span>
          {!loading && <span className="ml-1 text-xs text-muted-foreground">{visible.length}</span>}
        </div>
        <div className="flex min-w-0 flex-wrap items-center justify-end gap-2 sm:flex-nowrap">
          <ToggleGroup
            type="single"
            value={surface === "board" ? animationStyle : listPresentation}
            onValueChange={changeBoardSurface}
            variant="outline"
            size="sm"
            spacing={0}
            className="shrink-0"
            aria-label={surface === "board" ? t("Board style", "看板风格") : t("Task presentation", "任务展示")}
          >
            {surface === "board" ? (
              <>
                <ToggleGroupItem value="pixel-agents" aria-label={t("Work floor", "工作风")} title={t("Studio-style live work floor", "工作室经营视角的实时工作现场")}>
                  <Bot />
                  <span className="hidden xl:inline">{t("Work", "工作风")}</span>
                </ToggleGroupItem>
                <ToggleGroupItem value="market-kline" aria-label={t("Task K-line", "任务 K 线")} title={t("Task K-line", "任务 K 线")}>
                  <CandlestickChart />
                  <span className="hidden xl:inline">{t("K-line", "K 线")}</span>
                </ToggleGroupItem>
              </>
            ) : (
              <>
                <ToggleGroupItem value="row" aria-label={t("Row mode", "行模式")} title={t("Row mode", "行模式")}>
                  <List />
                  <span className="hidden xl:inline">{t("Rows", "行")}</span>
                </ToggleGroupItem>
                <ToggleGroupItem value="card" aria-label={t("Card mode", "卡片模式")} title={t("Card mode", "卡片模式")}>
                  <LayoutGrid />
                  <span className="hidden xl:inline">{t("Cards", "卡片")}</span>
                </ToggleGroupItem>
              </>
            )}
          </ToggleGroup>
          {toolbarExtras}
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              ref={searchRef}
              value={filters.query}
              onChange={(event) => updateFilters({ query: event.target.value })}
              placeholder={t("Filter ( / )", "筛选（/）")}
              className="h-7 w-40 pl-7 text-xs"
            />
          </div>
          <button type="button" className="sr-only" onClick={onNewTask}>{t("New task", "新建任务")}</button>
        </div>
      </header>

      {(priorityOptions.length > 0 || labelOptions.length > 0 || assigneeOptions.length > 0) && (
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-1.5">
          {priorityOptions.length > 0 && (
            <Facet value={filters.priority} onChange={(value) => updateFilters({ priority: value })} placeholder={t("Priority", "优先级")}>
              {priorityOptions.map((value) => (
                <SelectItem key={value} value={value}>
                  <span className="flex items-center gap-2"><PriorityIcon priority={value} />{priorityLabel(value)}</span>
                </SelectItem>
              ))}
            </Facet>
          )}
          {labelOptions.length > 0 && (
            <SearchableFacet
              value={filters.label}
              onChange={(value) => updateFilters({ label: value })}
              placeholder={t("Label", "标签")}
              options={labelOptions}
            />
          )}
          {assigneeOptions.length > 0 && (
            <Facet value={filters.assignee} onChange={(value) => updateFilters({ assignee: value })} placeholder={t("Assignee", "负责人")}>
              {assigneeOptions.map((value) => <SelectItem key={value} value={value}>{formatWorker(value)}</SelectItem>)}
            </Facet>
          )}
          {activeFilters && (
            <button type="button" className="inline-flex h-6 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground hover:bg-muted hover:text-foreground" onClick={clearFilters}>
              <X className="size-3" />{t("Clear", "清除")}
            </button>
          )}
        </div>
      )}

      <DndContext
        sensors={sensors}
        collisionDetection={collisionDetection}
        onDragStart={onDragStart}
        onDragOver={onDragOver}
        onDragEnd={onDragEnd}
        onDragCancel={clearDrag}
      >
        <BoardBackgroundContextMenu
          className={cn(
            "flex-1 overscroll-contain py-1",
            surface === "board" ? "flex min-h-0 flex-col overflow-hidden" : "overflow-y-auto",
          )}
          presentation={presentation}
          surface={surface}
          animationStyle={animationStyle}
          onNewTask={onNewTask}
          onSearch={() => searchRef.current?.focus()}
          onRefresh={onRefresh ?? (() => undefined)}
          onExpandAll={() => setSectionCommand((current) => ({ revision: (current?.revision ?? 0) + 1, open: true, scopeKey: storageKey }))}
          onCollapseAll={() => setSectionCommand((current) => ({ revision: (current?.revision ?? 0) + 1, open: false, scopeKey: storageKey }))}
          onClearFilters={activeFilters ? clearFilters : undefined}
          onPresentationChange={changePresentation}
          onAnimationStyleChange={changeBoardSurface}
        >
          {loading ? (
            <div className="space-y-1.5 px-3 py-2">
              {Array.from({ length: 8 }).map((_, index) => <Skeleton key={index} className="h-8 w-full" />)}
            </div>
          ) : tasks.length === 0 ? (
            <EmptyState
              icon={Inbox}
              title={t("Your task list is empty", "任务列表为空")}
              message={t("Create a task to start this project.", "新建一个任务，开始推进这个项目。")}
              action={{ label: t("New task", "新建任务"), icon: Plus, onClick: onNewTask }}
            />
          ) : visible.length === 0 ? (
            <EmptyState
              icon={Inbox}
              title={t("No matching tasks", "没有匹配的任务")}
              message={t("Clear a filter or create a task.", "请清除筛选条件，或新建一个任务。")}
              action={{ label: t("Clear filters", "清除筛选"), icon: X, onClick: clearFilters }}
            />
          ) : presentation === "animation" ? (
            <CarbonAnimationBoard
              projectKey={marketKey}
              tasks={visible}
              status={status}
              style={animationStyle}
              onOpenTask={guardedOpenTask}
              prefersReducedMotion={prefersReducedMotion}
            />
          ) : (
            groups.map(([state, list]) => (
              <CarbonStatusSection
                key={state}
                state={state}
                tasks={list}
                status={status}
                storageKey={storageKey}
                presentation={presentation}
                sectionCommand={sectionCommand}
                defaultOpen={!closed.has(state)}
                forceOpen={activeId !== null}
                dragDisabled={settlingDrop}
                prefersReducedMotion={prefersReducedMotion}
                focusedTaskId={focusedTaskId}
                transitioningId={transitioningId}
                bulkMode={bulkMode}
                selectedIds={selectedIds}
                onOpenTask={guardedOpenTask}
                onOpenWorker={onOpenWorker}
                taskHref={taskHref}
                onTransition={onTransition}
                onTrashTask={onTrashTask ? requestTrash : undefined}
                onToggleSelection={toggleSelection}
              />
            ))
          )}
        </BoardBackgroundContextMenu>
        <DragOverlay dropAnimation={prefersReducedMotion ? null : CARBON_TASK_DROP_ANIMATION}>
          {activeTask ? <CarbonTaskDragPreview task={activeTask} presentation={presentation} /> : null}
        </DragOverlay>
        </DndContext>
    </div>
      <ConfirmDeleteDialog
      open={pendingTrash !== null}
      onOpenChange={(open) => {
        if (!open) setPendingTrash(null);
      }}
      title={pendingTrash ? t("Move {id} to trash?", "将 {id} 移入回收站？", { id: pendingTrash.id }) : ""}
      description={t("The task can be restored from the trash later.", "任务会进入回收站，之后仍可恢复。")}
      confirmLabel={t("Move to trash", "移入回收站")}
      onConfirm={() => {
        if (pendingTrash) onTrashTask?.(pendingTrash);
        setPendingTrash(null);
      }}
      />
    </>
  );
}

const CarbonStatusSection = memo(function CarbonStatusSection({
  state,
  tasks,
  status,
  storageKey,
  presentation,
  sectionCommand,
  defaultOpen,
  forceOpen,
  dragDisabled,
  prefersReducedMotion,
  focusedTaskId,
  transitioningId,
  bulkMode,
  selectedIds,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onTransition,
  onTrashTask,
  onToggleSelection,
}: {
  state: string;
  tasks: Task[];
  status: Status;
  storageKey: string;
  presentation: BoardPresentation;
  sectionCommand: { revision: number; open: boolean; scopeKey: string } | null;
  defaultOpen: boolean;
  forceOpen: boolean;
  dragDisabled: boolean;
  prefersReducedMotion: boolean;
  focusedTaskId?: string;
  transitioningId?: string;
  bulkMode: boolean;
  selectedIds?: ReadonlySet<string>;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => AsyncTaskActionResult;
  onTrashTask?: (task: Task) => void;
  onToggleSelection: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(() => getBoardStatusSectionOpen(storageKey, state, defaultOpen));
  const { setNodeRef, isOver } = useDroppable({ id: state });
  useEffect(() => {
    setOpen(getBoardStatusSectionOpen(storageKey, state, defaultOpen));
  }, [defaultOpen, state, storageKey]);
  const changeOpen = useCallback((next: boolean) => {
    setOpen(next);
    setBoardStatusSectionOpen(storageKey, state, next);
  }, [state, storageKey]);
  useEffect(() => {
    if (!sectionCommand || sectionCommand.scopeKey !== storageKey) return;
    changeOpen(sectionCommand.open);
  }, [changeOpen, sectionCommand, storageKey]);
  const expanded = forceOpen || open;
  return (
    <Collapsible open={expanded} onOpenChange={changeOpen} className="mb-1">
      <CollapsibleTrigger asChild>
        <button type="button" className="flex h-9 w-full items-center gap-2 px-3 text-left text-[13px] hover:bg-foreground/[0.02]">
          <ChevronRight className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none", expanded && "rotate-90")} />
          <StatusIcon status={state} closed={status.closed} initial={status.initial} className="size-3.5" />
          <span className="font-medium">{localizedStatusLabel(state, t)}</span>
          <span className="text-xs text-muted-foreground">{tasks.length}</span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent style={{ contentVisibility: "auto", containIntrinsicSize: "auto 32px" }}>
        {presentation === "card" ? (
          <SortableContext items={tasks.map((task) => task.id)} strategy={rectSortingStrategy}>
            <div
              ref={setNodeRef}
              className={cn(
                "grid min-h-12 gap-2 px-3 pb-2 pt-1 transition-colors motion-reduce:transition-none sm:grid-cols-2 2xl:grid-cols-3",
                isOver && "bg-brand/[0.035]",
              )}
            >
              {tasks.map((task) => (
                <SortableCarbonTask
                  key={task.id}
                  task={task}
                  status={status}
                  presentation={presentation}
                  selected={task.id === focusedTaskId}
                  bulkMode={bulkMode}
                  bulkSelected={selectedIds?.has(task.id)}
                  transitioning={task.id === transitioningId}
                  dragDisabled={dragDisabled}
                  prefersReducedMotion={prefersReducedMotion}
                  onOpenTask={onOpenTask}
                  onOpenWorker={onOpenWorker}
                  taskHref={taskHref}
                  onTransition={onTransition}
                  onTrashTask={onTrashTask}
                  onToggleSelection={onToggleSelection}
                />
              ))}
            </div>
          </SortableContext>
        ) : (
          <SortableContext items={tasks.map((task) => task.id)} strategy={verticalListSortingStrategy}>
            <div
              ref={setNodeRef}
              className={cn(
                "min-h-8 transition-colors motion-reduce:transition-none",
                isOver && "bg-brand/[0.035]",
              )}
            >
              {tasks.map((task) => (
                <SortableCarbonTask
                  key={task.id}
                  task={task}
                  status={status}
                  presentation={presentation}
                  selected={task.id === focusedTaskId}
                  bulkMode={bulkMode}
                  bulkSelected={selectedIds?.has(task.id)}
                  transitioning={task.id === transitioningId}
                  dragDisabled={dragDisabled}
                  prefersReducedMotion={prefersReducedMotion}
                  onOpenTask={onOpenTask}
                  onOpenWorker={onOpenWorker}
                  taskHref={taskHref}
                  onTransition={onTransition}
                  onTrashTask={onTrashTask}
                  onToggleSelection={onToggleSelection}
                />
              ))}
            </div>
          </SortableContext>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
});

const SortableCarbonTask = memo(function SortableCarbonTask({
  task,
  status,
  presentation,
  selected,
  bulkMode,
  bulkSelected,
  transitioning,
  dragDisabled,
  prefersReducedMotion,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onTransition,
  onTrashTask,
  onToggleSelection,
}: {
  task: Task;
  status: Status;
  presentation: BoardPresentation;
  selected: boolean;
  bulkMode: boolean;
  bulkSelected?: boolean;
  transitioning: boolean;
  dragDisabled: boolean;
  prefersReducedMotion: boolean;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => AsyncTaskActionResult;
  onTrashTask?: (task: Task) => void;
  onToggleSelection: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition, isDragging } = useSortable({
    id: task.id,
    disabled: dragDisabled,
  });
  const dragHandle = (
    <span className="shrink-0" onClick={(event) => event.stopPropagation()}>
      <button
        ref={setActivatorNodeRef}
        type="button"
        disabled={dragDisabled}
        aria-label={t("Drag task {id}", "拖动任务 {id}", { id: task.id })}
        className="grid size-4 cursor-grab place-items-center rounded text-muted-foreground/80 opacity-45 outline-none transition-opacity hover:bg-muted hover:text-foreground hover:opacity-100 focus-visible:opacity-100 active:cursor-grabbing group-hover:opacity-100 disabled:cursor-wait disabled:opacity-25 motion-reduce:transition-none"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-3" />
      </button>
    </span>
  );
  const style = {
    transform: CSS.Transform.toString(transform),
    transition: prefersReducedMotion ? undefined : transition,
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={cn(
        "relative origin-center motion-safe:transition-[transform,opacity] motion-safe:duration-200 motion-safe:ease-out motion-reduce:transition-none",
        isDragging && "pointer-events-none opacity-0",
      )}
    >
      {presentation === "card" ? (
        <CarbonTaskCard
          task={task}
          status={status}
          selected={selected}
          bulkMode={bulkMode}
          bulkSelected={bulkSelected}
          transitioning={transitioning}
          dragHandle={dragHandle}
          onOpenTask={onOpenTask}
          onOpenWorker={onOpenWorker}
          taskHref={taskHref}
          onTransition={onTransition}
          onTrashTask={onTrashTask}
          onToggleSelection={onToggleSelection}
        />
      ) : (
        <CarbonTaskRow
          task={task}
          status={status}
          selected={selected}
          bulkMode={bulkMode}
          bulkSelected={bulkSelected}
          transitioning={transitioning}
          dragHandle={dragHandle}
          onOpenTask={onOpenTask}
          onOpenWorker={onOpenWorker}
          taskHref={taskHref}
          onTransition={onTransition}
          onTrashTask={onTrashTask}
          onToggleSelection={onToggleSelection}
        />
      )}
    </div>
  );
});

const CarbonTaskRow = memo(function CarbonTaskRow({
  task,
  status,
  selected,
  bulkMode,
  bulkSelected,
  transitioning,
  dragHandle,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onTransition,
  onTrashTask,
  onToggleSelection,
}: {
  task: Task;
  status: Status;
  selected: boolean;
  bulkMode: boolean;
  bulkSelected?: boolean;
  transitioning: boolean;
  dragHandle?: ReactNode;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => AsyncTaskActionResult;
  onTrashTask?: (task: Task) => void;
  onToggleSelection: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const checks = task.checks ?? [];
  const passed = checks.filter((check) => check.result === "pass").length;
  const allPassed = checks.length > 0 && checks.length === passed;
  const missingVersion = !task.version;
  const importance = carbonImportanceLabel(task.importance ?? "normal", t);
  const stop = (event: MouseEvent | PointerEvent) => event.stopPropagation();
  const triggerTransition = (next: string) => {
    void Promise.resolve(onTransition(task, next)).catch(() => undefined);
  };
  const openKeyboardContextMenu = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10")) return false;
    event.preventDefault();
    event.stopPropagation();
    const rect = event.currentTarget.getBoundingClientRect();
    event.currentTarget.dispatchEvent(new window.MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + Math.min(24, rect.width / 2),
      clientY: rect.top + Math.min(24, rect.height / 2),
    }));
    return true;
  };
  return (
    <CarbonTaskContextMenu
      task={task}
      status={status}
      transitioning={transitioning}
      taskHref={taskHref}
      statusLabel={(value) => localizedStatusLabel(value, t)}
      onOpenTask={onOpenTask}
      onTransition={(_task, next) => triggerTransition(next)}
      onTrashTask={onTrashTask}
    >
      <div
      id={`row-${task.id}`}
      data-carbon-task-surface
      role="button"
      tabIndex={0}
      aria-busy={transitioning}
      aria-haspopup="menu"
      aria-label={t("Open task {id}: {title}", "打开任务 {id}：{title}", { id: task.id, title: task.title })}
      onClick={() => onOpenTask(task)}
      onKeyDown={(event) => {
        if (event.target !== event.currentTarget) return;
        if (openKeyboardContextMenu(event)) return;
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onOpenTask(task);
        }
      }}
      className={cn(
        "group flex h-8 w-full cursor-pointer items-center gap-2 px-3 text-left text-[13px] transition-colors hover:bg-foreground/[0.04] focus-visible:bg-foreground/[0.04] focus-visible:outline-none motion-reduce:transition-none",
        selected && "bg-foreground/[0.06] ring-1 ring-inset ring-brand/40",
      )}
      style={{ contentVisibility: "auto", containIntrinsicSize: "auto 32px" }}
    >
      {dragHandle}
      {bulkMode && (
        <Checkbox
          checked={bulkSelected}
          aria-label={t("Select {id}", "选择 {id}", { id: task.id })}
          onClick={stop}
          onPointerDown={stop}
          onCheckedChange={(checked) => onToggleSelection(task.id, checked === true)}
          className="shrink-0"
        />
      )}
      <PriorityIcon priority={task.priority} className="shrink-0" />
      <DropdownMenu>
        <DropdownMenuTrigger asChild onClick={stop}>
          <button
            type="button"
            aria-label={t("Change status", "修改状态")}
            disabled={transitioning}
            className="grid size-4 shrink-0 place-items-center rounded hover:ring-2 hover:ring-foreground/10 disabled:cursor-not-allowed disabled:opacity-45"
          >
            {transitioning ? <Loader2 className="size-3 animate-spin text-brand" /> : <StatusIcon status={task.status} closed={status.closed} initial={status.initial} className="size-3.5" />}
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" onClick={stop}>
          <DropdownMenuGroup>
            {(status.states ?? []).map((next) => (
              <DropdownMenuItem key={next} disabled={next === task.status || transitioning} onSelect={(event) => { event.stopPropagation(); triggerTransition(next); }}>
                <StatusIcon status={next} closed={status.closed} initial={status.initial} />
                {localizedStatusLabel(next, t)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <span className="w-20 shrink-0 truncate font-mono text-xs whitespace-nowrap text-muted-foreground">{task.id}</span>
      <span className="min-w-24 flex-1 truncate">{task.title}</span>
      <Badge
        variant="outline"
        className="h-5 shrink-0 px-1.5 text-[10px] font-medium"
        title={t("Importance: {importance}", "重要性：{importance}", { importance })}
      >
        {t("Importance: {importance}", "重要性：{importance}", { importance })}
      </Badge>
      {task.labels?.slice(0, 2).map((label) => (
        <Badge key={label} variant="secondary" className={cn("carbon-label hidden h-4 shrink-0 px-1.5 text-[10px] font-normal sm:inline-flex", labelTone(label))}>
          {label}
        </Badge>
      ))}
      {task.labels && task.labels.length > 2 && <span className="hidden shrink-0 text-[10px] text-muted-foreground sm:inline">+{task.labels.length - 2}</span>}
      <TaskMetadata task={task} compact className="hidden shrink-0 2xl:flex" />
      <SessionStatus state={task.executionState} compact />
      {task.ready && task.status === status.initial && (
        <Tooltip>
          <TooltipTrigger asChild><span className="size-1.5 shrink-0 rounded-full bg-brand" /></TooltipTrigger>
          <TooltipContent>{t("Ready to start — dependencies closed", "可以开始——依赖已关闭")}</TooltipContent>
        </Tooltip>
      )}
      {task.deps && task.deps.length > 0 && <span className="hidden shrink-0 items-center gap-1 text-xs text-muted-foreground sm:flex"><GitBranch className="size-3" />{task.deps.length}</span>}
      {checks.length > 0 && <span className={cn("hidden shrink-0 items-center gap-1 text-xs tabular-nums sm:flex", allPassed ? "text-success" : "text-muted-foreground")}><CheckCircle2 className="size-3" />{passed}/{checks.length}</span>}
      {task.updatedAt && <span className="hidden shrink-0 text-xs text-muted-foreground tabular-nums xl:block">{timeAgo(task.updatedAt)}</span>}
      {task.assignee ? (
        <Assignee actor={task.assignee} className="shrink-0" onOpenWorker={onOpenWorker} />
      ) : (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t("Ask to take over", "申请接手")}
              onClick={(event) => { stop(event); onOpenTask(task); }}
              className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100"
            >
              <UserPlus className="size-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>{t("Open the task to ask to take it over", "打开任务后即可申请接手")}</TooltipContent>
        </Tooltip>
      )}
      {onTrashTask && (
        <div className="shrink-0" onPointerDown={stop} onClick={stop}>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t("Task actions", "任务操作")}
                className="grid size-5 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
              >
                <MoreHorizontal className="size-3.5" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                variant="destructive"
                disabled={missingVersion}
                onSelect={(event) => {
                  event.stopPropagation();
                  onTrashTask(task);
                }}
              >
                <Trash2 /> {t("Move to trash", "移入回收站")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}
      </div>
    </CarbonTaskContextMenu>
  );
});

const CarbonTaskCard = memo(function CarbonTaskCard({
  task,
  status,
  selected,
  bulkMode,
  bulkSelected,
  transitioning,
  dragHandle,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onTransition,
  onTrashTask,
  onToggleSelection,
}: {
  task: Task;
  status: Status;
  selected: boolean;
  bulkMode: boolean;
  bulkSelected?: boolean;
  transitioning: boolean;
  dragHandle?: ReactNode;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => AsyncTaskActionResult;
  onTrashTask?: (task: Task) => void;
  onToggleSelection: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const checks = task.checks ?? [];
  const passed = checks.filter((check) => check.result === "pass").length;
  const allPassed = checks.length > 0 && checks.length === passed;
  const missingVersion = !task.version;
  const importance = carbonImportanceLabel(task.importance ?? "normal", t);
  const stop = (event: MouseEvent | PointerEvent) => event.stopPropagation();
  const triggerTransition = (next: string) => {
    void Promise.resolve(onTransition(task, next)).catch(() => undefined);
  };
  const openKeyboardContextMenu = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10")) return false;
    event.preventDefault();
    event.stopPropagation();
    const rect = event.currentTarget.getBoundingClientRect();
    event.currentTarget.dispatchEvent(new window.MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + Math.min(24, rect.width / 2),
      clientY: rect.top + Math.min(24, rect.height / 2),
    }));
    return true;
  };

  return (
    <CarbonTaskContextMenu
      task={task}
      status={status}
      transitioning={transitioning}
      taskHref={taskHref}
      statusLabel={(value) => localizedStatusLabel(value, t)}
      onOpenTask={onOpenTask}
      onTransition={(_task, next) => triggerTransition(next)}
      onTrashTask={onTrashTask}
    >
      <article
        id={`row-${task.id}`}
        data-carbon-task-surface
        role="button"
        tabIndex={0}
        aria-busy={transitioning}
        aria-haspopup="menu"
        aria-label={t("Open task {id}: {title}", "打开任务 {id}：{title}", { id: task.id, title: task.title })}
        onClick={() => onOpenTask(task)}
        onKeyDown={(event) => {
          if (event.target !== event.currentTarget) return;
          if (openKeyboardContextMenu(event)) return;
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            onOpenTask(task);
          }
        }}
        className={cn(
          "group flex min-h-40 cursor-pointer flex-col gap-3 rounded-xl border bg-card p-3 text-left shadow-sm transition-colors hover:bg-muted/35 focus-visible:bg-muted/35 focus-visible:outline-none motion-reduce:transition-none",
          selected && "ring-2 ring-brand/40",
        )}
        style={{ contentVisibility: "auto", containIntrinsicSize: "auto 160px" }}
      >
        <div className="flex min-w-0 items-center gap-2">
          {dragHandle}
          {bulkMode && (
            <Checkbox
              checked={bulkSelected}
              aria-label={t("Select {id}", "选择 {id}", { id: task.id })}
              onClick={stop}
              onPointerDown={stop}
              onCheckedChange={(checked) => onToggleSelection(task.id, checked === true)}
              className="shrink-0"
            />
          )}
          <PriorityIcon priority={task.priority} className="shrink-0" />
          <DropdownMenu>
            <DropdownMenuTrigger asChild onClick={stop}>
              <button
                type="button"
                aria-label={t("Change status", "修改状态")}
                disabled={transitioning}
                onPointerDown={stop}
                className="grid size-5 shrink-0 place-items-center rounded hover:ring-2 hover:ring-foreground/10 disabled:cursor-not-allowed disabled:opacity-45"
              >
                {transitioning ? <Loader2 className="size-3 animate-spin text-brand" /> : <StatusIcon status={task.status} closed={status.closed} initial={status.initial} className="size-4" />}
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" onClick={stop}>
              <DropdownMenuGroup>
                {(status.states ?? []).map((next) => (
                  <DropdownMenuItem key={next} disabled={next === task.status || transitioning} onSelect={(event) => { event.stopPropagation(); triggerTransition(next); }}>
                    <StatusIcon status={next} closed={status.closed} initial={status.initial} />
                    {localizedStatusLabel(next, t)}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">{task.id}</span>
          <SessionStatus state={task.executionState} compact />
          {onTrashTask && (
            <div className="shrink-0" onPointerDown={stop} onClick={stop}>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    aria-label={t("Task actions", "任务操作")}
                    className="grid size-6 place-items-center rounded text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
                  >
                    <MoreHorizontal className="size-3.5" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuGroup>
                    <DropdownMenuItem
                      variant="destructive"
                      disabled={missingVersion}
                      onSelect={(event) => {
                        event.stopPropagation();
                        onTrashTask(task);
                      }}
                    >
                      <Trash2 /> {t("Move to trash", "移入回收站")}
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
        </div>

        <p className="line-clamp-2 text-sm font-medium leading-snug">{task.title}</p>

        <div className="flex flex-wrap items-center gap-1.5">
          <Badge
            variant="outline"
            className="h-5 px-1.5 text-[10px] font-medium"
            title={t("Importance: {importance}", "重要性：{importance}", { importance })}
          >
            {t("Importance: {importance}", "重要性：{importance}", { importance })}
          </Badge>
          {task.type && (
            <Badge variant="secondary" className={cn("carbon-label h-5 px-1.5 text-[10px] font-medium", labelTone(task.type))}>
              {carbonTaskTypeLabel(task.type, t)}
            </Badge>
          )}
          {task.labels?.slice(0, 3).map((label) => (
            <Badge key={label} variant="secondary" className={cn("carbon-label h-5 max-w-28 px-1.5 text-[10px] font-medium", labelTone(label))}>
              <span className="truncate">{label}</span>
            </Badge>
          ))}
          {task.labels && task.labels.length > 3 && <Badge variant="outline" className="h-5 px-1.5 text-[10px]">+{task.labels.length - 3}</Badge>}
        </div>

        <div className="mt-auto flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
          {task.ready && task.status === status.initial && <span className="size-1.5 shrink-0 rounded-full bg-brand" aria-label={t("Ready to start", "可以开始")} />}
          {task.deps && task.deps.length > 0 && <span className="inline-flex shrink-0 items-center gap-1"><GitBranch className="size-3" />{task.deps.length}</span>}
          {checks.length > 0 && <span className={cn("inline-flex shrink-0 items-center gap-1 tabular-nums", allPassed ? "text-success" : "text-muted-foreground")}><CheckCircle2 className="size-3" />{passed}/{checks.length}</span>}
          {task.updatedAt && <span className="min-w-0 flex-1 truncate tabular-nums">{timeAgo(task.updatedAt)}</span>}
          {task.assignee ? (
            <Assignee actor={task.assignee} className="shrink-0" onOpenWorker={onOpenWorker} />
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={t("Ask to take over", "申请接手")}
                  onClick={(event) => { stop(event); onOpenTask(task); }}
                  onPointerDown={stop}
                  className="grid size-6 shrink-0 place-items-center rounded text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
                >
                  <UserPlus className="size-3.5" />
                </button>
              </TooltipTrigger>
              <TooltipContent>{t("Open the task to ask to take it over", "打开任务后即可申请接手")}</TooltipContent>
            </Tooltip>
          )}
        </div>
      </article>
    </CarbonTaskContextMenu>
  );
});

function CarbonTaskDragPreview({ task, presentation }: { task: Task; presentation: BoardPresentation }) {
  return presentation === "card" ? (
    <article className="w-[min(20rem,calc(100vw-2rem))] rotate-[0.5deg] rounded-xl border border-brand/25 bg-card p-3 shadow-lg">
      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <GripVertical className="size-3 shrink-0" />
        <span className="truncate font-mono">{task.id}</span>
      </div>
      <p className="mt-2 line-clamp-2 text-sm font-medium leading-snug">{task.title}</p>
    </article>
  ) : (
    <div className="flex w-[min(42rem,calc(100vw-2rem))] items-center gap-2 rounded-md border border-brand/25 bg-card px-3 py-2 text-[13px] shadow-lg">
      <GripVertical className="size-3 shrink-0 text-muted-foreground" />
      <span className="w-20 shrink-0 truncate font-mono text-xs text-muted-foreground">{task.id}</span>
      <span className="min-w-0 flex-1 truncate font-medium">{task.title}</span>
    </div>
  );
}

function createTaskColumns(states: readonly string[], tasks: readonly Task[]): TaskColumns {
  const next: TaskColumns = Object.fromEntries(states.map((state) => [state, []]));
  for (const task of tasks) (next[task.status] ??= []).push(task.id);
  const byId = new Map(tasks.map((task) => [task.id, task]));
  for (const list of Object.values(next)) {
    list.sort((left, right) => compareTaskOrder(byId.get(left)!, byId.get(right)!));
  }
  return next;
}

function dropAfterOverItem(event: DragOverEvent | DragEndEvent, presentation: BoardPresentation): boolean {
  if (!event.over || String(event.over.id) === String(event.active.id)) return false;
  const translated = event.active.rect.current.translated;
  if (!translated) return false;
  const over = event.over.rect;
  const activeCenterY = translated.top + translated.height / 2;
  const overCenterY = over.top + over.height / 2;
  if (presentation !== "card" || Math.abs(activeCenterY - overCenterY) >= over.height / 2) {
    return activeCenterY > overCenterY;
  }
  return translated.left + translated.width / 2 > over.left + over.width / 2;
}

function sameTaskOrder(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((id, index) => id === right[index]);
}

function sameTaskColumns(left: TaskColumns, right: TaskColumns): boolean {
  const leftStates = Object.keys(left);
  const rightStates = Object.keys(right);
  return leftStates.length === rightStates.length && leftStates.every((state) => {
    const leftIds = left[state] ?? [];
    const rightIds = right[state] ?? [];
    return leftIds.length === rightIds.length && leftIds.every((id, index) => id === rightIds[index]);
  });
}

function localizedStatusLabel(state: string, t: Translate): string {
  const normalized = state.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const labels: Record<string, [string, string]> = {
    backlog: ["Backlog", "待办"],
    todo: ["To do", "待办"],
    to_do: ["To do", "待办"],
    in_progress: ["In progress", "进行中"],
    active: ["Active", "进行中"],
    review: ["In review", "审核中"],
    awaiting_review: ["Awaiting review", "等待审核"],
    done: ["Done", "已完成"],
    completed: ["Completed", "已完成"],
    closed: ["Closed", "已关闭"],
    blocked: ["Blocked", "受阻"],
  };
  const label = labels[normalized];
  return label ? t(...label) : statusLabel(state);
}
