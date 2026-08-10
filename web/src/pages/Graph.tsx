import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactElement,
} from "react";
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  ArrowLeft,
  ClipboardCopy,
  ChevronsDown,
  ExternalLink,
  FilePlus2,
  Maximize2,
  MessageSquareCode,
  Network,
  PanelRightClose,
  PanelRightOpen,
  RefreshCw,
  Search,
  Text,
  Unlink,
  WandSparkles,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  ContextMenu,
  ContextMenuCheckboxItem,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/EmptyState";
import { StatusIcon } from "@/components/StatusIcon";
import { useI18n } from "@/lib/i18n";
import { useTasks } from "@/lib/queries";
import { carbonImportanceLabel } from "@/lib/task-labels";
import { cn, statusLabel } from "@/lib/utils";
import type { Status, Task } from "@/lib/api";
import {
  dependencyTopologyKey,
  GRAPH_NODE_HEIGHT,
  GRAPH_NODE_WIDTH,
  layoutDependencyGraph,
  partitionDependencyTasks,
  shouldUseDependencyLayoutWorker,
  uniqueDependencyTasks,
  type DependencyGraphLayout,
  type DependencyGraphLayoutRequest,
  type DependencyGraphLayoutResponse,
} from "@/pages/graph-layout";

const UNLINKED_PAGE_SIZE = 80;

type NodeData = {
  task: Task;
  status: Status;
  ready: boolean;
  closedState: boolean;
  importanceLabel: string;
  dimmed: boolean;
  onOpenTask: (id: string) => void;
};

const EMPTY_GRAPH: { nodes: Node<NodeData>[]; edges: Edge[] } = { nodes: [], edges: [] };

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Clipboard access is unavailable");
}

function openKeyboardContextMenu(event: ReactKeyboardEvent<HTMLElement>): boolean {
  if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10")) return false;
  event.preventDefault();
  event.stopPropagation();
  const rect = event.currentTarget.getBoundingClientRect();
  event.currentTarget.dispatchEvent(new MouseEvent("contextmenu", {
    bubbles: true,
    cancelable: true,
    clientX: rect.left + Math.min(24, rect.width / 2),
    clientY: rect.top + Math.min(24, rect.height / 2),
  }));
  return true;
}

function GraphTaskContextMenu({
  children,
  task,
  onOpenTask,
}: {
  children: ReactElement;
  task: Task;
  onOpenTask: (id: string) => void;
}) {
  const { t } = useI18n();
  const copy = async (value: string, success: string) => {
    try {
      await copyText(value);
      toast.success(success);
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
      <ContextMenuContent className="min-w-52">
        <ContextMenuLabel>{task.id}</ContextMenuLabel>
        <ContextMenuGroup>
          <ContextMenuItem onSelect={(event) => { event.stopPropagation(); onOpenTask(task.id); }}>
            <ExternalLink />
            {t("Open task details", "打开任务详情")}
            <ContextMenuShortcut>Enter</ContextMenuShortcut>
          </ContextMenuItem>
          <ContextMenuItem onSelect={(event) => { event.stopPropagation(); void copy(task.id, t("Task ID copied", "任务 ID 已复制")); }}>
            <ClipboardCopy />
            {t("Copy task ID", "复制任务 ID")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={(event) => { event.stopPropagation(); void copy(task.title, t("Task title copied", "任务标题已复制")); }}>
            <Text />
            {t("Copy task title", "复制任务标题")}
          </ContextMenuItem>
          <ContextMenuItem
            onSelect={(event) => {
              event.stopPropagation();
              void copy(
                `Work on Carbon task ${task.id}: ${task.title}`,
                t("Agent prompt copied", "智能体提示词已复制"),
              );
            }}
          >
            <MessageSquareCode />
            {t("Copy Agent prompt", "复制智能体提示词")}
          </ContextMenuItem>
        </ContextMenuGroup>
      </ContextMenuContent>
    </ContextMenu>
  );
}

function tasksFromTopologyKey(topologyKey: string): Array<{ id: string; deps: string[] }> {
  const topology = JSON.parse(topologyKey) as { ids: string[]; edges: Array<[string, string]> };
  const dependencies = new Map(topology.ids.map((id) => [id, [] as string[]]));
  for (const [source, target] of topology.edges) dependencies.get(target)?.push(source);
  return topology.ids.map((id) => ({ id, deps: dependencies.get(id) ?? [] }));
}

function taskMatchesQuery(task: Task, query: string): boolean {
  if (!query) return true;
  const haystack = [task.id, task.title, task.status, task.importance, ...(task.labels ?? [])]
    .filter(Boolean)
    .join("\n")
    .toLocaleLowerCase();
  return haystack.includes(query);
}

function layout(
  tasks: Task[],
  status: Status,
  geometry: DependencyGraphLayout,
  query: string,
  onOpenTask: (id: string) => void,
  importanceLabel: (value: string | undefined) => string,
): { nodes: Node<NodeData>[]; edges: Edge[] } {
  const uniqueTasks = uniqueDependencyTasks(tasks);
  const taskById = new Map(uniqueTasks.map((task) => [task.id, task]));
  const closed = new Set(status.closed ?? []);
  const animateEdges = geometry.connections.length <= 200;
  const nodes: Node<NodeData>[] = uniqueTasks.map((task) => {
    const position = geometry.positions.get(task.id) ?? { x: 0, y: 0 };
    return {
      id: task.id,
      type: "task",
      position,
      data: {
        task,
        status,
        ready: task.ready,
        closedState: closed.has(task.status),
        importanceLabel: importanceLabel(task.importance),
        dimmed: !!query && !taskMatchesQuery(task, query),
        onOpenTask,
      },
    };
  });
  const edges: Edge[] = geometry.connections.map((connection) => {
    const target = taskById.get(connection.target);
    return {
      id: `${connection.source}->${connection.target}`,
      source: connection.source,
      target: connection.target,
      animated: animateEdges && target?.ready === true && !closed.has(target.status),
      reconnectable: false,
      markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14 },
    };
  });
  return { nodes, edges };
}

function TaskNode({ data, isConnectable }: NodeProps<Node<NodeData>>) {
  const { task, status, ready, closedState, importanceLabel, dimmed, onOpenTask } = data;
  return (
    <GraphTaskContextMenu task={task} onOpenTask={onOpenTask}>
      <div
        data-carbon-task-surface
        role="button"
        tabIndex={0}
        aria-haspopup="menu"
        aria-label={`${task.id}: ${task.title}`}
        style={{ width: GRAPH_NODE_WIDTH, height: GRAPH_NODE_HEIGHT }}
        className={cn(
          "group flex items-center gap-2 rounded-lg border bg-panel px-2.5 text-left shadow-xs outline-none transition-[border-color,box-shadow,opacity] focus-visible:ring-2 focus-visible:ring-ring/40",
          closedState && "opacity-70",
          ready && !closedState && "border-brand/60 ring-1 ring-brand/30",
          dimmed && "opacity-25",
        )}
        onDoubleClick={() => onOpenTask(task.id)}
        onKeyDown={(event) => {
          if (openKeyboardContextMenu(event)) return;
          if (event.key !== "Enter" && event.key !== " ") return;
          event.preventDefault();
          onOpenTask(task.id);
        }}
      >
        <Handle
          type="target"
          position={Position.Left}
          isConnectable={isConnectable}
          isConnectableStart={false}
          isConnectableEnd={false}
          className="!pointer-events-none !size-1 !border-0 !bg-transparent !opacity-0"
        />
        <StatusIcon status={task.status} closed={status.closed} initial={status.initial} className="size-3.5" />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <span className="truncate font-mono text-[10px] leading-tight text-muted-foreground">{task.id}</span>
            {importanceLabel && task.importance !== "normal" && (
              <span className="truncate text-[9px] leading-tight text-muted-foreground">· {importanceLabel}</span>
            )}
          </div>
          <div className="truncate text-xs leading-tight">{task.title}</div>
        </div>
        <Handle
          type="source"
          position={Position.Right}
          isConnectable={isConnectable}
          isConnectableStart={false}
          isConnectableEnd={false}
          className="!pointer-events-none !size-1 !border-0 !bg-transparent !opacity-0"
        />
      </div>
    </GraphTaskContextMenu>
  );
}

const nodeTypes = { task: TaskNode };

function UnlinkedTaskCard({
  task,
  status,
  onOpenTask,
}: {
  task: Task;
  status: Status;
  onOpenTask: (id: string) => void;
}) {
  const { t } = useI18n();
  const importance = carbonImportanceLabel(task.importance, t);
  return (
    <GraphTaskContextMenu task={task} onOpenTask={onOpenTask}>
      <button
        data-carbon-task-surface
        type="button"
        className="group flex min-h-14 min-w-0 items-center gap-2 rounded-lg border bg-background px-3 py-2 text-left shadow-xs transition-[border-color,box-shadow,background-color] hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        onClick={() => onOpenTask(task.id)}
        onKeyDown={(event) => { openKeyboardContextMenu(event); }}
        aria-haspopup="menu"
        aria-label={`${task.id}: ${task.title}`}
      >
        <StatusIcon status={task.status} closed={status.closed} initial={status.initial} className="size-3.5" />
        <span className="min-w-0 flex-1">
          <span className="block truncate font-mono text-[10px] leading-tight text-muted-foreground">{task.id}</span>
          <span className="block truncate text-xs leading-5">{task.title}</span>
        </span>
        {importance && (
          <Badge variant="secondary" className="max-w-24 shrink-0 truncate px-1.5 text-[10px] font-normal">
            {importance}
          </Badge>
        )}
      </button>
    </GraphTaskContextMenu>
  );
}

function UnlinkedTasks({
  tasks,
  allCount,
  status,
  limit,
  onLoadMore,
  onOpenTask,
  compact,
}: {
  tasks: Task[];
  allCount: number;
  status: Status;
  limit: number;
  onLoadMore: () => void;
  onOpenTask: (id: string) => void;
  compact: boolean;
}) {
  const { t } = useI18n();
  const visible = useMemo(() => tasks.slice(0, limit), [limit, tasks]);
  const groups = useMemo(() => {
    const stateOrder = new Map((status.states ?? []).map((state, index) => [state, index]));
    const importanceOrder = new Map([
      ["core", 0],
      ["important", 1],
      ["normal", 2],
      ["optional", 3],
      ["experimental", 4],
    ]);
    const grouped = new Map<string, Task[]>();
    for (const task of visible) {
      const list = grouped.get(task.status) ?? [];
      list.push(task);
      grouped.set(task.status, list);
    }
    for (const list of grouped.values()) {
      list.sort((left, right) => {
        const importance = (importanceOrder.get(left.importance ?? "normal") ?? 5)
          - (importanceOrder.get(right.importance ?? "normal") ?? 5);
        return importance || left.id.localeCompare(right.id);
      });
    }
    return [...grouped.entries()].sort(([left], [right]) => {
      const state = (stateOrder.get(left) ?? Number.MAX_SAFE_INTEGER)
        - (stateOrder.get(right) ?? Number.MAX_SAFE_INTEGER);
      return state || left.localeCompare(right);
    });
  }, [status.states, visible]);
  return (
    <section className={cn("flex min-h-0 flex-col bg-muted/15", compact ? "w-80 shrink-0 border-l" : "h-full") }>
      <div className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
        <Unlink className="size-3.5 text-muted-foreground" />
        <h2 className="text-xs font-medium">{t("Independent tasks", "独立任务")}</h2>
        <Badge variant="secondary" className="ml-auto tabular-nums">{allCount}</Badge>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {tasks.length === 0 ? (
          <p className="px-2 py-8 text-center text-xs text-muted-foreground">
            {t("No independent tasks match this search.", "没有匹配搜索的独立任务。")}
          </p>
        ) : (
          <div className="space-y-4">
            {groups.map(([state, stateTasks]) => (
              <section key={state}>
                <div className="mb-1.5 flex items-center gap-1.5 px-0.5 text-[11px] font-medium text-muted-foreground">
                  <StatusIcon status={state} closed={status.closed} initial={status.initial} className="size-3" />
                  <span>{statusLabel(state)}</span>
                  <span className="ml-auto tabular-nums">{stateTasks.length}</span>
                </div>
                <div className={cn("grid gap-2", compact ? "grid-cols-1" : "grid-cols-[repeat(auto-fill,minmax(220px,1fr))]") }>
                  {stateTasks.map((task) => (
                    <UnlinkedTaskCard key={task.id} task={task} status={status} onOpenTask={onOpenTask} />
                  ))}
                </div>
              </section>
            ))}
          </div>
        )}
        {visible.length < tasks.length && (
          <Button variant="ghost" size="sm" className="mt-3 w-full" onClick={onLoadMore}>
            <ChevronsDown />
            {t("Show {count} more", "再显示 {count} 个", { count: Math.min(UNLINKED_PAGE_SIZE, tasks.length - visible.length) })}
          </Button>
        )}
      </div>
    </section>
  );
}

function GraphLayoutLoading({ connectedCount }: { connectedCount: number }) {
  const { t } = useI18n();
  return (
    <div className="grid h-full min-w-0 place-items-center bg-app p-6" role="status" aria-live="polite" aria-busy="true">
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        <Network className="size-5 animate-pulse text-brand" />
        <div>
          <p className="text-sm font-medium">{t("Laying out {count} connected tasks…", "正在布局 {count} 个已连接任务…", { count: connectedCount })}</p>
          <p className="mt-1 text-xs text-muted-foreground">{t("The dependency graph will be ready without blocking this workspace.", "依赖图将在不阻塞当前工作区的情况下完成。")}</p>
        </div>
        <Skeleton className="h-2 w-48 rounded-full" />
      </div>
    </div>
  );
}

export function Graph({
  path,
  status,
  onOpenTask,
  onBack,
}: {
  path: string;
  status: Status;
  onOpenTask: (id: string) => void;
  onBack: () => void;
}) {
  const { data: tasks, isLoading } = useTasks(path);
  return <GraphCanvas tasks={tasks ?? []} status={status} loading={isLoading} onOpenTask={onOpenTask} onBack={onBack} />;
}

/** Data-source agnostic canvas for both the legacy path query and Carbon's scoped task query. */
export function GraphCanvas({
  tasks,
  status,
  loading = false,
  onOpenTask,
  onBack,
  onNewTask,
  onRefresh,
}: {
  tasks: Task[];
  status: Status;
  loading?: boolean;
  onOpenTask: (id: string) => void;
  onBack?: () => void;
  onNewTask?: () => void;
  onRefresh?: () => void;
}) {
  const { t } = useI18n();
  const partition = useMemo(() => partitionDependencyTasks(tasks), [tasks]);
  const topologyKey = useMemo(() => dependencyTopologyKey(partition.connected), [partition.connected]);
  const [layoutRevision, setLayoutRevision] = useState(0);
  const layoutKey = `${topologyKey}\nlayout:${layoutRevision}`;
  const [workerLayout, setWorkerLayout] = useState<{ layoutKey: string; geometry: DependencyGraphLayout } | null>(null);
  const [failedWorkerLayoutKey, setFailedWorkerLayoutKey] = useState<string | null>(null);
  const layoutRequestId = useRef(0);
  const useWorkerLayout = shouldUseDependencyLayoutWorker(partition.connected.length)
    && typeof Worker !== "undefined"
    && failedWorkerLayoutKey !== layoutKey;
  const synchronousGeometry = useMemo(
    () => {
      // A manual "auto-arrange" request intentionally recomputes the same
      // deterministic topology and then re-fits the viewport.
      void layoutRevision;
      return useWorkerLayout ? undefined : layoutDependencyGraph(tasksFromTopologyKey(topologyKey));
    },
    [layoutRevision, topologyKey, useWorkerLayout],
  );

  useEffect(() => {
    if (!useWorkerLayout) return;

    const requestId = ++layoutRequestId.current;
    let disposed = false;
    let settled = false;
    let worker: Worker;
    try {
      worker = new Worker(new URL("./graph-layout.worker.ts", import.meta.url), { type: "module" });
    } catch {
      setFailedWorkerLayoutKey(layoutKey);
      return;
    }

    const finishWithFallback = () => {
      if (disposed || settled || layoutRequestId.current !== requestId) return;
      settled = true;
      window.clearTimeout(timeout);
      worker.terminate();
      setFailedWorkerLayoutKey(layoutKey);
    };
    const timeout = window.setTimeout(finishWithFallback, 5_000);
    const onMessage = (event: MessageEvent<DependencyGraphLayoutResponse>) => {
      const response = event.data;
      if (disposed || settled || layoutRequestId.current !== requestId || response.requestId !== requestId) return;
      if (!response.layout) {
        finishWithFallback();
        return;
      }
      settled = true;
      window.clearTimeout(timeout);
      worker.terminate();
      setWorkerLayout({ layoutKey, geometry: response.layout });
    };

    worker.addEventListener("message", onMessage);
    worker.addEventListener("error", finishWithFallback);
    const request: DependencyGraphLayoutRequest = { requestId, tasks: tasksFromTopologyKey(topologyKey) };
    worker.postMessage(request);

    return () => {
      disposed = true;
      window.clearTimeout(timeout);
      worker.terminate();
      if (layoutRequestId.current === requestId) layoutRequestId.current += 1;
    };
  }, [layoutKey, topologyKey, useWorkerLayout]);

  const geometry = useWorkerLayout
    ? workerLayout?.layoutKey === layoutKey ? workerLayout.geometry : undefined
    : synchronousGeometry;
  const layoutPending = useWorkerLayout && !geometry;
  const [query, setQuery] = useState("");
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const { nodes, edges } = useMemo(
    () => geometry
      ? layout(
        partition.connected,
        status,
        geometry,
        normalizedQuery,
        onOpenTask,
        (value) => carbonImportanceLabel(value, t),
      )
      : EMPTY_GRAPH,
    [geometry, normalizedQuery, onOpenTask, partition.connected, status, t],
  );
  const filteredUnlinked = useMemo(
    () => partition.isolated.filter((task) => taskMatchesQuery(task, normalizedQuery)),
    [normalizedQuery, partition.isolated],
  );
  const [flow, setFlow] = useState<ReactFlowInstance<Node<NodeData>, Edge> | null>(null);
  const [flowTopologyKey, setFlowTopologyKey] = useState<string | null>(null);
  const [showUnlinked, setShowUnlinked] = useState(true);
  const [unlinkedLimit, setUnlinkedLimit] = useState(UNLINKED_PAGE_SIZE);
  const searchRef = useRef<HTMLInputElement>(null);

  const handleFlowInit = useCallback((nextFlow: ReactFlowInstance<Node<NodeData>, Edge>) => {
    setFlow(nextFlow);
    setFlowTopologyKey(topologyKey);
  }, [topologyKey]);

  const fitGraph = useCallback(() => {
    if (layoutPending || !flow || flowTopologyKey !== topologyKey || nodes.length === 0) return;
    void flow.fitView({ padding: 0.16, duration: 180 });
  }, [flow, flowTopologyKey, layoutPending, nodes.length, topologyKey]);
  const autoArrange = useCallback(() => {
    setFailedWorkerLayoutKey(null);
    setLayoutRevision((current) => current + 1);
  }, []);

  useEffect(() => {
    setUnlinkedLimit(UNLINKED_PAGE_SIZE);
  }, [normalizedQuery]);

  useEffect(() => {
    if (layoutPending || !flow || flowTopologyKey !== topologyKey || nodes.length === 0) return;
    const frame = window.requestAnimationFrame(fitGraph);
    return () => window.cancelAnimationFrame(frame);
  }, [fitGraph, flow, flowTopologyKey, layoutPending, layoutRevision, nodes.length, topologyKey]);

  const hasConnectedTasks = partition.connected.length > 0;
  const hasUnlinkedTasks = partition.isolated.length > 0;
  const showUnlinkedPanel = hasUnlinkedTasks && (!hasConnectedTasks || showUnlinked);

  return (
    <div className="flex h-full flex-col">
      <header className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b px-3 py-1.5">
        {onBack && (
          <Button variant="ghost" size="icon" aria-label={t("Back", "返回")} onClick={onBack}>
            <ArrowLeft />
          </Button>
        )}
        <span className="text-sm font-medium">{t("Dependency graph", "依赖关系图")}</span>
        <span className="text-xs tabular-nums text-muted-foreground">
          {t(
            "{connected} in graph · {unlinked} independent",
            "{connected} 个进入依赖图 · {unlinked} 个独立任务",
            { connected: partition.connected.length, unlinked: partition.isolated.length },
          )}
        </span>
        <span className="hidden items-center gap-1 text-[11px] text-muted-foreground lg:inline-flex">
          <WandSparkles className="size-3" />
          {t("Auto-arranged from task dependencies", "根据任务依赖自动整理")}
        </span>
        <div className="relative ml-auto w-48 max-w-full">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={searchRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("Search tasks…", "搜索任务…")}
            aria-label={t("Search graph tasks", "搜索依赖图任务")}
            className="h-7 pl-8 text-xs"
          />
        </div>
        {hasConnectedTasks && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t("Auto-arrange dependency graph", "自动整理依赖图")}
            title={t("Auto-arrange from task dependencies", "根据任务依赖自动整理")}
            onClick={autoArrange}
            disabled={layoutPending}
          >
            <WandSparkles />
          </Button>
        )}
        {hasConnectedTasks && (
          <Button variant="ghost" size="icon-sm" aria-label={t("Fit dependency graph", "适应依赖图")} onClick={fitGraph} disabled={layoutPending}>
            <Maximize2 />
          </Button>
        )}
        {hasConnectedTasks && hasUnlinkedTasks && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={showUnlinked ? t("Hide independent tasks", "隐藏独立任务") : t("Show independent tasks", "显示独立任务")}
            aria-pressed={showUnlinked}
            onClick={() => setShowUnlinked((current) => !current)}
          >
            {showUnlinked ? <PanelRightClose /> : <PanelRightOpen />}
          </Button>
        )}
      </header>

      <ContextMenu>
        <ContextMenuTrigger asChild>
          <div
            data-carbon-context-surface
            className="flex min-h-0 flex-1 bg-background"
            onContextMenu={(event) => {
              const target = event.target;
              if (target instanceof Element && target.closest("[data-carbon-task-surface]")) event.preventDefault();
            }}
          >
            {tasks.length === 0 && loading ? (
              <div className="grid min-w-0 flex-1 place-items-center bg-app p-6" aria-label={t("Loading dependency graph", "正在加载依赖图")}>
                <div className="grid w-full max-w-3xl grid-cols-3 gap-x-20 gap-y-10">
                  <Skeleton className="col-start-1 h-12 rounded-lg" />
                  <Skeleton className="col-start-2 h-12 rounded-lg" />
                  <Skeleton className="col-start-3 h-12 rounded-lg" />
                  <Skeleton className="col-start-2 h-12 rounded-lg" />
                </div>
              </div>
            ) : tasks.length === 0 ? (
              <div className="min-w-0 flex-1">
                <EmptyState
                  icon={Network}
                  title={t("No tasks to graph yet", "还没有可显示的任务")}
                  message={t("As tasks and their dependencies appear, you'll see the dependency graph here.", "任务及其依赖关系出现后，会在这里显示。")}
                />
              </div>
            ) : hasConnectedTasks ? (
              <div className="min-w-0 flex-1">
                {layoutPending ? <GraphLayoutLoading connectedCount={partition.connected.length} /> : (
                  <ReactFlow
                    key={topologyKey}
                    nodes={nodes}
                    edges={edges}
                    nodeTypes={nodeTypes}
                    onInit={handleFlowInit}
                    proOptions={{ hideAttribution: true }}
                    onNodeClick={(_, node) => onOpenTask(node.id)}
                    nodesDraggable={false}
                    nodesConnectable={false}
                    edgesReconnectable={false}
                    connectOnClick={false}
                    deleteKeyCode={null}
                  >
                    <Background gap={16} className="!bg-app" />
                    <Controls showInteractive={false} />
                    {nodes.length >= 12 && (
                      <MiniMap
                        pannable
                        zoomable
                        nodeColor="var(--muted-foreground)"
                        maskColor="color-mix(in oklch, var(--background) 78%, transparent)"
                        className="!rounded-lg !border !bg-background/90 !shadow-sm"
                      />
                    )}
                  </ReactFlow>
                )}
              </div>
            ) : null}

            {showUnlinkedPanel && (
              <UnlinkedTasks
                tasks={filteredUnlinked}
                allCount={partition.isolated.length}
                status={status}
                limit={unlinkedLimit}
                onLoadMore={() => setUnlinkedLimit((current) => current + UNLINKED_PAGE_SIZE)}
                onOpenTask={onOpenTask}
                compact={hasConnectedTasks}
              />
            )}
          </div>
        </ContextMenuTrigger>
        <ContextMenuContent>
          <ContextMenuLabel>{t("Dependency graph", "依赖关系图")}</ContextMenuLabel>
          <ContextMenuGroup>
            {onNewTask && (
              <ContextMenuItem onSelect={onNewTask}>
                <FilePlus2 />
                {t("New task", "新建任务")}
                <ContextMenuShortcut>C</ContextMenuShortcut>
              </ContextMenuItem>
            )}
            <ContextMenuItem onSelect={() => searchRef.current?.focus()}>
              <Search />
              {t("Search graph tasks", "搜索依赖图任务")}
              <ContextMenuShortcut>/</ContextMenuShortcut>
            </ContextMenuItem>
            {onRefresh && (
              <ContextMenuItem onSelect={onRefresh}>
                <RefreshCw />
                {t("Refresh graph data", "刷新依赖图数据")}
              </ContextMenuItem>
            )}
          </ContextMenuGroup>
          <ContextMenuSeparator />
          <ContextMenuGroup>
            <ContextMenuItem disabled={!hasConnectedTasks || layoutPending} onSelect={autoArrange}>
              <WandSparkles />
              {t("Auto-arrange from dependencies", "根据依赖自动整理")}
            </ContextMenuItem>
            <ContextMenuItem disabled={!hasConnectedTasks || layoutPending} onSelect={fitGraph}>
              <Maximize2 />
              {t("Fit connected graph", "适应已连接图")}
            </ContextMenuItem>
          </ContextMenuGroup>
          {hasConnectedTasks && hasUnlinkedTasks && (
            <>
              <ContextMenuSeparator />
              <ContextMenuCheckboxItem checked={showUnlinked} onCheckedChange={(checked) => setShowUnlinked(checked === true)}>
                {t("Show independent tasks", "显示独立任务")}
              </ContextMenuCheckboxItem>
            </>
          )}
        </ContextMenuContent>
      </ContextMenu>
    </div>
  );
}
