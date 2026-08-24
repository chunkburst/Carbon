import { useEffect, useRef, useState } from "react";
import { Bookmark, ChevronRight, Inbox, ListChecks, Plus, Search, Tags, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { PriorityIcon, PRIORITIES, priorityLabel } from "@/components/PriorityIcon";
import { Facet } from "@/components/Facet";
import { SearchableFacet } from "@/components/SearchableFacet";
import { EmptyState } from "@/components/EmptyState";
import { Onboarding } from "@/components/Onboarding";
import { addView, loadViews, removeView, type SavedView } from "@/lib/views";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { TaskRow } from "@/components/TaskRow";
import { StatusIcon } from "@/components/StatusIcon";
import { useBulkCarbonPatch, useCarbonCapabilities, useTasks } from "@/lib/queries";
import { hasCarbonFeature } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";
import { cn, statusLabel } from "@/lib/utils";
import { matches } from "@/lib/filter";
import type { Filter } from "@/components/AppSidebar";
import type { Status, Task } from "@/lib/api";

export function Board({
  path,
  status,
  filter,
  onOpenTask,
  onNewTask,
  onPickFilter,
}: {
  path: string;
  status: Status;
  filter: Filter;
  onOpenTask: (id: string) => void;
  onNewTask: () => void;
  onPickFilter: (f: Filter) => void;
}) {
  const { t } = useI18n();
  const { data: tasks, isLoading } = useTasks(path);
  const [query, setQuery] = useState("");
  const [label, setLabel] = useState("");
  const [assignee, setAssignee] = useState("");
  const [priority, setPriority] = useState("");
  const [views, setViews] = useState<SavedView[]>(() => loadViews(path));
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const [bulkLabels, setBulkLabels] = useState("");
  const bulkPatch = useBulkCarbonPatch(path);
  const { data: capabilityResult } = useCarbonCapabilities(path);

  const states = status.states ?? [];
  const closed = new Set(status.closed ?? []);
  const q = query.trim().toLowerCase();

  // Facet option lists, derived from what's present.
  const labelOpts = [...new Set((tasks ?? []).flatMap((t) => t.labels ?? []))].sort();
  const assigneeOpts = [...new Set((tasks ?? []).map((t) => t.assignee).filter(Boolean))].sort() as string[];
  const prioOpts = ["urgent", "high", "medium", "low"].filter((p) =>
    (tasks ?? []).some((t) => t.priority === p),
  );
  const facetsActive = !!(label || assignee || priority || q);

  const visible = (tasks ?? [])
    .filter((t) => matches(t, filter, status))
    .filter((t) => !q || t.id.toLowerCase().includes(q) || t.title.toLowerCase().includes(q))
    .filter((t) => !label || (t.labels ?? []).includes(label))
    .filter((t) => !assignee || t.assignee === assignee)
    .filter((t) => !priority || t.priority === priority);

  const applyView = (v: SavedView) => {
    setQuery(v.query ?? "");
    setLabel(v.label ?? "");
    setAssignee(v.assignee ?? "");
    setPriority(v.priority ?? "");
    onPickFilter((v.filter as Filter) ?? "all");
  };
  const saveCurrentView = () => {
    const name = window.prompt(t("Save view as", "保存视图为"));
    if (!name?.trim()) return;
    setViews(addView(path, { name: name.trim(), filter, query, label, assignee, priority }));
  };
  const clearFacets = () => {
    setQuery("");
    setLabel("");
    setAssignee("");
    setPriority("");
  };

  const byStatus = new Map<string, Task[]>(states.map((s) => [s, []]));
  for (const t of visible) {
    if (!byStatus.has(t.status)) byStatus.set(t.status, []);
    byStatus.get(t.status)!.push(t);
  }
  const groups = [...byStatus.entries()].filter(([, list]) => list.length > 0);
  const isEmpty = !isLoading && visible.length === 0;
  const currentFilterLabel = filterLabel(filter, t);

  // Keyboard navigation over the flat, display-ordered list (j/k/o/enter/c//).
  const flat = groups.flatMap(([, list]) => list);
  const [sel, setSel] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const selId = flat[Math.min(sel, flat.length - 1)]?.id;
  const selected = flat.filter((task) => selectedIds.has(task.id));
  const allSelected = flat.length > 0 && selected.length === flat.length;
  const bulkAvailable = capabilityResult?.available === true && hasCarbonFeature(capabilityResult.data, "bulk");

  const toggleSelected = (id: string, checked: boolean) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const toggleAll = (checked: boolean) => {
    setSelectedIds(checked ? new Set(flat.map((task) => task.id)) : new Set());
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      // bail when any modal / palette / Select-listbox / menu popup is open
      if (document.querySelector('[role="dialog"],[role="listbox"],[role="menu"]')) return;
      if (e.key === "j") {
        e.preventDefault();
        setSel((s) => Math.min(flat.length - 1, s + 1));
      } else if (e.key === "k") {
        e.preventDefault();
        setSel((s) => Math.max(0, s - 1));
      } else if (e.key === "Enter" || e.key === "o") {
        if (selId) {
          e.preventDefault();
          onOpenTask(selId);
        }
      } else if (e.key === "c") {
        e.preventDefault();
        onNewTask();
      } else if (e.key === "/") {
        e.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [flat.length, selId, onOpenTask, onNewTask]);

  useEffect(() => {
    if (selId) document.getElementById(`row-${selId}`)?.scrollIntoView({ block: "nearest" });
  }, [selId]);

  // Reset the keyboard cursor when the base view changes (avoids stale out-of-range index).
  useEffect(() => setSel(0), [filter]);

  useEffect(() => {
    const visibleIds = new Set((tasks ?? []).map((task) => task.id));
    setSelectedIds((current) => new Set([...current].filter((id) => visibleIds.has(id))));
  }, [tasks]);

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center justify-between border-b px-4">
          <div className="flex items-center gap-1.5 text-[13px]">
          <Checkbox
            checked={allSelected ? true : selected.length > 0 ? "indeterminate" : false}
            aria-label={t("Select all visible tasks", "选择所有可见任务")}
            onCheckedChange={(checked) => toggleAll(checked === true)}
          />
          <span className="font-medium">{status.prefix}</span>
          <ChevronRight className="size-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">{currentFilterLabel}</span>
          {!isLoading && (
            <span className="ml-1 text-xs text-muted-foreground">{visible.length}</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              ref={searchRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("Filter… ( / )", "筛选…（/）")}
              className="h-7 w-44 pl-7 text-xs"
            />
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="h-7 gap-1 px-2 text-xs">
                <Bookmark className="size-3.5" /> {t("Views", "视图")}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuLabel>{t("Saved views", "已保存的视图")}</DropdownMenuLabel>
              {views.length === 0 && (
                <div className="px-2 py-1.5 text-xs text-muted-foreground">{t("None yet", "暂无")}</div>
              )}
              {views.length > 0 && (
                <DropdownMenuGroup>
                  {views.map((v) => (
                    <DropdownMenuItem key={v.name} onSelect={() => applyView(v)} className="justify-between">
                      <span className="truncate">{v.name}</span>
                      <button
                        aria-label={t("Delete {name}", "删除 {name}", { name: v.name })}
                        onClick={(e) => {
                          e.stopPropagation();
                          setViews(removeView(path, v.name));
                        }}
                        className="text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="size-3.5" />
                      </button>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuItem onSelect={saveCurrentView}>{t("Save current view…", "保存当前视图…")}</DropdownMenuItem>
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
          <Button size="sm" className="h-7 gap-1 px-2.5 text-xs" onClick={onNewTask}>
            <Plus className="size-3.5" /> {t("New task", "新建任务")}
          </Button>
        </div>
      </header>

      {(labelOpts.length > 0 || assigneeOpts.length > 0 || prioOpts.length > 0) && (
        <div className="flex shrink-0 items-center gap-2 border-b px-4 py-1.5">
          {prioOpts.length > 0 && (
            <Facet value={priority} onChange={setPriority} placeholder={t("Priority", "优先级")}>
              {prioOpts.map((p) => (
                <SelectItem key={p} value={p}>
                  <span className="flex items-center gap-2">
                    <PriorityIcon priority={p} /> {priorityLabel(p)}
                  </span>
                </SelectItem>
              ))}
            </Facet>
          )}
          {labelOpts.length > 0 && (
            <SearchableFacet
              value={label}
              onChange={setLabel}
              placeholder={t("Label", "标签")}
              options={labelOpts}
            />
          )}
          {assigneeOpts.length > 0 && (
            <Facet value={assignee} onChange={setAssignee} placeholder={t("Assignee", "负责人")}>
              {assigneeOpts.map((a) => (
                <SelectItem key={a} value={a}>
                  {a}
                </SelectItem>
              ))}
            </Facet>
          )}
          {facetsActive && (
            <Button variant="ghost" size="sm" className="h-6 gap-1 px-2 text-xs" onClick={clearFacets}>
              <X className="size-3" /> {t("Clear", "清除")}
            </Button>
          )}
        </div>
      )}

      {selected.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/30 px-4 py-2">
          <span className="flex items-center gap-1.5 text-sm font-medium">
            <ListChecks />
            {t("{count} selected", "已选择 {count} 项", { count: selected.length })}
          </span>
          <Select
            value=""
            disabled={!bulkAvailable || bulkPatch.isPending}
            onValueChange={(status) => bulkPatch.mutate({ ids: selected.map((task) => task.id), status })}
          >
            <SelectTrigger className="h-8 w-36 text-sm">{t("Move to…", "移动到…")}</SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {(status.states ?? []).map((state) => (
                  <SelectItem key={state} value={state}>{statusLabel(state)}</SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <Select
            value=""
            disabled={!bulkAvailable || bulkPatch.isPending}
            onValueChange={(priority) =>
              bulkPatch.mutate({ ids: selected.map((task) => task.id), priority: priority === "none" ? "" : priority })
            }
          >
            <SelectTrigger className="h-8 w-40 text-sm">{t("Set priority…", "设置优先级…")}</SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="none">{t("No priority", "无优先级")}</SelectItem>
                {PRIORITIES.map((value) => <SelectItem key={value} value={value}>{priorityLabel(value)}</SelectItem>)}
              </SelectGroup>
            </SelectContent>
          </Select>
          <Input
            value={bulkLabels}
            onChange={(event) => setBulkLabels(event.target.value)}
            disabled={!bulkAvailable || bulkPatch.isPending}
            placeholder={t("Labels, comma-separated", "标签，用逗号分隔")}
            className="h-8 w-52 text-sm"
          />
          <Button
            variant="outline"
            size="sm"
            disabled={!bulkAvailable || !bulkLabels.trim() || bulkPatch.isPending}
            onClick={() => {
              const labels = bulkLabels.split(",").map((value) => value.trim()).filter(Boolean);
              bulkPatch.mutate({ ids: selected.map((task) => task.id), labels });
            }}
          >
            <Tags data-icon="inline-start" />
            {t("Apply labels", "应用标签")}
          </Button>
          <Button variant="ghost" size="sm" className="ml-auto" onClick={() => setSelectedIds(new Set())}>
            <X data-icon="inline-start" />
            {t("Clear", "清除")}
          </Button>
          {!bulkAvailable && (
            <span className="basis-full text-xs text-muted-foreground">
              {t("Update Carbon to use bulk actions.", "批量操作需要更新 Carbon 后才能使用。")}
            </span>
          )}
        </div>
      )}

      <div className="flex-1 overflow-y-auto py-1">
        {isLoading ? (
          <div className="space-y-1.5 px-3 py-2">
            {Array.from({ length: 8 }).map((_, i) => (
              <Skeleton key={i} className="h-7 w-full" />
            ))}
          </div>
        ) : (tasks?.length ?? 0) === 0 ? (
          <Onboarding status={status} onNewTask={onNewTask} />
        ) : isEmpty ? (
          <EmptyState
            icon={Inbox}
            title={t("Nothing in {view}", "“{view}”中没有任务", { view: currentFilterLabel.toLowerCase() })}
            message={t("Try another view, or create a task.", "试试其他视图，或新建一个任务。")}
            action={{ label: t("New task", "新建任务"), icon: Plus, onClick: onNewTask }}
          />
        ) : (
          groups.map(([state, list]) => (
            <StatusSection
              key={state}
              path={path}
              state={state}
              tasks={list}
              status={status}
              defaultOpen={!closed.has(state)}
              onOpen={onOpenTask}
              selectedId={selId}
              selectedIds={selectedIds}
              onSelect={toggleSelected}
            />
          ))
        )}
      </div>
    </div>
  );
}

function StatusSection({
  path,
  state,
  tasks,
  status,
  defaultOpen,
  onOpen,
  selectedId,
  selectedIds,
  onSelect,
}: {
  path: string;
  state: string;
  tasks: Task[];
  status: Status;
  defaultOpen: boolean;
  onOpen: (id: string) => void;
  selectedId?: string;
  selectedIds: Set<string>;
  onSelect: (id: string, checked: boolean) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(defaultOpen);
  useEffect(() => setOpen(defaultOpen), [defaultOpen]);
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="mb-1">
      <CollapsibleTrigger asChild>
        <button className="flex h-9 w-full items-center gap-2 px-3 text-[13px] hover:bg-foreground/[0.02]">
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
          />
          <StatusIcon
            status={state}
            closed={status.closed}
            initial={status.initial}
            className="size-3.5"
          />
          <span className="font-medium">{localizedStatusLabel(state, t)}</span>
          <span className="text-xs text-muted-foreground">{tasks.length}</span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        {tasks.map((t) => (
          <TaskRow
            key={t.id}
            path={path}
            task={t}
            status={status}
            onOpen={onOpen}
            selected={t.id === selectedId}
            bulkSelected={selectedIds.has(t.id)}
            onBulkSelect={(checked) => onSelect(t.id, checked)}
          />
        ))}
      </CollapsibleContent>
    </Collapsible>
  );
}
type Translate = (en: string, zh: string, vars?: Record<string, string | number>) => string;

function filterLabel(filter: Filter, t: Translate): string {
  const labels: Record<Filter, [string, string]> = {
    all: ["All tasks", "所有任务"],
    active: ["Active", "进行中"],
    stalled: ["Stalled", "已停滞"],
    review: ["Awaiting review", "等待审核"],
    backlog: ["Backlog", "待办"],
    ready: ["Ready", "就绪"],
  };
  return t(...labels[filter]);
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
