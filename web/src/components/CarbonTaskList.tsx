import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent, type PointerEvent, type ReactNode } from "react";
import { CheckCircle2, ChevronRight, GitBranch, Inbox, Loader2, MoreHorizontal, Plus, Search, Trash2, UserPlus, X } from "lucide-react";
import { Assignee } from "@/components/Assignee";
import { BoardBackgroundContextMenu } from "@/components/BoardBackgroundContextMenu";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { CarbonTaskContextMenu } from "@/components/CarbonTaskContextMenu";
import { EmptyState } from "@/components/EmptyState";
import { Facet } from "@/components/Facet";
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
import { effectiveRank } from "@/lib/filter";
import { useI18n, type Translate } from "@/lib/i18n";
import {
  getBoardPresentation,
  getBoardStatusSectionOpen,
  PERSONALIZATION_EVENT,
  setBoardPresentation,
  setBoardStatusSectionOpen,
  type BoardPresentation,
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

function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}

type CarbonTaskListProps = {
  /** Stable Home/project key used for locally persisted board affordances. */
  storageKey: string;
  tasks: Task[];
  status: Status;
  loading?: boolean;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onNewTask: () => void;
  onRefresh?: () => void;
  onTransition: (task: Task, to: string) => void;
  onTrashTask?: (task: Task) => void;
  transitioningId?: string;
  filters: CarbonTaskListFilters;
  onFiltersChange: (filters: CarbonTaskListFilters) => void;
  toolbarExtras?: ReactNode;
  bulkMode?: boolean;
  selectedIds?: ReadonlySet<string>;
  onSelectionChange?: (ids: Set<string>) => void;
};

/**
 * The Carbon task surface deliberately uses the original Carbon information-dense
 * list treatment: workflow sections stack vertically and every task occupies one
 * row. Carbon keeps the scoped transport outside this component so it cannot widen
 * a project's read/write boundary.
 */
export function CarbonTaskList({
  storageKey,
  tasks,
  status,
  loading = false,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onNewTask,
  onRefresh,
  onTransition,
  onTrashTask,
  transitioningId,
  filters,
  onFiltersChange,
  toolbarExtras,
  bulkMode = false,
  selectedIds,
  onSelectionChange,
}: CarbonTaskListProps) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  const searchRef = useRef<HTMLInputElement>(null);
  const [keyboardIndex, setKeyboardIndex] = useState(0);
  const [pendingTrash, setPendingTrash] = useState<Task | null>(null);
  const [presentation, setPresentation] = useState<BoardPresentation>(getBoardPresentation);
  const [sectionCommand, setSectionCommand] = useState<{ revision: number; open: boolean; scopeKey: string } | null>(null);
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
  const byStatus = useMemo(() => {
    const grouped = new Map<string, Task[]>(states.map((state) => [state, []]));
    for (const task of visible) {
      if (!grouped.has(task.status)) grouped.set(task.status, []);
      grouped.get(task.status)!.push(task);
    }
    for (const list of grouped.values()) {
      list.sort((left, right) => effectiveRank(left) - effectiveRank(right) || left.id.localeCompare(right.id));
    }
    return grouped;
  }, [states, visible]);
  const groups = useMemo(() => [...byStatus.entries()].filter(([, list]) => list.length > 0), [byStatus]);
  const flat = useMemo(() => groups.flatMap(([, list]) => list), [groups]);
  const focusedTaskId = flat[Math.min(keyboardIndex, Math.max(flat.length - 1, 0))]?.id;
  const activeFilters = Boolean(filters.query || filters.priority || filters.label || filters.assignee);
  const selected = useMemo(() => visible.filter((task) => selectedIds?.has(task.id)), [selectedIds, visible]);
  const allSelected = visible.length > 0 && selected.length === visible.length;

  useEffect(() => setKeyboardIndex(0), [filters.assignee, filters.label, filters.priority, debouncedQuery]);

  useEffect(() => {
    const syncPresentation = () => setPresentation(getBoardPresentation());
    window.addEventListener(PERSONALIZATION_EVENT, syncPresentation);
    window.addEventListener("storage", syncPresentation);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncPresentation);
      window.removeEventListener("storage", syncPresentation);
    };
  }, []);

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
    if (focusedTaskId) document.getElementById(`row-${focusedTaskId}`)?.scrollIntoView({ block: "nearest" });
  }, [focusedTaskId]);

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
  const changePresentation = useCallback((next: BoardPresentation) => {
    setPresentation(next);
    setBoardPresentation(next);
  }, []);

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
          <span className="text-muted-foreground">{t("Tasks", "任务")}</span>
          {!loading && <span className="ml-1 text-xs text-muted-foreground">{visible.length}</span>}
        </div>
        <div className="flex min-w-0 flex-wrap items-center justify-end gap-2 sm:flex-nowrap">
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
            <Facet value={filters.label} onChange={(value) => updateFilters({ label: value })} placeholder={t("Label", "标签")}>
              {labelOptions.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
            </Facet>
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

      <BoardBackgroundContextMenu
        className="flex-1 overflow-y-auto overscroll-contain py-1"
        presentation={presentation}
        onNewTask={onNewTask}
        onSearch={() => searchRef.current?.focus()}
        onRefresh={onRefresh ?? (() => undefined)}
        onExpandAll={() => setSectionCommand((current) => ({ revision: (current?.revision ?? 0) + 1, open: true, scopeKey: storageKey }))}
        onCollapseAll={() => setSectionCommand((current) => ({ revision: (current?.revision ?? 0) + 1, open: false, scopeKey: storageKey }))}
        onClearFilters={activeFilters ? clearFilters : undefined}
        onPresentationChange={changePresentation}
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
              focusedTaskId={focusedTaskId}
              transitioningId={transitioningId}
              bulkMode={bulkMode}
              selectedIds={selectedIds}
              onOpenTask={onOpenTask}
              onOpenWorker={onOpenWorker}
              taskHref={taskHref}
              onTransition={onTransition}
              onTrashTask={onTrashTask ? requestTrash : undefined}
              onToggleSelection={toggleSelection}
            />
          ))
        )}
      </BoardBackgroundContextMenu>
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
  focusedTaskId?: string;
  transitioningId?: string;
  bulkMode: boolean;
  selectedIds?: ReadonlySet<string>;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => void;
  onTrashTask?: (task: Task) => void;
  onToggleSelection: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(() => getBoardStatusSectionOpen(storageKey, state, defaultOpen));
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
  return (
    <Collapsible open={open} onOpenChange={changeOpen} className="mb-1">
      <CollapsibleTrigger asChild>
        <button type="button" className="flex h-9 w-full items-center gap-2 px-3 text-left text-[13px] hover:bg-foreground/[0.02]">
          <ChevronRight className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
          <StatusIcon status={state} closed={status.closed} initial={status.initial} className="size-3.5" />
          <span className="font-medium">{localizedStatusLabel(state, t)}</span>
          <span className="text-xs text-muted-foreground">{tasks.length}</span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent style={{ contentVisibility: "auto", containIntrinsicSize: "auto 32px" }}>
        {presentation === "card" ? (
          <div className="grid gap-2 px-3 pb-2 pt-1 sm:grid-cols-2 2xl:grid-cols-3">
            {tasks.map((task) => (
              <CarbonTaskCard
                key={task.id}
                task={task}
                status={status}
                selected={task.id === focusedTaskId}
                bulkMode={bulkMode}
                bulkSelected={selectedIds?.has(task.id)}
                transitioning={task.id === transitioningId}
                onOpenTask={onOpenTask}
                onOpenWorker={onOpenWorker}
                taskHref={taskHref}
                onTransition={onTransition}
                onTrashTask={onTrashTask}
                onToggleSelection={onToggleSelection}
              />
            ))}
          </div>
        ) : (
          tasks.map((task) => (
            <CarbonTaskRow
              key={task.id}
              task={task}
              status={status}
              selected={task.id === focusedTaskId}
              bulkMode={bulkMode}
              bulkSelected={selectedIds?.has(task.id)}
              transitioning={task.id === transitioningId}
              onOpenTask={onOpenTask}
              onOpenWorker={onOpenWorker}
              taskHref={taskHref}
              onTransition={onTransition}
              onTrashTask={onTrashTask}
              onToggleSelection={onToggleSelection}
            />
          ))
        )}
      </CollapsibleContent>
    </Collapsible>
  );
});

const CarbonTaskRow = memo(function CarbonTaskRow({
  task,
  status,
  selected,
  bulkMode,
  bulkSelected,
  transitioning,
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
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => void;
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
      onTransition={onTransition}
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
        "group flex h-8 w-full cursor-pointer items-center gap-2 px-3 text-left text-[13px] transition-colors hover:bg-foreground/[0.04] focus-visible:bg-foreground/[0.04] focus-visible:outline-none",
        selected && "bg-foreground/[0.06] ring-1 ring-inset ring-brand/40",
      )}
      style={{ contentVisibility: "auto", containIntrinsicSize: "auto 32px" }}
    >
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
              <DropdownMenuItem key={next} disabled={next === task.status || transitioning} onSelect={(event) => { event.stopPropagation(); onTransition(task, next); }}>
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
              aria-label={t("Request lease", "申请认领")}
              onClick={(event) => { stop(event); onOpenTask(task); }}
              className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100"
            >
              <UserPlus className="size-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent>{t("Open task to request a lease", "打开任务后申请租约")}</TooltipContent>
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
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onTransition: (task: Task, to: string) => void;
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
      onTransition={onTransition}
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
          "group flex min-h-40 cursor-pointer flex-col gap-3 rounded-xl border bg-card p-3 text-left shadow-sm transition-colors hover:bg-muted/35 focus-visible:bg-muted/35 focus-visible:outline-none",
          selected && "ring-2 ring-brand/40",
        )}
        style={{ contentVisibility: "auto", containIntrinsicSize: "auto 160px" }}
      >
        <div className="flex min-w-0 items-center gap-2">
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
                  <DropdownMenuItem key={next} disabled={next === task.status || transitioning} onSelect={(event) => { event.stopPropagation(); onTransition(task, next); }}>
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
                  aria-label={t("Request lease", "申请认领")}
                  onClick={(event) => { stop(event); onOpenTask(task); }}
                  onPointerDown={stop}
                  className="grid size-6 shrink-0 place-items-center rounded text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
                >
                  <UserPlus className="size-3.5" />
                </button>
              </TooltipTrigger>
              <TooltipContent>{t("Open task to request a lease", "打开任务后申请租约")}</TooltipContent>
            </Tooltip>
          )}
        </div>
      </article>
    </CarbonTaskContextMenu>
  );
});

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
