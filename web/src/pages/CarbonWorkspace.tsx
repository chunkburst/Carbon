import { lazy, Suspense, useCallback, useDeferredValue, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Bookmark,
  CircleDashed,
  ClockAlert,
  FilePlus2,
  HelpCircle,
  KanbanSquare,
  ListTodo,
  ListChecks,
  Network,
  Moon,
  Plug,
  ScanEye,
  Search,
  Settings2,
  Sparkles,
  Sun,
  Trash2,
  UsersRound,
} from "lucide-react";
import { toast } from "sonner";
import { BrandLogo } from "@/components/BrandLogo";
import type { Filter } from "@/components/AppSidebar";
import type { CatalogIconMutation, CatalogPresentationIcons } from "@/components/CatalogIcon";
import { CarbonProjectSwitcher } from "@/components/CarbonProjectSwitcher";
import { CarbonTaskList } from "@/components/CarbonTaskList";
import { WorkspaceBackgroundContextMenu } from "@/components/WorkspaceBackgroundContextMenu";
import type { TaskNavigationTarget } from "@/components/WorkLogTypes";
import { CarbonConnectPanel } from "@/pages/Connect";
import { CreateTaskDialog } from "@/components/CreateTaskDialog";
import { CarbonNotificationBell } from "@/components/NotificationBell";
import { HelpDialog } from "@/components/HelpDialog";
import { PRIORITIES, priorityLabel } from "@/components/PriorityIcon";
import { SettingsDialog } from "@/components/SettingsDialog";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { OwnerLogs } from "@/pages/OwnerLogs";
import { Trash } from "@/pages/Trash";
import { CarbonTaskDetailPage } from "@/pages/CarbonTaskDetailPage";
import { WorkLogs } from "@/pages/WorkLogs";
import { Workers } from "@/pages/Workers";
import { carbonScopeKey, hasCarbonFeature, type CarbonHomeCluster, type CarbonHomeProject, type CarbonScope } from "@/lib/carbon-api";
import { matches } from "@/lib/filter";
import { displayName, useIdentity } from "@/lib/identity";
import type { Notif } from "@/lib/notifications";
import type { Status, Task } from "@/lib/api";
import {
  useBulkCarbonPatch,
  useBulkCarbonMove,
  useCarbonCapabilities,
  useCarbonSearch,
  useCarbonSavedViews,
  useCarbonTaskTypes,
  useCarbonTasks,
  useCarbonTaskEvents,
  useCreateCarbonView,
  useDeleteCarbonView,
  useTrashCarbonTasks,
  useTransitionCarbonTask,
} from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { carbonStorageKey } from "@/lib/storage-identity";
import { carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";
import { isMultiProjectCluster } from "@/lib/carbon-projects";
import { getTheme, toggleTheme, type Theme } from "@/lib/theme";

const GraphCanvas = lazy(() =>
  import("@/pages/Graph").then((module) => ({ default: module.GraphCanvas })),
);
import { WorkerAliasProvider } from "@/lib/worker-aliases";
import { cn, statusLabel } from "@/lib/utils";
import { addView, loadViews, removeView, type SavedView } from "@/lib/views";

type CarbonView = "board" | "graph" | "workers" | "work-logs" | "owner-logs" | "trash";
type TaskScope = "project" | "cluster";

type CarbonWorkspaceProps = {
  home: string;
  homeId?: string;
  clusters: CarbonHomeCluster[];
  standaloneProjects?: CarbonHomeProject[];
  cluster?: CarbonHomeCluster;
  project: CarbonHomeProject;
  presentation?: CatalogPresentationIcons;
  catalogUpdatePending?: boolean;
  onApplyCatalogUpdate?: () => void;
  onSetIcon?: (input: CatalogIconMutation) => Promise<void>;
  onBack: () => void;
  onSelectProject: (clusterId: string | undefined, projectId: string) => void;
  /**
   * `workspaceProjectId` owns the surrounding chrome. An explicit empty fourth
   * argument means a cluster-wide task; a non-empty one is the task's source
   * project when it differs from the workspace project.
   */
  onOpenTaskRoute: (clusterId: string | undefined, workspaceProjectId: string, taskId: string, taskProjectId?: string) => void;
  onOpenWorker?: (actor: string) => void;
  onNavigateView: (view: CarbonView, workerId?: string) => void;
  activeView?: CarbonView;
  activeTaskId?: string;
  activeTaskScope?: "cluster";
  activeTaskProjectId?: string;
  activeWorkerId?: string;
  onCloseTaskRoute?: () => void;
  suggestedActor?: string;
};

const CLOSED_STATES = new Set(["done", "closed", "completed", "cancelled", "canceled"]);
const EMPTY_SELECTED_IDS: ReadonlySet<string> = new Set<string>();

function useDebouncedValue<T>(value: T, delay = 180): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}

function carbonWorkflowStatus(home: string, tasks: Task[]): Status {
  const defaults = ["backlog", "ready", "in_progress", "review", "done"];
  const discovered = tasks.map((task) => task.status).filter(Boolean);
  const states = [...new Set([...defaults, ...discovered])];
  return {
    initialized: true,
    root: home,
    suggestedPrefix: "CARBON",
    prefix: "CARBON",
    initial: "backlog",
    states,
    closed: states.filter((state) => CLOSED_STATES.has(state.toLowerCase())),
  };
}

export function CarbonWorkspace({
  home,
  homeId,
  clusters,
  standaloneProjects = [],
  cluster,
  project,
  presentation,
  catalogUpdatePending = false,
  onApplyCatalogUpdate,
  onBack,
  onSelectProject,
  onOpenTaskRoute,
  onOpenWorker,
  onNavigateView,
  activeView = "board",
  activeTaskId,
  activeTaskScope,
  activeTaskProjectId,
  activeWorkerId,
  onCloseTaskRoute,
  suggestedActor,
}: CarbonWorkspaceProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { actor } = useIdentity(suggestedActor);
  const [taskScope, setTaskScope] = useState<TaskScope>("project");
  const [sidebarFilter, setSidebarFilter] = useState<Filter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [theme, setTheme] = useState<Theme>(getTheme);
  const [connectOpen, setConnectOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const canUseClusterScope = Boolean(cluster && isMultiProjectCluster(cluster));
  const scope = useMemo<CarbonScope>(
    () => ({ home, clusterId: cluster?.id, projectId: project.id }),
    [home, cluster?.id, project.id],
  );
  const workspaceScopeKey = useMemo(() => carbonScopeKey(scope), [scope]);
  const clusterScope = useMemo<CarbonScope>(
    () => cluster ? { home, clusterId: cluster.id } : scope,
    [cluster, home, scope],
  );
  const storageKey = carbonStorageKey({ home, homeId, clusterId: cluster?.id, projectId: project.id });
  const workspaceProjects = cluster?.projects ?? [project];
  const notificationHomeScopes = useMemo<CarbonScope[]>(
    () => [
      ...clusters.map((item) => ({ home, clusterId: item.id })),
      ...standaloneProjects.map((item) => ({ home, projectId: item.id })),
    ],
    [clusters, home, standaloneProjects],
  );
  // Selecting the cluster view is an explicit widening by the user. Keep the
  // project id out of that request so reads and batch writes use the same,
  // current-cluster-only boundary rather than a project request with an
  // include-cluster escape hatch.
  const boardScope = taskScope === "cluster" && canUseClusterScope ? clusterScope : scope;
  const boardScopeKey = useMemo(() => carbonScopeKey(boardScope), [boardScope]);
  const taskQuery = useCarbonTasks(boardScope);
  useCarbonTaskEvents(boardScope);
  const tasks = useMemo(() => taskQuery.data?.available ? taskQuery.data.data.tasks ?? [] : [], [taskQuery.data]);
  const status = useMemo(() => carbonWorkflowStatus(home, tasks), [home, tasks]);
  const boardTasks = useMemo(
    () => tasks.filter((task) => matches(task, sidebarFilter, status)),
    [sidebarFilter, status, tasks],
  );
  const filterCounts = useMemo<Partial<Record<Filter, number>>>(() => ({
    all: tasks.filter((task) => matches(task, "all", status)).length,
    backlog: tasks.filter((task) => matches(task, "backlog", status)).length,
    ready: tasks.filter((task) => matches(task, "ready", status)).length,
    active: tasks.filter((task) => matches(task, "active", status)).length,
    stalled: tasks.filter((task) => matches(task, "stalled", status)).length,
    review: tasks.filter((task) => matches(task, "review", status)).length,
  }), [status, tasks]);

  useEffect(() => {
    if (!canUseClusterScope) setTaskScope("project");
  }, [canUseClusterScope]);

  // A project/scope change must never leave an old dialog or selection surface
  // poised to submit into the new request boundary. The workspace shell itself
  // stays mounted, so its view and layout remain visually stable.
  useEffect(() => {
    setCreateOpen(false);
    setConnectOpen(false);
    setSearchOpen(false);
  }, [boardScopeKey, workspaceScopeKey]);

  const taskNav: { id: Filter; label: string; icon: typeof ListTodo }[] = [
    { id: "all", label: t("All tasks", "全部任务"), icon: ListTodo },
    { id: "backlog", label: t("Backlog", "待办"), icon: CircleDashed },
    { id: "ready", label: t("Ready", "就绪"), icon: Sparkles },
  ];
  const agentWorkNav: { id: Extract<Filter, "active" | "stalled" | "review">; label: string; icon: typeof ListTodo }[] = [
    { id: "active", label: t("Active", "进行中"), icon: Activity },
    { id: "stalled", label: t("Stalled", "已停滞"), icon: ClockAlert },
    { id: "review", label: t("Awaiting review", "等待审核"), icon: ScanEye },
  ];
  const nav: { id: CarbonView; label: string; icon: typeof KanbanSquare }[] = [
    { id: "board", label: t("Board", "看板"), icon: KanbanSquare },
    { id: "graph", label: t("Graph", "依赖图"), icon: Network },
    { id: "workers", label: t("Worker", "Worker"), icon: UsersRound },
    { id: "work-logs", label: t("Work logs", "工作日志"), icon: ListChecks },
    { id: "owner-logs", label: t("Owner logs", "负责人日志"), icon: UsersRound },
    { id: "trash", label: t("Trash", "回收站"), icon: Trash2 },
  ];

  const selectTaskFilter = (filter: Filter) => {
    setSidebarFilter(filter);
    onNavigateView("board");
  };

  const openTask = useCallback((task: Task) => {
    // Carbon represents a cluster-wide task with an intentionally empty
    // `projectId`. It is not an absent value and must never become the current
    // workspace project by fallback.
    if (task.projectId === "") {
      if (cluster?.id) onOpenTaskRoute(cluster.id, project.id, task.id, "");
      else toast.message(t(
        "This cluster-wide task needs a cluster workspace before it can be opened.",
        "此集群任务需要在集群工作区中打开。",
      ));
      return;
    }
    const taskProjectId = task.projectId?.trim();
    if (taskProjectId) {
      onOpenTaskRoute(cluster?.id, project.id, task.id, taskProjectId);
      return;
    }
    // An id from a project-scoped task query is still safe: the query itself is
    // the authoritative project boundary. A cluster-wide query without task
    // metadata is ambiguous, so it intentionally does not navigate.
    if (boardScope.projectId) {
      onOpenTaskRoute(cluster?.id, project.id, task.id, boardScope.projectId);
      return;
    }
    toast.message(t(
      "Task scope is unavailable. Refresh the board before opening this task.",
      "任务范围不可用；请刷新看板后再打开该任务。",
    ));
  }, [boardScope.projectId, cluster, onOpenTaskRoute, project.id, t]);
  const openNotificationTask = (notification: Notif) => {
    const targetClusterId = notification.target?.clusterId ?? notification.clusterId;
    const targetProjectId = notification.target?.projectId ?? notification.projectId;
    if (targetClusterId && targetProjectId) {
      const workspaceProjectId = targetClusterId === cluster?.id ? project.id : targetProjectId;
      onOpenTaskRoute(targetClusterId, workspaceProjectId, notification.taskId, targetProjectId);
      return;
    }
    if (!targetClusterId && targetProjectId && standaloneProjects.some((candidate) => candidate.id === targetProjectId)) {
      onOpenTaskRoute(undefined, targetProjectId, notification.taskId, targetProjectId);
      return;
    }
    if (targetClusterId) {
      const destinationCluster = clusters.find((candidate) => candidate.id === targetClusterId);
      const workspaceProjectId = destinationCluster?.id === cluster?.id
        ? project.id
        : destinationCluster?.projects[0]?.id;
      // A cluster-only notification has no source project metadata. Preserve a
      // valid project only for chrome and mark the actual task scope explicitly.
      if (workspaceProjectId) onOpenTaskRoute(targetClusterId, workspaceProjectId, notification.taskId, "");
      return;
    }
    const target = tasks.find((task) => task.id === notification.taskId);
    if (target) openTask(target);
  };
  const openTaskTarget = useCallback((target: TaskNavigationTarget) => {
    const taskId = target.taskId.trim();
    if (!taskId) return;

    const targetClusterId = target.clusterId?.trim();
    const targetProjectId = target.projectId?.trim();
    const isExplicitClusterTask = target.projectId === "";

    if (targetProjectId) {
      if (targetClusterId) {
        const destinationCluster = targetClusterId === cluster?.id
          ? cluster
          : clusters.find((candidate) => candidate.id === targetClusterId);
        if (destinationCluster?.projects.some((candidate) => candidate.id === targetProjectId)) {
          // Preserve the active project chrome inside the same cluster. The
          // explicit fourth argument keeps every detail write bound to the
          // source project instead of that chrome project.
          const workspaceProjectId = destinationCluster.id === cluster?.id ? project.id : targetProjectId;
          onOpenTaskRoute(destinationCluster.id, workspaceProjectId, taskId, targetProjectId);
          return;
        }
      } else if (targetProjectId === project.id) {
        // A source that names the already-mounted project is unambiguous even
        // when its legacy DTO omitted the surrounding cluster id.
        onOpenTaskRoute(cluster?.id, project.id, taskId, targetProjectId);
        return;
      } else {
        const standaloneMatches = standaloneProjects.filter((candidate) => candidate.id === targetProjectId);
        const clusterMatches = clusters.filter((candidate) => candidate.projects.some((project) => project.id === targetProjectId));
        if (standaloneMatches.length === 1 && clusterMatches.length === 0) {
          onOpenTaskRoute(undefined, targetProjectId, taskId, targetProjectId);
          return;
        }
        if (standaloneMatches.length === 0 && clusterMatches.length === 1) {
          const destinationCluster = clusterMatches[0];
          const workspaceProjectId = destinationCluster.id === cluster?.id ? project.id : targetProjectId;
          onOpenTaskRoute(destinationCluster.id, workspaceProjectId, taskId, targetProjectId);
          return;
        }
      }
    } else if (isExplicitClusterTask && targetClusterId) {
      const destinationCluster = targetClusterId === cluster?.id
        ? cluster
        : clusters.find((candidate) => candidate.id === targetClusterId);
      const workspaceProjectId = destinationCluster?.id === cluster?.id
        ? project.id
        : destinationCluster?.projects[0]?.id;
      if (destinationCluster && workspaceProjectId) {
        onOpenTaskRoute(destinationCluster.id, workspaceProjectId, taskId, "");
        return;
      }
    } else if (target.projectId === undefined && target.clusterId === undefined) {
      // Missing source metadata can only reuse a task already loaded by the
      // current scoped query. Never infer a project for an arbitrary id.
      const task = tasks.find((candidate) => candidate.id === taskId);
      if (task) {
        openTask(task);
        return;
      }
    }

    toast.message(t(
      "Task scope is unavailable. Open it from a scoped board instead.",
      "任务范围不可用；请从对应范围的看板中打开该任务。",
    ));
  }, [cluster, clusters, onOpenTaskRoute, openTask, project.id, standaloneProjects, t, tasks]);
  const openWorker = useCallback((worker: string) => {
    if (onOpenWorker) onOpenWorker(worker);
    else onNavigateView("workers", worker);
  }, [onNavigateView, onOpenWorker]);
  const taskHref = useCallback((task: Task) => {
    const isClusterTask = task.projectId === "";
    if (isClusterTask && !cluster?.id) return window.location.href;
    const clusterTask = Boolean(cluster?.id && isClusterTask);
    const taskProjectId = task.projectId?.trim();
    if (!clusterTask && !taskProjectId && !boardScope.projectId) return window.location.href;
    const base = cluster?.id
      ? `#carbon/${encodeURIComponent(cluster.id)}/${encodeURIComponent(project.id)}`
      : `#carbon/project/${encodeURIComponent(project.id)}`;
    const query = new URLSearchParams();
    if (clusterTask) query.set("taskScope", "cluster");
    else if (taskProjectId && taskProjectId !== project.id) query.set("taskProject", taskProjectId);
    const suffix = query.toString();
    const hash = `${base}/task/${encodeURIComponent(task.id)}${suffix ? `?${suffix}` : ""}`;
    return `${window.location.origin}${window.location.pathname}${window.location.search}${hash}`;
  }, [boardScope.projectId, cluster, project.id]);

  return (
    <WorkerAliasProvider home={home}>
      <div className="flex h-full min-w-0 flex-col bg-app">
        <WorkspaceBackgroundContextMenu
          className="shrink-0"
          activeView={activeView}
          projectName={project.name}
          onNewTask={() => setCreateOpen(true)}
          onSearch={() => setSearchOpen(true)}
          onRefresh={() => { void queryClient.invalidateQueries({ queryKey: ["carbon"] }); }}
          onNavigate={onNavigateView}
          onSettings={() => setSettingsOpen(true)}
        >
          <header className="flex min-h-14 shrink-0 flex-wrap items-center gap-2 border-b bg-panel px-3 py-2">
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <BrandLogo className="size-7 shrink-0" />
            <CarbonProjectSwitcher
              clusters={clusters}
              standaloneProjects={standaloneProjects}
              cluster={cluster}
              project={project}
              home={home}
              presentation={presentation}
              catalogUpdatePending={catalogUpdatePending}
              onApplyCatalogUpdate={onApplyCatalogUpdate}
              onSelectProject={onSelectProject}
              onManage={onBack}
            />
          </div>
          <div className="flex flex-wrap items-center justify-end gap-2">
            <div className="flex rounded-lg border bg-app/40 p-0.5" aria-label={t("Task scope", "任务范围")}>
              <Button
                variant={taskScope === "project" ? "secondary" : "ghost"}
                size="sm"
                className="h-8 px-3 text-sm"
                aria-pressed={taskScope === "project"}
                title={t("Only tasks in this project", "仅显示当前项目任务")}
                onClick={() => setTaskScope("project")}
              >
                {t("Project", "项目")}
              </Button>
              <Button
                variant={taskScope === "cluster" ? "secondary" : "ghost"}
                size="sm"
                className={cn("h-8 px-3 text-sm", !canUseClusterScope && "hidden")}
                aria-pressed={taskScope === "cluster"}
                title={t("Tasks across this cluster", "显示整个集群的任务")}
                onClick={() => setTaskScope("cluster")}
              >
                {t("Cluster", "集群")}
              </Button>
            </div>
            <Button variant="outline" size="sm" onClick={() => setSearchOpen(true)}>
              <Search data-icon="inline-start" />
              {t("Search", "搜索")}
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <FilePlus2 data-icon="inline-start" />
              {t("New task", "新建任务")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setConnectOpen(true)}>
              <Plug data-icon="inline-start" />
              {t("Connect", "连接")}
            </Button>
            <CarbonNotificationBell
              scope={scope}
              actor={actor}
              options={{ aggregation: "home", homeId, homeScopes: notificationHomeScopes }}
              onOpenTask={openNotificationTask}
            />
            <Button variant="ghost" size="icon" aria-label={t("Settings", "设置")} onClick={() => setSettingsOpen(true)}>
              <Settings2 />
            </Button>
          </div>
          </header>
        </WorkspaceBackgroundContextMenu>

      <WorkspaceBackgroundContextMenu
        className="flex min-h-0 flex-1 flex-col sm:flex-row"
        activeView={activeView}
        projectName={project.name}
        onNewTask={() => setCreateOpen(true)}
        onSearch={() => setSearchOpen(true)}
        onRefresh={() => { void queryClient.invalidateQueries({ queryKey: ["carbon"] }); }}
        onNavigate={onNavigateView}
        onSettings={() => setSettingsOpen(true)}
      >
        <aside className="flex shrink-0 gap-1 overflow-x-auto border-b bg-panel p-2 sm:w-52 sm:flex-col sm:overflow-y-auto sm:border-r sm:border-b-0">
          <nav className="flex min-w-max flex-1 items-start gap-1 sm:min-w-0 sm:flex-col" aria-label={t("Workspace navigation", "工作区导航")}>
            <div className="flex shrink-0 items-start gap-1 sm:w-full sm:flex-col">
              <p className="hidden px-3 pb-1 pt-1.5 text-[11px] font-medium text-muted-foreground sm:block">
                {t("Tasks", "任务")}
              </p>
              {taskNav.map(({ id, label, icon: Icon }) => {
                const active = activeView === "board" && sidebarFilter === id;
                return (
                  <button
                    key={id}
                    type="button"
                    onClick={() => selectTaskFilter(id)}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-muted sm:w-full",
                      active && "bg-muted font-medium text-brand",
                    )}
                    >
                      <Icon className="size-4" />
                      <span>{label}</span>
                      <span className="ml-auto text-xs tabular-nums text-muted-foreground">{filterCounts[id] ?? 0}</span>
                    </button>
                );
              })}
            </div>
            <div className="hidden w-full border-t sm:block" />
            <div className="flex shrink-0 items-start gap-1 sm:w-full sm:flex-col">
              <p className="hidden px-3 pb-1 pt-1.5 text-[11px] font-medium text-muted-foreground sm:block">
                {t("Agent work", "智能体工作")}
              </p>
              {agentWorkNav.map(({ id, label, icon: Icon }) => {
                const active = activeView === "board" && sidebarFilter === id;
                return (
                  <button
                    key={id}
                    type="button"
                    onClick={() => selectTaskFilter(id)}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-muted sm:w-full",
                      active && "bg-muted font-medium text-brand",
                    )}
                  >
                    <Icon className="size-4" />
                    <span>{label}</span>
                    <span className="ml-auto text-xs tabular-nums text-muted-foreground">{filterCounts[id] ?? 0}</span>
                  </button>
                );
              })}
            </div>
            <div className="hidden w-full border-t sm:block" />
            <div className="flex shrink-0 items-start gap-1 sm:w-full sm:flex-col">
              <p className="hidden px-3 pb-1 pt-1.5 text-[11px] font-medium text-muted-foreground sm:block">
                {t("Carbon", "Carbon")}
              </p>
              {nav.map(({ id, label, icon: Icon }) => (
                <button
                  key={id}
                  type="button"
                  onClick={() => onNavigateView(id)}
                  aria-current={activeView === id ? "page" : undefined}
                  className={cn(
                    "flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-muted sm:w-full",
                    activeView === id && "bg-muted font-medium text-brand",
                  )}
                >
                  <Icon className="size-4" />
                  <span>{label}</span>
                </button>
              ))}
            </div>
          </nav>
          <div className="hidden w-full flex-col gap-1 border-t px-2 pt-2 sm:flex">
            <button
              type="button"
              onClick={() => setSettingsOpen(true)}
              className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-muted"
            >
              <span className="grid size-5 shrink-0 place-items-center rounded-full bg-muted text-[10px] font-medium text-muted-foreground">
                {(displayName(actor) || "?").slice(0, 1).toUpperCase()}
              </span>
              <span className="truncate">{displayName(actor) || t("Set your name", "设置你的名称")}</span>
            </button>
            <div className="flex min-w-0 items-center justify-between gap-2 px-2 pb-1">
              <span className="truncate text-xs text-muted-foreground">{project.name}</span>
              <div className="flex shrink-0 items-center gap-0.5">
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={t("Help", "帮助")}
                  title={t("Help", "帮助")}
                  onClick={() => setHelpOpen(true)}
                >
                  <HelpCircle />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={theme === "dark" ? t("Use light theme", "切换至浅色主题") : t("Use dark theme", "切换至深色主题")}
                  title={theme === "dark" ? t("Use light theme", "切换至浅色主题") : t("Use dark theme", "切换至深色主题")}
                  onClick={() => setTheme(toggleTheme())}
                >
                  {theme === "dark" ? <Sun /> : <Moon />}
                </Button>
              </div>
            </div>
          </div>
        </aside>

        <main className="min-h-0 min-w-0 flex-1 overflow-hidden">
          {activeTaskId ? (
            <CarbonTaskDetailPage
              home={home}
              homeId={homeId}
              cluster={cluster}
              project={project}
              taskId={activeTaskId}
              taskScope={activeTaskScope}
              taskProjectId={activeTaskProjectId}
              suggestedActor={suggestedActor}
              onBack={onCloseTaskRoute ?? (() => onNavigateView("board"))}
              onOpenTask={onOpenTaskRoute}
              onOpenWorker={openWorker}
            />
          ) : (
            <>
          {activeView === "board" && (
            <CarbonBoard
              scope={scope}
              bulkScope={boardScope}
              boardScopeKey={boardScopeKey}
              storageKey={storageKey}
              tasks={boardTasks}
              status={status}
              projects={workspaceProjects}
              loading={taskQuery.isLoading}
              unavailable={!taskQuery.isLoading && taskQuery.data?.available === false}
              onOpenTask={openTask}
              onOpenWorker={openWorker}
              taskHref={taskHref}
              onNewTask={() => setCreateOpen(true)}
              onRefresh={() => { void taskQuery.refetch(); }}
            />
          )}
          {activeView === "graph" && (
            <Suspense fallback={<div aria-busy="true" className="h-full animate-pulse rounded-lg bg-muted/20" />}>
              <GraphCanvas tasks={tasks} status={status} loading={taskQuery.isLoading} onOpenTask={(id) => {
                const task = tasks.find((item) => item.id === id);
                if (task) openTask(task);
              }} onNewTask={() => setCreateOpen(true)} onRefresh={() => { void taskQuery.refetch(); }} />
            </Suspense>
          )}
          {activeView === "workers" && (activeWorkerId ? (
            <OwnerLogs home={home} carbonScope={scope} actor={activeWorkerId} onOpenWorker={openWorker} onOpenTask={openTaskTarget} />
          ) : (
            <Workers home={home} carbonScope={scope} clusters={clusters} allowClusterScope={Boolean(cluster)} onOpenWorker={openWorker} onOpenTask={openTaskTarget} />
          ))}
          {activeView === "work-logs" && <WorkLogs home={home} carbonScope={scope} clusters={clusters} allowClusterScope={Boolean(cluster)} onOpenWorker={openWorker} onOpenTask={openTaskTarget} />}
          {activeView === "owner-logs" && <OwnerLogs home={home} carbonScope={scope} onOpenWorker={openWorker} onOpenTask={openTaskTarget} />}
          {activeView === "trash" && <Trash carbonScope={clusterScope} projects={workspaceProjects} />}
            </>
          )}
        </main>
      </WorkspaceBackgroundContextMenu>

      <CreateTaskDialog
        path={storageKey}
        carbonScope={scope}
        forceCarbon
        defaultProjectId={project.id}
        open={createOpen}
        onOpenChange={setCreateOpen}
      />
      <SettingsDialog
        open={settingsOpen}
        onOpenChange={(open) => {
          setSettingsOpen(open);
          if (!open) setTheme(getTheme());
        }}
        path=""
        carbonMode
        carbonScope={scope}
        notificationHomeId={homeId}
        suggestedActor={suggestedActor}
      />
      <Dialog open={connectOpen} onOpenChange={setConnectOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader><DialogTitle>{t("Connect MCP agent", "连接 MCP 智能体")}</DialogTitle><DialogDescription>{t("Use a project session by default, or choose a fixed scope for compatibility and advanced shared-pool work.", "默认使用可切换的项目会话；如需兼容旧配置或操作共享任务池，可选择固定范围。")}</DialogDescription></DialogHeader>
          <CarbonConnectPanel home={home} clusterId={cluster?.id} projects={workspaceProjects} defaultProjectId={project.id} defaultScope="session" />
        </DialogContent>
      </Dialog>
      <CarbonSearchDialog
        open={searchOpen}
        onOpenChange={setSearchOpen}
        scope={scope}
        clusters={clusters}
        standaloneProjects={standaloneProjects}
        onOpenTaskRoute={onOpenTaskRoute}
        onOpenTask={openTask}
      />
      <HelpDialog open={helpOpen} onOpenChange={setHelpOpen} />
      </div>
    </WorkerAliasProvider>
  );
}

function CarbonBoard({
  scope,
  bulkScope,
  boardScopeKey,
  storageKey,
  tasks,
  status,
  projects,
  loading,
  unavailable,
  onOpenTask,
  onOpenWorker,
  taskHref,
  onNewTask,
  onRefresh,
}: {
  scope: CarbonScope;
  bulkScope: CarbonScope;
  boardScopeKey: string;
  storageKey: string;
  tasks: Task[];
  status: Status;
  projects: CarbonHomeProject[];
  loading: boolean;
  unavailable: boolean;
  onOpenTask: (task: Task) => void;
  onOpenWorker?: (actor: string) => void;
  taskHref?: (task: Task) => string;
  onNewTask: () => void;
  onRefresh: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [priority, setPriority] = useState("");
  const [label, setLabel] = useState("");
  const [assignee, setAssignee] = useState("");
  const [bulkMode, setBulkMode] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [selectionScopeKey, setSelectionScopeKey] = useState(boardScopeKey);
  const [bulkLabels, setBulkLabels] = useState("");
  const [bulkMoveProjectId, setBulkMoveProjectId] = useState("");
  const [bulkMoveClusterWide, setBulkMoveClusterWide] = useState(false);
  const [bulkMoveForce, setBulkMoveForce] = useState(false);
  const [bulkMoveReason, setBulkMoveReason] = useState("");
  const [storedLocalViews, setStoredLocalViews] = useState<SavedView[]>(() => loadViews(storageKey));
  const [storedViewsKey, setStoredViewsKey] = useState(storageKey);
  const capabilities = useCarbonCapabilities(bulkScope);
  const savedViews = useCarbonSavedViews(scope);
  const taskTypes = useCarbonTaskTypes(scope);
  const createView = useCreateCarbonView(scope);
  const deleteView = useDeleteCarbonView(scope);
  // A project board remains project-bound. The only project-less mutation
  // scope comes from the explicit cluster board selection above.
  const bulkPatch = useBulkCarbonPatch(storageKey, bulkScope);
  const bulkMove = useBulkCarbonMove(storageKey, bulkScope);
  const trashTasks = useTrashCarbonTasks(storageKey, bulkScope);
  const transitionTask = useTransitionCarbonTask(storageKey, bulkScope);
  const bulkAvailable = capabilities.data?.available === true && hasCarbonFeature(capabilities.data.data, "bulk");
  const deferredQuery = useDeferredValue(query);
  const debouncedQuery = useDebouncedValue(deferredQuery, 150);
  const needle = debouncedQuery.trim().toLowerCase();
  const visible = useMemo(() => tasks.filter((task) =>
    (!needle || task.id.toLowerCase().includes(needle) || task.title.toLowerCase().includes(needle))
    && (!priority || task.priority === priority)
    && (!label || (task.labels ?? []).includes(label))
    && (!assignee || task.assignee === assignee),
  ), [assignee, label, needle, priority, tasks]);
  const selectedIds = selectionScopeKey === boardScopeKey ? selected : EMPTY_SELECTED_IDS;
  const localViews = storedViewsKey === storageKey ? storedLocalViews : [];
  const selectedTasks = useMemo(() => visible.filter((task) => selectedIds.has(task.id)), [selectedIds, visible]);
  const expectedVersions = useMemo(
    () => Object.fromEntries(selectedTasks.flatMap((task) => task.version ? [[task.id, task.version] as const] : [])),
    [selectedTasks],
  );
  const hasExpectedVersions = selectedTasks.length > 0 && selectedTasks.every((task) => Boolean(task.version));
  const bulkMoveNeedsForce = Boolean(
    (bulkMoveClusterWide || bulkMoveProjectId)
    && selectedTasks.some((task) => (task.projectId ?? "") !== (bulkMoveClusterWide ? "" : bulkMoveProjectId)),
  );
  const canBulkMove = Boolean(
    bulkAvailable
    && hasExpectedVersions
    && (bulkMoveClusterWide || bulkMoveProjectId)
    && (!bulkMoveNeedsForce || (bulkMoveForce && bulkMoveReason.trim())),
  );
  const typeOptions = useMemo(() => [...new Set([
    "foundation",
    "library",
    "patch",
    "extension",
    "plugin",
    ...(taskTypes.data?.available ? taskTypes.data.data.types ?? [] : []),
    ...tasks.map((task) => task.type).filter((value): value is string => Boolean(value)),
  ])].sort(), [taskTypes.data, tasks]);
  const serverViewsAvailable = savedViews.data?.available === true;
  const localViewsFallback = savedViews.data?.available === false;
  const remoteViews = serverViewsAvailable ? savedViews.data?.data?.views ?? [] : [];

  useEffect(() => {
    // The visual board stays mounted across a scope switch, but selections are a
    // write capability. Reset them synchronously on the new scope identity so an
    // old project's selected ids cannot be submitted into a new project/cluster.
    setSelectionScopeKey(boardScopeKey);
    setSelected(new Set());
    setBulkMode(false);
    setBulkLabels("");
    setBulkMoveProjectId("");
    setBulkMoveClusterWide(false);
    setBulkMoveForce(false);
    setBulkMoveReason("");
  }, [boardScopeKey]);

  useEffect(() => {
    // `useState(loadViews(...))` only runs once. Load on every stable project
    // identity instead, while the key comparison above avoids a one-render leak
    // of a previous project's fallback views.
    setStoredViewsKey(storageKey);
    setStoredLocalViews(loadViews(storageKey));
  }, [storageKey]);

  useEffect(() => {
    if (selectionScopeKey !== boardScopeKey) return;
    const ids = new Set(tasks.map((task) => task.id));
    setSelected((current) => new Set([...current].filter((id) => ids.has(id))));
  }, [boardScopeKey, selectionScopeKey, tasks]);

  const applyRemoteView = (view: (typeof remoteViews)[number]) => {
    setQuery(view.query.text ?? "");
    setPriority("");
    setLabel(view.query.labels?.[0] ?? "");
    setAssignee(view.query.assignee ?? "");
  };

  const applyLocalView = (view: SavedView) => {
    setQuery(view.query ?? "");
    setPriority(view.priority ?? "");
    setLabel(view.label ?? "");
    setAssignee(view.assignee ?? "");
  };

  const saveCurrentView = () => {
    const name = window.prompt(t("Save view as", "保存视图为"));
    if (!name?.trim()) return;
    if (serverViewsAvailable) {
      createView.mutate({ name: name.trim(), query: { text: query || undefined, labels: label ? [label] : undefined, assignee: assignee || undefined } });
      return;
    }
    if (localViewsFallback) {
      setStoredViewsKey(storageKey);
      setStoredLocalViews(addView(storageKey, { name: name.trim(), filter: "all", query, priority, label, assignee }));
    }
  };

  const boardFilters = useMemo(
    () => ({ query, priority, label, assignee }),
    [assignee, label, priority, query],
  );
  const handleTransition = useCallback((task: Task, to: string) => {
    transitionTask.mutate({ id: task.id, to });
  }, [transitionTask]);
  const requestTrash = useCallback((task: Task) => {
    if (!task.version) return;
    trashTasks.mutate({
      ids: [task.id],
      reason: "moved from task list",
      expectedVersions: { [task.id]: task.version },
    });
  }, [trashTasks]);
  const handleFiltersChange = useCallback((next: { query: string; priority: string; label: string; assignee: string }) => {
    setQuery(next.query);
    setPriority(next.priority);
    setLabel(next.label);
    setAssignee(next.assignee);
  }, []);
  const updateSelection = useCallback((next: Set<string>) => {
    setSelectionScopeKey(boardScopeKey);
    setSelected(next);
  }, [boardScopeKey]);
  const clearSelection = useCallback(() => {
    setSelectionScopeKey(boardScopeKey);
    setSelected(new Set());
  }, [boardScopeKey]);
  const submitBulkMove = () => {
    if (!canBulkMove) return;
    if (bulkMoveNeedsForce && !window.confirm(t(
      bulkMoveClusterWide
        ? "Move selected tasks to the shared cluster pool? This will replace their project assignment."
        : "Move selected tasks to another project? This will replace their project assignment.",
      bulkMoveClusterWide
        ? "确认将所选任务移动到集群共享池吗？这会替换其项目归属。"
        : "确认将所选任务移动到其他项目吗？这会替换其项目归属。",
    ))) return;
    bulkMove.mutate({
      ids: selectedTasks.map((task) => task.id),
      projectId: bulkMoveClusterWide ? "" : bulkMoveProjectId,
      clusterWide: bulkMoveClusterWide,
      expectedVersions,
      force: bulkMoveNeedsForce ? true : undefined,
      reason: bulkMoveNeedsForce ? bulkMoveReason.trim() : undefined,
    }, {
      onSuccess: (result) => {
        if (result.available) {
          clearSelection();
          setBulkMoveProjectId("");
          setBulkMoveClusterWide(false);
          setBulkMoveForce(false);
          setBulkMoveReason("");
        }
      },
    });
  };

  return (
    <div className="flex h-full min-w-0 flex-col">
      {bulkMode && selectedTasks.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/30 px-4 py-2">
          <ListChecks className="size-4" />
          <span className="text-sm font-medium">{t("{count} selected", "已选择 {count} 项", { count: selectedTasks.length })}</span>
          <Select
            value=""
            disabled={!bulkAvailable || !hasExpectedVersions || bulkPatch.isPending}
            onValueChange={(status) => bulkPatch.mutate({ ids: selectedTasks.map((task) => task.id), status, expectedVersions })}
          >
            <SelectTrigger className="h-8 w-36 text-sm">{t("Move to", "移动到")}</SelectTrigger>
            <SelectContent>
              {(status.states ?? []).map((state) => <SelectItem key={state} value={state}>{statusLabel(state)}</SelectItem>)}
            </SelectContent>
          </Select>
          <Select
            value=""
            disabled={!bulkAvailable || !hasExpectedVersions || bulkPatch.isPending}
            onValueChange={(priority) => bulkPatch.mutate({ ids: selectedTasks.map((task) => task.id), priority: priority === "none" ? "" : priority, expectedVersions })}
          >
            <SelectTrigger className="h-8 w-40 text-sm">{t("Set priority", "设置优先级")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="none">{t("No priority", "无优先级")}</SelectItem>
              {PRIORITIES.map((value) => <SelectItem key={value} value={value}>{priorityLabel(value)}</SelectItem>)}
            </SelectContent>
          </Select>
          <Select
            value=""
            disabled={!bulkAvailable || !hasExpectedVersions || bulkPatch.isPending || taskTypes.isLoading}
            onValueChange={(type) => bulkPatch.mutate({ ids: selectedTasks.map((task) => task.id), type, expectedVersions })}
          >
            <SelectTrigger className="h-8 w-36 text-sm">{t("Set type", "设置类型")}</SelectTrigger>
            <SelectContent>
              {typeOptions.map((value) => <SelectItem key={value} value={value}>{carbonTaskTypeLabel(value, t)}</SelectItem>)}
            </SelectContent>
          </Select>
          <Select
            value=""
            disabled={!bulkAvailable || !hasExpectedVersions || bulkPatch.isPending}
            onValueChange={(importance) => bulkPatch.mutate({ ids: selectedTasks.map((task) => task.id), importance, expectedVersions })}
          >
            <SelectTrigger className="h-8 w-40 text-sm">{t("Set importance", "设置重要性")}</SelectTrigger>
            <SelectContent>
              {(["core", "important", "normal", "optional", "experimental"] as const).map((value) => <SelectItem key={value} value={value}>{carbonImportanceLabel(value, t)}</SelectItem>)}
            </SelectContent>
          </Select>
          <Input
            value={bulkLabels}
            onChange={(event) => setBulkLabels(event.target.value)}
            disabled={!bulkAvailable || !hasExpectedVersions || bulkPatch.isPending}
            placeholder={t("Replace labels, comma-separated", "替换标签，用逗号分隔")}
            className="h-8 w-52 text-sm"
          />
          <Button
            variant="outline"
            size="sm"
            disabled={!bulkAvailable || !hasExpectedVersions || !bulkLabels.trim() || bulkPatch.isPending}
            onClick={() => bulkPatch.mutate({
              ids: selectedTasks.map((task) => task.id),
              labels: [...new Set(bulkLabels.split(",").map((value) => value.trim()).filter(Boolean))],
              expectedVersions,
            })}
          >
            {t("Replace labels", "替换标签")}
          </Button>
          <Select
            value={bulkMoveClusterWide ? "cluster" : bulkMoveProjectId || "none"}
            disabled={!bulkAvailable || !hasExpectedVersions || bulkMove.isPending}
            onValueChange={(target) => {
              if (target === "cluster") {
                setBulkMoveClusterWide(true);
                setBulkMoveProjectId("");
                return;
              }
              setBulkMoveClusterWide(false);
              setBulkMoveProjectId(target === "none" ? "" : target);
            }}
          >
            <SelectTrigger className="h-8 w-40 text-sm">{t("Move project", "移动项目")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="none">{t("Choose project", "选择项目")}</SelectItem>
              <SelectItem value="cluster">{t("Cluster shared pool", "集群共享池")}</SelectItem>
              {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
            </SelectContent>
          </Select>
          {bulkMoveNeedsForce && (
            <>
              <label className="flex items-center gap-1.5 text-xs">
                <Checkbox checked={bulkMoveForce} onCheckedChange={(checked) => setBulkMoveForce(checked === true)} />
                {t("Confirm project reassignment", "确认更改项目归属")}
              </label>
              <Input
                value={bulkMoveReason}
                onChange={(event) => setBulkMoveReason(event.target.value)}
                placeholder={t("Reason for cross-project move", "跨项目移动原因")}
                disabled={!bulkAvailable || bulkMove.isPending}
                className="h-8 w-56 text-sm"
              />
            </>
          )}
          <Button variant="outline" size="sm" disabled={!canBulkMove || bulkMove.isPending} onClick={submitBulkMove}>
            {t("Move tasks", "移动任务")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!bulkAvailable || !hasExpectedVersions || trashTasks.isPending}
            onClick={() => trashTasks.mutate({ ids: selectedTasks.map((task) => task.id), reason: "bulk trash from board", expectedVersions }, { onSuccess: (result) => result.available && clearSelection() })}
          >
            <Trash2 data-icon="inline-start" />
            {t("Trash", "移入回收站")}
          </Button>
          <Button variant="ghost" size="sm" className="ml-auto" onClick={clearSelection}>{t("Clear", "清除")}</Button>
          {!bulkAvailable && <span className="basis-full text-xs text-muted-foreground">{t("Bulk editing requires the Carbon stable v2 bulk capability.", "批量编辑需要 Carbon stable v2 的 bulk 能力。")}</span>}
          {!hasExpectedVersions && <span className="basis-full text-xs text-warning">{t("Refresh the selected tasks before a version-protected bulk operation.", "请先刷新选中任务，再执行受版本保护的批量操作。")}</span>}
        </div>
      )}

      {unavailable && (
        <div className="p-4">
          <Alert>
            <AlertTitle>{t("Scoped task API unavailable", "范围任务 API 不可用")}</AlertTitle>
            <AlertDescription>{t("Carbon never falls back to a filesystem path request.", "Carbon 不会回退为基于文件路径的请求。")}</AlertDescription>
          </Alert>
        </div>
      )}
      {loading && <p className="p-4 text-sm text-muted-foreground">{t("Loading tasks…", "正在加载任务…")}</p>}
      {!loading && !unavailable && (
        <CarbonTaskList
          storageKey={storageKey}
          tasks={tasks}
          status={status}
          onTransition={handleTransition}
          onTrashTask={requestTrash}
          transitioningId={transitionTask.isPending ? transitionTask.variables?.id : undefined}
          onOpenTask={onOpenTask}
          onOpenWorker={onOpenWorker}
          taskHref={taskHref}
          onNewTask={onNewTask}
          onRefresh={onRefresh}
          filters={boardFilters}
          onFiltersChange={handleFiltersChange}
          toolbarExtras={(
            <>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" disabled={savedViews.isLoading}>
                    <Bookmark className="size-3.5" />
                    {t("Views", "视图")}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-64">
                  <DropdownMenuLabel>{serverViewsAvailable ? t("Shared saved views", "共享保存视图") : localViewsFallback ? t("Local fallback views", "仅本机的兼容视图") : t("Loading views", "正在加载视图")}</DropdownMenuLabel>
                  {serverViewsAvailable && remoteViews.map((view) => (
                    <DropdownMenuItem key={view.id} onSelect={() => applyRemoteView(view)} className="justify-between">
                      <span className="truncate">{view.name}</span>
                      <button className="text-muted-foreground hover:text-destructive" aria-label={t("Delete {name}", "删除 {name}", { name: view.name })} onClick={(event) => { event.stopPropagation(); deleteView.mutate(view.id); }}>×</button>
                    </DropdownMenuItem>
                  ))}
                  {localViewsFallback && localViews.map((view) => (
                    <DropdownMenuItem key={view.name} onSelect={() => applyLocalView(view)} className="justify-between">
                      <span className="truncate">{view.name}</span>
                      <button className="text-muted-foreground hover:text-destructive" aria-label={t("Delete {name}", "删除 {name}", { name: view.name })} onClick={(event) => { event.stopPropagation(); setStoredViewsKey(storageKey); setStoredLocalViews(removeView(storageKey, view.name)); }}>×</button>
                    </DropdownMenuItem>
                  ))}
                  {(serverViewsAvailable ? remoteViews.length === 0 : localViewsFallback && localViews.length === 0) && <DropdownMenuItem disabled>{t("No saved views", "暂无保存视图")}</DropdownMenuItem>}
                  <DropdownMenuSeparator />
                  <DropdownMenuItem disabled={!serverViewsAvailable && !localViewsFallback} onSelect={saveCurrentView}>{t("Save current view", "保存当前视图")}</DropdownMenuItem>
                  {localViewsFallback && <p className="px-2 pb-1 text-[11px] text-muted-foreground">{t("Server views returned 404; this fallback is stored only on this device.", "服务端 views 返回 404；兼容视图仅保存在本机。")}</p>}
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                variant={bulkMode ? "secondary" : "ghost"}
                size="sm"
                className="h-7 gap-1 px-2 text-xs"
                onClick={() => {
                  if (bulkMode) clearSelection();
                  setBulkMode(!bulkMode);
                }}
              >
                <ListChecks className="size-3.5" />
                {t("Bulk", "批量")}
              </Button>
              <Badge variant="outline">{visible.length}</Badge>
            </>
          )}
          bulkMode={bulkMode}
          selectedIds={selectedIds}
          onSelectionChange={updateSelection}
        />
      )}
    </div>
  );
}


function CarbonSearchDialog({
  open,
  onOpenChange,
  scope,
  clusters,
  standaloneProjects,
  onOpenTaskRoute,
  onOpenTask,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scope: CarbonScope;
  clusters: CarbonHomeCluster[];
  standaloneProjects: CarbonHomeProject[];
  onOpenTaskRoute: (clusterId: string | undefined, workspaceProjectId: string, taskId: string, taskProjectId?: string) => void;
  onOpenTask: (task: Task) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const debouncedQuery = useDebouncedValue(query);
  const [searchScope, setSearchScope] = useState<"all" | "cluster" | "project">(() => scope.clusterId ? "all" : "project");
  const effectiveSearchScope = scope.clusterId ? searchScope : "project";
  const effectiveScope = useMemo<CarbonScope>(() => effectiveSearchScope === "all" ? { home: scope.home } : effectiveSearchScope === "cluster" ? { home: scope.home, clusterId: scope.clusterId } : scope, [effectiveSearchScope, scope]);
  const search = useCarbonSearch(effectiveScope, debouncedQuery, effectiveSearchScope !== "project");
  const results = search.data?.available ? search.data.data.results ?? [] : [];
  const openResult = (result: (typeof results)[number]) => {
    const task = result.task;
    const { id, projectId } = task;
    if (projectId) {
      if (!result.clusterId && standaloneProjects.some((project) => project.id === projectId)) {
        onOpenTaskRoute(undefined, projectId, id, projectId);
        onOpenChange(false);
        return;
      }
      const candidates = result.clusterId ? clusters.filter((cluster) => cluster.id === result.clusterId) : clusters;
      for (const cluster of candidates) {
        if (cluster.projects.some((project) => project.id === projectId)) {
          onOpenTaskRoute(cluster.id, projectId, id, projectId);
          onOpenChange(false);
          return;
        }
      }
    }
    if (result.clusterId) {
      const destinationCluster = clusters.find((candidate) => candidate.id === result.clusterId);
      const destinationProject = destinationCluster?.id === scope.clusterId
        ? scope.projectId
        : destinationCluster?.projects[0]?.id;
      // Search results with a cluster but no project are the cluster-wide task
      // representation. Keep a project for workspace chrome, but send an
      // explicit empty source project so the detail page cannot write through
      // that chrome project by accident.
      if (destinationProject) onOpenTaskRoute(result.clusterId, destinationProject, id, "");
      onOpenChange(false);
      return;
    }
    // Use the server-returned task directly; never look it up in the current
    // project list after a cross-project navigation.
    onOpenTask(task);
    onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader><DialogTitle>{scope.clusterId ? t("Global Carbon search", "Carbon 全局搜索") : t("Project search", "项目搜索")}</DialogTitle><DialogDescription>{scope.clusterId ? t("Results come from the scoped /api/search endpoint. The default searches all clusters in this Home.", "结果来自 scoped /api/search 接口；默认搜索此 Home 的全部集群。") : t("Search tasks only in this independent project's boundary.", "仅在当前独立项目的边界内搜索任务。")}</DialogDescription></DialogHeader>
        {scope.clusterId && <Select value={searchScope} onValueChange={(value) => setSearchScope(value as "all" | "cluster" | "project")}><SelectTrigger className="w-44">{searchScope === "all" ? t("All clusters", "全部集群") : searchScope === "cluster" ? t("This cluster", "当前集群") : t("This project", "当前项目")}</SelectTrigger><SelectContent><SelectItem value="all">{t("All clusters", "全部集群")}</SelectItem><SelectItem value="cluster">{t("This cluster", "当前集群")}</SelectItem><SelectItem value="project">{t("This project", "当前项目")}</SelectItem></SelectContent></Select>}
        <Input autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("Search tasks", "搜索任务")} />
        {query.trim() && (search.isLoading || query !== debouncedQuery) && <p className="text-sm text-muted-foreground">{t("Searching…", "正在搜索…")}</p>}
        {query.trim() && search.data?.available === false && <Alert><AlertTitle>{t("Search API unavailable", "搜索 API 不可用")}</AlertTitle><AlertDescription>{t("No current-list fallback is used for global Carbon search.", "Carbon 全局搜索不会回退为当前列表筛选。")}</AlertDescription></Alert>}
        <div className="max-h-80 overflow-y-auto rounded-md border">
          {results.map((result) => <button key={`${result.clusterId ?? "current"}:${result.task.projectId ?? "current"}:${result.task.id}`} onClick={() => openResult(result)} className="flex w-full flex-col gap-1 border-b px-3 py-2 text-left last:border-0 hover:bg-muted"><span className="font-medium">{result.task.title}</span><span className="font-mono text-xs text-muted-foreground">{result.task.id} {result.task.projectId ? `· ${result.task.projectId}` : ""}{result.clusterId ? ` · ${result.clusterId}` : ""}</span>{result.highlights?.[0]?.excerpt && <span className="line-clamp-2 text-xs text-muted-foreground">{result.highlights[0].excerpt}</span>}</button>)}
          {query.trim() && !search.isLoading && search.data?.available && !results.length && <p className="p-3 text-sm text-muted-foreground">{t("No matches.", "没有匹配项。")}</p>}
        </div>
      </DialogContent>
    </Dialog>
  );
}
