import { Component, lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { FolderPlus, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Toaster } from "@/components/ui/sonner";
import { Button } from "@/components/ui/button";
import { AppSidebar, type Filter } from "@/components/AppSidebar";
import { AddProjectDialog } from "@/components/AddProjectDialog";
import { CreateTaskDialog } from "@/components/CreateTaskDialog";
import { EmptyState } from "@/components/EmptyState";
import { OpenProject } from "@/pages/OpenProject";
import { InitProject } from "@/pages/InitProject";
import { Board } from "@/pages/Board";
import { BoardView } from "@/pages/BoardView";
import { TaskDetail } from "@/pages/TaskDetail";
import { Connect } from "@/pages/Connect";
import { HomeShell } from "@/pages/HomeShell";
import { CommandPalette } from "@/components/CommandPalette";
import { CaptureView } from "@/components/CaptureView";
import { CarbonFloatingWindowView } from "@/components/CarbonFloatingWindowView";
import { SettingsDialog } from "@/components/SettingsDialog";
import { clusterQueryKey, useCluster, useStatus, useTaskEvents } from "@/lib/queries";
import { useDeepLinks, useDesktopMenu, useTrayMenu } from "@/lib/desktop-hooks";
import { closeFloatingBoardWindow } from "@/lib/desktop";
import { setCurrentClusterRoot, useCurrentClusterRoot } from "@/lib/clusters";
import { isTauri, pickFolder } from "@/lib/tauri";
import { lastWorkspace, registerWorkspace, resolveSlug } from "@/lib/workspaces";
import * as api from "@/lib/api";
import { useI18n } from "@/lib/i18n";

const Graph = lazy(() =>
  import("@/pages/Graph").then((module) => ({ default: module.Graph })),
);

const queryClient = new QueryClient({
  defaultOptions: { queries: { refetchOnWindowFocus: false, retry: false } },
});

// The global quick-add window loads the SPA at #capture and renders only the capture UI.
function isCaptureRoute(): boolean {
  return window.location.hash.replace(/^#\/?/, "") === "capture";
}

// A native floating board gets its own webview at #floating-board?project=… and must not mount
// the regular workspace shell behind it. The view itself validates every query value again.
function isFloatingBoardRoute(): boolean {
  return window.location.hash.replace(/^#\/?/, "").split("?", 1)[0] === "floating-board";
}

// macOS uses a frameless window (titleBarStyle: Overlay) — the traffic lights float over a
// slim draggable strip, Linear-style. Other platforms keep their native title bar.
function isMacDesktop(): boolean {
  return isTauri() && typeof navigator !== "undefined" && /Mac/i.test(navigator.userAgent);
}

function TitleBar() {
  if (!isMacDesktop()) return null;
  return <div data-tauri-drag-region className="h-7 shrink-0 bg-app" />;
}

type FloatingWindowErrorBoundaryState = { error: Error | null };

/**
 * A native floating webview must never fail into an uncloseable blank surface.  This boundary
 * keeps a tiny inline fallback independent of the rest of the app's CSS/components so a render
 * error, a chart/WebView incompatibility, or a stale cached bundle still leaves an escape hatch.
 */
class FloatingWindowErrorBoundary extends Component<{ children: ReactNode }, FloatingWindowErrorBoundaryState> {
  state: FloatingWindowErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): FloatingWindowErrorBoundaryState {
    return { error };
  }

  componentDidCatch(): void {
    // Keep the fallback quiet for users; the native shell remains responsible for diagnostics.
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main
        role="alert"
        style={{
          boxSizing: "border-box",
          display: "grid",
          minHeight: "100vh",
          placeContent: "center",
          gap: "12px",
          padding: "24px",
          background: "#10131c",
          color: "#f4f6fb",
          fontFamily: "Inter, Segoe UI, sans-serif",
          textAlign: "center",
        }}
      >
        <strong style={{ fontSize: "15px" }}>Carbon 工作窗暂时没有响应</strong>
        <span style={{ color: "#aeb7ca", fontSize: "13px" }}>可以安全关闭这个窗口，再从主界面重新打开。</span>
        <button
          type="button"
          onClick={() => { void closeFloatingBoardWindow(); }}
          style={{ justifySelf: "center", cursor: "pointer", border: "1px solid #5d6a86", borderRadius: "8px", padding: "7px 14px", background: "#20283a", color: "#f4f6fb" }}
        >
          关闭工作窗
        </button>
      </main>
    );
  }
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={200}>
        {isCaptureRoute() ? (
          <CaptureView />
        ) : isFloatingBoardRoute() ? (
          <FloatingWindowErrorBoundary>
            <CarbonFloatingWindowView />
          </FloatingWindowErrorBoundary>
        ) : (
          <div className="flex h-screen flex-col bg-app">
            <TitleBar />
            <div className="min-h-0 flex-1">
              <Flow />
            </div>
          </div>
        )}
        <Toaster richColors />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

// --- routing: #/<workspace-slug>/<view> ---
// The slug still maps to a project path locally. Cluster selection is deliberately kept
// outside the URL so existing workspace URLs and deep links keep working unchanged.
type View =
  | { kind: "list"; filter: Filter }
  | { kind: "task"; id: string }
  | { kind: "graph" }
  | { kind: "board" }
  | { kind: "connect" };
type Route = { slug: string | null; view: View };

const FILTERS: Filter[] = ["all", "active", "stalled", "review", "backlog", "ready"];
const DEFAULT_VIEW: View = { kind: "list", filter: "all" };

function parseHash(): Route {
  const parts = window.location.hash
    .replace(/^#\/?/, "")
    .split("/")
    .filter(Boolean)
    .map(decodeURIComponent);
  const slug = parts[0] ?? null;
  const rest = parts.slice(1);
  let view: View = DEFAULT_VIEW;
  if (rest[0] === "task" && rest[1]) view = { kind: "task", id: rest[1] };
  else if (rest[0] === "graph") view = { kind: "graph" };
  else if (rest[0] === "board") view = { kind: "board" };
  else if (rest[0] === "connect") view = { kind: "connect" };
  else if (FILTERS.includes(rest[0] as Filter)) view = { kind: "list", filter: rest[0] as Filter };
  return { slug, view };
}

function hashFor(slug: string, view: View): string {
  if (view.kind === "task") return `#/${slug}/task/${encodeURIComponent(view.id)}`;
  if (view.kind === "graph") return `#/${slug}/graph`;
  if (view.kind === "board") return `#/${slug}/board`;
  if (view.kind === "connect") return `#/${slug}/connect`;
  return `#/${slug}/${view.filter}`;
}

function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseHash);
  useEffect(() => {
    const onHash = () => setRoute(parseHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return route;
}

const projectKey = (project: api.ClusterProject) => `${project.id}:${project.path}`;

function syncClusterProjects(cluster: api.Cluster): void {
  // Register only the canonical server paths. This preserves readable legacy URLs but
  // makes all new cluster routes resolve to the path the server itself will use.
  cluster.projects.forEach((project) => registerWorkspace(project.path, { makeCurrent: false }));
  queryClient.setQueryData(clusterQueryKey(cluster.root), cluster);
}

function preferredProject(cluster: api.Cluster): api.ClusterProject | null {
  const last = lastWorkspace();
  const available = cluster.projects.filter((project) => !project.offline);
  return available.find((project) => project.path === last?.path) ?? available[0] ?? null;
}

function isCarbonMode(status: api.Status | undefined): boolean {
  if (!status) return false;
  if (status.scope?.mode === "carbon" || status.scope?.mode === "carbon_home") return true;

  // Scope is authoritative when available. The contract fallback keeps a current
  // stable-v2 Home sidecar recognizable if a transitional response omits scope.
  return status.requestedCompatLayer === "v2" &&
    status.stableCompatLayer === "v2" &&
    status.capabilities?.includes("home") === true;
}

function Flow() {
  const route = useRoute();
  const { t } = useI18n();
  const clusterRoot = useCurrentClusterRoot();
  // Probe status first. A Carbon sidecar uses its stable Home API and must never be
  // sent through the legacy /api/cluster?path= flow, even during initial loading.
  const { data: launchStatus } = useStatus("");
  const carbonMode = isCarbonMode(launchStatus);
  const { data: cluster, isLoading: clusterLoading, error: clusterError } = useCluster(
    launchStatus && !carbonMode ? clusterRoot : null,
  );
  const [addDialogOpen, setAddDialogOpen] = useState(false);
  const [choosingCluster, setChoosingCluster] = useState(false);
  const attemptedAdoptions = useRef(new Set<string>());
  const selectedClusterRoot = useRef<string | null>(clusterRoot);
  const clusterChooserActive = useRef(choosingCluster);
  useDeepLinks(); // route carbon:// opens (desktop only; no-op in the browser)

  useEffect(() => {
    if (carbonMode && !window.location.hash.startsWith("#carbon")) window.location.hash = "#carbon";
  }, [carbonMode]);

  useEffect(() => {
    selectedClusterRoot.current = clusterRoot;
    if (clusterRoot) setChoosingCluster(false);
  }, [clusterRoot]);

  useEffect(() => {
    clusterChooserActive.current = choosingCluster;
  }, [choosingCluster]);

  const openProject = useCallback((path: string, view: View = DEFAULT_VIEW) => {
    window.location.hash = hashFor(registerWorkspace(path), view);
  }, []);

  const activateCluster = useCallback(
    (next: api.Cluster, preserveRoute = false) => {
      syncClusterProjects(next);
      selectedClusterRoot.current = next.root;
      clusterChooserActive.current = false;
      setCurrentClusterRoot(next.root);
      setChoosingCluster(false);
      if (preserveRoute) return;
      const target = preferredProject(next);
      if (target) openProject(target.path);
      else window.location.hash = "#/";
    },
    [openProject],
  );

  const legacyPath = useMemo(() => {
    const fromRoute = route.slug ? resolveSlug(route.slug) : null;
    return fromRoute ?? (!route.slug ? lastWorkspace()?.path ?? null : null);
  }, [route.slug]);

  // Upgrade an old local route into a cluster once, without breaking it if the new endpoint
  // is unavailable or the folder cannot be adopted. POST only creates a manifest; project
  // task data remains in place. The project-scoped Codex entry is refreshed best-effort so
  // newly started Codex tasks bind to this project while already-running MCP processes stay put.
  useEffect(() => {
    if (carbonMode || choosingCluster || clusterRoot || !legacyPath || attemptedAdoptions.current.has(legacyPath)) return;
    attemptedAdoptions.current.add(legacyPath);
    void api
      .openCluster(legacyPath)
      .then((next) => {
        if (selectedClusterRoot.current || clusterChooserActive.current) return;
        syncClusterProjects(next);
        selectedClusterRoot.current = next.root;
        setCurrentClusterRoot(next.root);
        const adopted = next.projects.find((project) => project.path === legacyPath);
        if (adopted && !adopted.offline) {
          void api.connectAgent(adopted.path, "codex", "agent:codex").catch(() => {
            toast.error(
              t(
                "Project cluster opened. Couldn't refresh Codex MCP — retry from Connect.",
                "项目集群已打开，但无法刷新 Codex MCP；可在“连接”页面重试。",
              ),
            );
          });
        }
      })
      .catch(() => {
        // Keep rendering the legacy project below. A failed additive upgrade must not block it.
      });
  }, [carbonMode, choosingCluster, clusterRoot, legacyPath, t]);

  // An empty cluster should open the most recent member, never an unrelated old workspace.
  useEffect(() => {
    if (carbonMode || !cluster || route.slug || cluster.projects.length === 0) return;
    const target = preferredProject(cluster);
    if (target) openProject(target.path);
  }, [carbonMode, cluster, openProject, route.slug]);

  // A URL registered by another cluster must never become an implicit escape hatch into
  // that project's task/session context. Route only to members of the selected cluster.
  useEffect(() => {
    if (carbonMode || !cluster || !route.slug) return;
    const routePath = resolveSlug(route.slug);
    const member = cluster.projects.find((project) => project.path === routePath && !project.offline);
    if (member) return;
    const target = preferredProject(cluster);
    if (target) openProject(target.path);
    else window.location.hash = "#/";
  }, [carbonMode, cluster, openProject, route.slug]);

  const switchCluster = useCallback(async () => {
    if (!isTauri()) {
      // Leave the workspace registry untouched: browser users can type a different root on
      // the chooser page and still return to all existing deep links.
      clusterChooserActive.current = true;
      setChoosingCluster(true);
      setCurrentClusterRoot(null);
      window.location.hash = "#/";
      return;
    }
    const picked = await pickFolder();
    if (!picked) return;
    try {
      activateCluster(await api.openCluster(picked));
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t("Could not open cluster", "无法打开集群"));
    }
  }, [activateCluster, t]);

  const addProject = useCallback(
    async (candidate: string) => {
      if (!cluster) throw new Error(t("Open a project cluster first", "请先打开项目集群"));
      const previousProjects = new Set(cluster.projects.map(projectKey));
      const next = await api.addClusterProject(cluster.root, candidate);
      syncClusterProjects(next);
      const project =
        next.projects.find((entry) => !previousProjects.has(projectKey(entry))) ??
        next.projects.find((entry) => entry.path === candidate) ??
        null;

      if (!project) {
        // The server accepted the registration, but did not identify the project in its
        // response. Keep the manifest cache fresh rather than navigating to a guessed path.
        toast.success(t("Project added", "项目已添加"));
        return;
      }

      openProject(project.path);
      toast.success(t("Added {project}", "已添加 {project}", { project: project.name }));

      // This is deliberately best-effort. A registration must survive a project-scoped
      // Codex connection failure so the user can retry it from Connect.
      try {
        await api.connectAgent(project.path, "codex", "agent:codex");
        toast.success(t("Codex connected for {project}", "已为 {project} 连接 Codex", { project: project.name }));
      } catch {
        toast.error(
          t(
            "Project added. Couldn't connect Codex — retry from Connect.",
            "项目已添加，但无法连接 Codex；可在“连接”页面重试。",
          ),
        );
      }
    },
    [cluster, openProject, t],
  );

  const requestAddProject = useCallback(async () => {
    if (!cluster) return;
    if (!isTauri()) {
      setAddDialogOpen(true);
      return;
    }
    const picked = await pickFolder();
    if (!picked) return;
    try {
      await addProject(picked);
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : String(cause));
    }
  }, [addProject, cluster]);

  const renderProject = (path: string, currentCluster: api.Cluster | null) => {
    const slug = route.slug ?? registerWorkspace(path);
    const member = currentCluster?.projects.find((project) => project.path === path) ?? null;
    return (
      <Project
        key={path}
        path={path}
        cluster={currentCluster}
        currentProject={member}
        view={route.view}
        navigate={(view) => {
          window.location.hash = hashFor(slug, view);
        }}
        onSwitchCluster={() => void switchCluster()}
        onAddProject={() => void requestAddProject()}
        onSelectProject={(project) => openProject(project.path)}
      />
    );
  };

  if (carbonMode) return <HomeShell initialHome={launchStatus?.scope?.home} suggestedActor={launchStatus?.suggestedActor} />;

  let content: ReactNode;
  if (!clusterRoot) {
    // Existing hash routes and last-workspace storage remain valid even before (or after a
    // failed) automatic legacy adoption.
    content = !choosingCluster && legacyPath ? (
      renderProject(legacyPath, null)
    ) : (
      <OpenProject onOpen={(next) => activateCluster(next)} />
    );
  } else if (clusterLoading) {
    content = (
      <Centered>
        <Loader2 data-icon="loader" className="size-5 animate-spin text-muted-foreground" />
      </Centered>
    );
  } else if (clusterError || !cluster) {
    content = (
      <OpenProject
        onOpen={(next) => activateCluster(next)}
        notice={
          clusterError instanceof Error
            ? clusterError.message
            : t("Could not load the selected project cluster.", "无法加载所选项目集群。")
        }
      />
    );
  } else if (route.slug) {
    const path = resolveSlug(route.slug);
    const member = cluster.projects.find((project) => project.path === path && !project.offline);
    content = member ? renderProject(member.path, cluster) : <Centered><Loader2 className="size-5 animate-spin text-muted-foreground" /></Centered>;
  } else if (cluster.projects.length === 0) {
    content = <ClusterEmpty onAddProject={() => void requestAddProject()} onSwitchCluster={() => void switchCluster()} />;
  } else if (!preferredProject(cluster)) {
    content = <ClusterUnavailable onAddProject={() => void requestAddProject()} onSwitchCluster={() => void switchCluster()} />;
  } else {
    // The effect above moves to a member URL immediately. Keep the transition quiet.
    content = (
      <Centered>
        <Loader2 data-icon="loader" className="size-5 animate-spin text-muted-foreground" />
      </Centered>
    );
  }

  return (
    <>
      {content}
      <AddProjectDialog open={addDialogOpen} onOpenChange={setAddDialogOpen} onAdd={addProject} />
    </>
  );
}

function ClusterEmpty({
  onAddProject,
  onSwitchCluster,
}: {
  onAddProject: () => void;
  onSwitchCluster: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="h-full bg-background text-foreground">
      <EmptyState
        icon={FolderPlus}
        title={t("No projects in this cluster", "此集群还没有项目")}
        message={t(
          "Register an existing project to keep its work visible alongside the rest of this cluster.",
          "添加已有项目后，即可在这个集群中统一查看其工作进度。",
        )}
        action={{ label: t("Add project", "添加项目"), icon: FolderPlus, onClick: onAddProject }}
        secondaryAction={{ label: t("Switch cluster…", "切换项目集群…"), onClick: onSwitchCluster }}
      />
    </div>
  );
}

function ClusterUnavailable({
  onAddProject,
  onSwitchCluster,
}: {
  onAddProject: () => void;
  onSwitchCluster: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="h-full bg-background text-foreground">
      <EmptyState
        icon={FolderPlus}
        title={t("No available projects", "没有可用项目")}
        message={t(
          "The registered project folders are offline. Reconnect them or register another project.",
          "已登记的项目文件夹当前不可用。请恢复这些路径，或登记其他项目。",
        )}
        action={{ label: t("Add project", "添加项目"), icon: FolderPlus, onClick: onAddProject }}
        secondaryAction={{ label: t("Switch cluster…", "切换项目集群…"), onClick: onSwitchCluster }}
      />
    </div>
  );
}

function Project({
  path,
  cluster,
  currentProject,
  view,
  navigate,
  onSwitchCluster,
  onAddProject,
  onSelectProject,
}: {
  path: string;
  cluster: api.Cluster | null;
  currentProject: api.ClusterProject | null;
  view: View;
  navigate: (view: View) => void;
  onSwitchCluster: () => void;
  onAddProject: () => void;
  onSelectProject: (project: api.ClusterProject) => void;
}) {
  const { data: status, isLoading, error } = useStatus(path);
  const { t } = useI18n();

  if (isLoading)
    return (
      <Centered>
        <Loader2 data-icon="loader" className="size-5 animate-spin text-muted-foreground" />
      </Centered>
    );
  if (error || !status)
    return (
      <Centered>
        <div className="text-center">
          <p className="text-sm text-destructive">
            {error instanceof Error ? error.message : t("Could not open project", "无法打开项目")}
          </p>
          <Button variant="outline" size="sm" className="mt-4" onClick={onSwitchCluster}>
            {t("Choose another project cluster", "选择其他项目集群")}
          </Button>
        </div>
      </Centered>
    );
  if (!status.initialized)
    return <InitProject path={path} status={status} onChangeFolder={onSwitchCluster} />;
  return (
    <Workspace
      key={path}
      path={path}
      status={status}
      cluster={cluster}
      currentProject={currentProject}
      view={view}
      navigate={navigate}
      onSwitchCluster={onSwitchCluster}
      onAddProject={onAddProject}
      onSelectProject={onSelectProject}
    />
  );
}

function Workspace({
  path,
  status,
  cluster,
  currentProject,
  view,
  navigate,
  onSwitchCluster,
  onAddProject,
  onSelectProject,
}: {
  path: string;
  status: api.Status;
  cluster: api.Cluster | null;
  currentProject: api.ClusterProject | null;
  view: View;
  navigate: (view: View) => void;
  onSwitchCluster: () => void;
  onAddProject: () => void;
  onSelectProject: (project: api.ClusterProject) => void;
}) {
  const [creating, setCreating] = useState(false);
  const [createParent, setCreateParent] = useState<string | undefined>(undefined);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const newTask = () => {
    setCreateParent(undefined);
    setCreating(true);
  };
  const addSubtask = (parentId: string) => {
    setCreateParent(parentId);
    setCreating(true);
  };

  useTaskEvents(path, cluster?.root); // exactly one project SSE; cluster polling covers siblings

  useTrayMenu(path, cluster?.projects ?? [], {
    openTask: (id) => navigate({ kind: "task", id }),
    openFilter: (filter) => navigate({ kind: "list", filter: filter as Filter }),
    switchProject: (slug) => {
      window.location.hash = `#/${slug}/all`;
    },
    newTask,
    openSettings: () => setSettingsOpen(true),
  });
  useDesktopMenu({
    "menu:new_task": newTask,
    "menu:open_folder": onSwitchCluster,
    "menu:board": () => navigate({ kind: "board" }),
    "menu:graph": () => navigate({ kind: "graph" }),
    "menu:settings": () => setSettingsOpen(true),
  });

  return (
    <div className="flex h-full overflow-hidden bg-app text-foreground">
      <AppSidebar
        path={path}
        status={status}
        cluster={cluster}
        currentProject={currentProject}
        active={view.kind === "list" ? view.filter : null}
        graphActive={view.kind === "graph"}
        boardActive={view.kind === "board"}
        connectActive={view.kind === "connect"}
        onFilter={(filter) => navigate({ kind: "list", filter })}
        onGraph={() => navigate({ kind: "graph" })}
        onBoard={() => navigate({ kind: "board" })}
        onConnect={() => navigate({ kind: "connect" })}
        onSwitchCluster={onSwitchCluster}
        onAddProject={onAddProject}
        onSelectProject={onSelectProject}
        onNewTask={newTask}
        onOpenTask={(id) => navigate({ kind: "task", id })}
        onOpenSettings={() => setSettingsOpen(true)}
      />
      <main className="min-w-0 flex-1 p-2 pl-0">
        <div className="flex h-full flex-col overflow-hidden rounded-xl border bg-panel shadow-xs">
          {view.kind === "list" ? (
            <Board
              path={path}
              status={status}
              filter={view.filter}
              onOpenTask={(id) => navigate({ kind: "task", id })}
              onNewTask={newTask}
              onPickFilter={(filter) => navigate({ kind: "list", filter })}
            />
          ) : view.kind === "graph" ? (
            <Suspense fallback={<Centered><Loader2 className="size-5 animate-spin text-muted-foreground" /></Centered>}>
              <Graph
                path={path}
                status={status}
                onOpenTask={(id) => navigate({ kind: "task", id })}
                onBack={() => navigate({ kind: "list", filter: "all" })}
              />
            </Suspense>
          ) : view.kind === "board" ? (
            <BoardView
              path={path}
              status={status}
              onOpenTask={(id) => navigate({ kind: "task", id })}
              onNewTask={newTask}
            />
          ) : view.kind === "connect" ? (
            <Connect path={path} status={status} />
          ) : (
            <TaskDetail
              path={path}
              id={view.id}
              status={status}
              onBack={() => navigate({ kind: "list", filter: "all" })}
              onOpenTask={(id) => navigate({ kind: "task", id })}
              onAddSubtask={addSubtask}
            />
          )}
        </div>
      </main>
      <CreateTaskDialog
        path={path}
        open={creating}
        onOpenChange={setCreating}
        defaultParent={createParent}
      />
      <CommandPalette
        path={path}
        status={status}
        onView={(filter) => navigate({ kind: "list", filter })}
        onOpenTask={(id) => navigate({ kind: "task", id })}
        onNewTask={newTask}
        onChangeCluster={onSwitchCluster}
        onGraph={() => navigate({ kind: "graph" })}
        onBoard={() => navigate({ kind: "board" })}
      />
      <SettingsDialog
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        path={path}
        checkShell={status.checkShell}
        carbonMode={isCarbonMode(status)}
      />
    </div>
  );
}

function Centered({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center bg-background p-6 text-foreground">
      {children}
    </div>
  );
}
