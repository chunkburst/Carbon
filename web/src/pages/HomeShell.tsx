import { useEffect, useMemo, useRef, useState } from "react";
import { Loader2, Settings2 } from "lucide-react";
import type { CatalogIconMutation } from "@/components/CatalogIcon";
import { FirstProjectWizard } from "@/components/FirstProjectWizard";
import { ProjectManagementPage, ProjectManagerDialog } from "@/components/ProjectManagerDialog";
import { SettingsDialog } from "@/components/SettingsDialog";
import { Button } from "@/components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import { useCarbonCatalogPresentation, useCarbonHome, useCarbonHomeCatalogUpdates, useSetCarbonCatalogIcon } from "@/lib/queries";
import {
  getLastProjectSelection,
  getProjectManagementPresentation,
  setLastProjectSelection,
} from "@/lib/personalization";
import { useI18n } from "@/lib/i18n";
import { CarbonWorkspace } from "@/pages/CarbonWorkspace";

type CarbonWorkspaceView = "board" | "graph" | "workers" | "work-logs" | "owner-logs" | "trash";

type HomeRoute = {
  view?: CarbonWorkspaceView | "manage";
  clusterId?: string;
  projectId?: string;
  standalone?: boolean;
  taskId?: string;
  /** Cluster-wide tasks retain the workspace project chrome but never its scope. */
  taskScope?: "cluster";
  /** Explicit source project for a task opened from a wider cluster board. */
  taskProjectId?: string;
  workerId?: string;
  legacyTaskQuery?: boolean;
};

type ProjectSelection = { clusterId?: string; projectId: string };

function decodeRouteSegment(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    return decodeURIComponent(value);
  } catch {
    return undefined;
  }
}

function parseRoute(): HomeRoute {
  const [routePath, query = ""] = window.location.hash.replace(/^#\/?/, "").split("?");
  const queryParams = new URLSearchParams(query);
  const parts = routePath.split("/").filter(Boolean);
  if (parts[0] !== "carbon") return {};
  if (parts[1] === "manage") return { view: "manage" };
  // `#carbon/project/:project` is the explicit, unambiguous standalone route.
  // Existing `#carbon/:cluster/:project` links keep their historical meaning.
  const standalone = parts[1] === "project";
  const clusterId = standalone ? undefined : decodeRouteSegment(parts[1]);
  const projectId = decodeRouteSegment(parts[2]);
  const tail = parts[3];
  const pathTaskId = tail === "task" ? decodeRouteSegment(parts[4]) : undefined;
  const queryTaskId = queryParams.get("task")?.trim() || undefined;
  const taskScope = clusterId && queryParams.get("taskScope") === "cluster" ? "cluster" : undefined;
  const taskProjectId = !taskScope ? queryParams.get("taskProject")?.trim() || undefined : undefined;
  const view: CarbonWorkspaceView = tail === "graph"
    ? "graph"
    : tail === "workers" || tail === "worker"
      ? "workers"
      : tail === "work-logs"
        ? "work-logs"
    : tail === "owner-logs"
          ? "owner-logs"
          : tail === "trash"
            ? "trash"
          : "board";
  return {
    view,
    clusterId,
    projectId,
    standalone,
    taskId: pathTaskId ?? queryTaskId,
    taskScope,
    taskProjectId,
    workerId: tail === "worker" ? decodeRouteSegment(parts[4]) : undefined,
    legacyTaskQuery: !pathTaskId && Boolean(queryTaskId),
  };
}

function routeHash(route: HomeRoute): string {
  if (route.view === "manage") return "#carbon/manage";
  const base = route.projectId
    ? route.clusterId
      ? `#carbon/${encodeURIComponent(route.clusterId)}/${encodeURIComponent(route.projectId)}`
      : `#carbon/project/${encodeURIComponent(route.projectId)}`
    : "#carbon";
  if (!route.projectId) return base;
  if (route.taskId) {
    const query = new URLSearchParams();
    if (route.taskScope === "cluster" && route.clusterId) query.set("taskScope", "cluster");
    else if (route.taskProjectId?.trim() && route.taskProjectId !== route.projectId) query.set("taskProject", route.taskProjectId);
    const suffix = query.toString();
    return `${base}/task/${encodeURIComponent(route.taskId)}${suffix ? `?${suffix}` : ""}`;
  }
  if (route.workerId) return `${base}/worker/${encodeURIComponent(route.workerId)}`;
  if (route.view && route.view !== "board") return `${base}/${route.view}`;
  return base;
}

function navigate(route: HomeRoute, replace = false): void {
  const next = routeHash(route);
  if (window.location.hash === next) return;
  if (replace) {
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${next}`);
    window.dispatchEvent(new Event("hashchange"));
    return;
  }
  window.location.hash = next;
}

function firstProject(clusters: CarbonHomeCluster[], projects: CarbonHomeProject[]): ProjectSelection | null {
  const standalone = projects[0];
  if (standalone) return { projectId: standalone.id };
  for (const cluster of clusters) {
    const project = cluster.projects[0];
    if (project) return { clusterId: cluster.id, projectId: project.id };
  }
  return null;
}

function findProject(clusters: CarbonHomeCluster[], projects: CarbonHomeProject[], selection?: ProjectSelection | null): ProjectSelection | null {
  if (!selection) return null;
  if (!selection.clusterId) return projects.some((project) => project.id === selection.projectId) ? { projectId: selection.projectId } : null;
  const project = clusters.find((cluster) => cluster.id === selection.clusterId)?.projects.find((item) => item.id === selection.projectId);
  return project ? { clusterId: selection.clusterId, projectId: selection.projectId } : null;
}

export function HomeShell({ initialHome, suggestedActor }: { initialHome?: string; suggestedActor?: string }) {
  const { t } = useI18n();
  const [route, setRoute] = useState<HomeRoute>(parseRoute);
  const [managerOpen, setManagerOpen] = useState(false);
  const managerOpenFrame = useRef<number | undefined>(undefined);
  const homeQuery = useCarbonHome(initialHome);
  const result = homeQuery.data;
  const data = result?.available ? result.data : undefined;
  const catalogUpdate = useCarbonHomeCatalogUpdates(initialHome, data?.root, data?.capabilities, result);
  const presentationQ = useCarbonCatalogPresentation(data?.root);
  const setIcon = useSetCarbonCatalogIcon(data?.root);
  const presentation = presentationQ.data?.available ? presentationQ.data.data : undefined;
  const setCatalogIcon = async (input: CatalogIconMutation) => {
    const result = await setIcon.mutateAsync(input);
    if (!result.available) throw new Error("Carbon catalog presentation API is unavailable");
  };
  const clusters = useMemo(() => data?.manifest?.clusters ?? [], [data?.manifest?.clusters]);
  const projects = useMemo(() => data?.manifest?.projects ?? [], [data?.manifest?.projects]);
  const homeId = data?.manifest?.id;
  const currentSelection = useMemo(
    () => findProject(
      clusters,
      projects,
      route.projectId ? { clusterId: route.clusterId, projectId: route.projectId } : null,
    ),
    [clusters, projects, route.clusterId, route.projectId],
  );
  const selectedCluster = currentSelection ? clusters.find((cluster) => cluster.id === currentSelection.clusterId) : undefined;
  const selectedProject = selectedCluster?.projects.find((project) => project.id === currentSelection?.projectId)
    ?? projects.find((project) => !currentSelection?.clusterId && project.id === currentSelection?.projectId);
  const hasProjects = Boolean(firstProject(clusters, projects));

  useEffect(() => {
    const onHash = () => setRoute(parseRoute());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => () => {
    if (managerOpenFrame.current !== undefined) window.cancelAnimationFrame(managerOpenFrame.current);
  }, []);

  // Carbon 0.3 used `?task=` inside a project hash. Keep old links working, but
  // immediately collapse them to the refreshable stable-v2 route so copied links and
  // browser history have one canonical task address.
  useEffect(() => {
    if (!route.legacyTaskQuery || !route.projectId || !route.taskId) return;
    navigate({ ...route, legacyTaskQuery: false }, true);
  }, [route]);

  // A base `#carbon` route is a launch route, not a management landing page. Keep
  // the remembered selection keyed by the manifest Home id so moving the portable
  // directory cannot leak an absolute data-home path into local storage.
  useEffect(() => {
    if (!data?.initialized || route.view === "manage" || !hasProjects) return;
    if (currentSelection) {
      const remembered = getLastProjectSelection(homeId);
      if (!remembered || remembered.clusterId !== currentSelection.clusterId || remembered.projectId !== currentSelection.projectId) {
        setLastProjectSelection(homeId, currentSelection);
      }
      return;
    }
    const destination = findProject(clusters, projects, getLastProjectSelection(homeId)) ?? firstProject(clusters, projects);
    if (destination) navigate({ ...destination }, true);
  }, [clusters, currentSelection, data?.initialized, hasProjects, homeId, projects, route.view]);

  const openProject = (selection: ProjectSelection) => {
    setManagerOpen(false);
    // Selecting a project closes the manager, so it is also a safe boundary for a
    // catalog update that arrived while its draft was open.
    catalogUpdate.apply();
    // A project switch changes only the scoped data underneath the workspace. Keep
    // the user's current surface (board, graph, worker/log views, or trash), but
    // deliberately omit task/worker route detail so an id from the previous
    // project can never remain selected against the new scope.
    const view: CarbonWorkspaceView = route.view && route.view !== "manage" ? route.view : "board";
    navigate({ ...selection, view });
  };

  const openManagerAfterCatalogApply = (openManager: () => void) => {
    const deferOpen = catalogUpdate.pending;
    catalogUpdate.apply();
    if (!deferOpen) {
      openManager();
      return;
    }
    if (managerOpenFrame.current !== undefined) window.cancelAnimationFrame(managerOpenFrame.current);
    // React Query applies synchronously, but rendering the manager a frame later
    // ensures its first visible list and draft state see the staged catalog.
    managerOpenFrame.current = window.requestAnimationFrame(() => {
      managerOpenFrame.current = undefined;
      openManager();
    });
  };

  const openProjectManager = () => {
    openManagerAfterCatalogApply(() => {
      if (getProjectManagementPresentation() === "page") {
        setManagerOpen(false);
        navigate({ view: "manage" });
        return;
      }
      setManagerOpen(true);
    });
  };

  const returnToTasks = () => {
    // A page-style manager keeps its draft stable until it has been left.
    catalogUpdate.apply();
    const destination = findProject(clusters, projects, getLastProjectSelection(homeId)) ?? firstProject(clusters, projects);
    if (destination) navigate({ ...destination, view: "board" });
  };

  if (homeQuery.isLoading) {
    return <div className="grid h-full place-items-center"><Loader2 className="animate-spin text-muted-foreground" /></div>;
  }

  if (!data) {
    return (
      <div className="grid h-full place-items-center p-6">
        <Card className="max-w-xl"><CardHeader><CardTitle>{t("Carbon Home is unavailable", "Carbon 主目录不可用")}</CardTitle><CardDescription>{t("This Carbon sidecar does not provide the Home API required by this build.", "当前 Carbon 本地服务未提供此版本所需的主目录 API。")}</CardDescription></CardHeader></Card>
      </div>
    );
  }

  if (!data?.initialized || !hasProjects) {
    return (
      <FirstProjectWizard
        home={data.root}
        initialized={data.initialized}
        clusters={clusters}
        projects={projects}
        onComplete={openProject}
      />
    );
  }

  if (route.view === "manage") {
    return (
      <ProjectManagementPage
        home={data.root}
        clusters={clusters}
        projects={projects}
        presentation={presentation}
        onSetIcon={setCatalogIcon}
        onOpenProject={openProject}
        onBack={returnToTasks}
        headerActions={<CarbonSettingsButton home={data.root} homeId={homeId} suggestedActor={suggestedActor} />}
      />
    );
  }

  if (!selectedProject) {
    // The startup redirect above resolves this on the next hash tick. Do not flash
    // the former text-heavy Home page while the user is being taken to a board.
    return <div className="grid h-full place-items-center"><Loader2 className="animate-spin text-muted-foreground" /></div>;
  }

  const openTaskRoute = (clusterId: string | undefined, workspaceProjectId: string, taskId: string, taskProjectId?: string) => {
    const clusterTask = Boolean(clusterId && taskProjectId !== undefined && taskProjectId.trim() === "");
    const explicitTaskProjectId = !clusterTask ? taskProjectId?.trim() || undefined : undefined;
    navigate({
      clusterId,
      projectId: workspaceProjectId,
      taskId,
      taskScope: clusterTask ? "cluster" : undefined,
      taskProjectId: explicitTaskProjectId && explicitTaskProjectId !== workspaceProjectId ? explicitTaskProjectId : undefined,
      view: "board",
    });
  };
  const openWorkerRoute = (actor: string) => {
    navigate({ clusterId: selectedCluster?.id, projectId: selectedProject.id, view: "workers", workerId: actor });
  };
  const navigateWorkspaceView = (view: CarbonWorkspaceView, workerId?: string) => {
    navigate({ clusterId: selectedCluster?.id, projectId: selectedProject.id, view, workerId });
  };

  return (
    <>
      <CarbonWorkspace
        home={data.root}
        homeId={homeId}
        clusters={clusters}
        standaloneProjects={projects}
        cluster={selectedCluster}
        project={selectedProject}
        presentation={presentation}
        catalogUpdatePending={catalogUpdate.pending}
        onApplyCatalogUpdate={catalogUpdate.apply}
        onSetIcon={setCatalogIcon}
        activeView={route.view ?? "board"}
        activeTaskId={route.taskId}
        activeTaskScope={route.taskScope}
        activeTaskProjectId={route.taskProjectId}
        activeWorkerId={route.workerId}
        onCloseTaskRoute={() => navigate({ clusterId: selectedCluster?.id, projectId: selectedProject.id, view: "board" })}
        onBack={openProjectManager}
        onSelectProject={(clusterId, projectId) => openProject({ clusterId, projectId })}
        onOpenTaskRoute={openTaskRoute}
        onOpenWorker={openWorkerRoute}
        onNavigateView={navigateWorkspaceView}
        suggestedActor={suggestedActor}
      />
      <ProjectManagerDialog
        open={managerOpen}
        onOpenChange={(open) => {
          if (!open && managerOpenFrame.current !== undefined) {
            window.cancelAnimationFrame(managerOpenFrame.current);
            managerOpenFrame.current = undefined;
          }
          setManagerOpen(open);
          if (!open) catalogUpdate.apply();
        }}
        home={data.root}
        clusters={clusters}
        projects={projects}
        presentation={presentation}
        onSetIcon={setCatalogIcon}
        onOpenProject={openProject}
        onOpenManagementPage={() => {
          setManagerOpen(false);
          openManagerAfterCatalogApply(() => navigate({ view: "manage" }));
        }}
      />
    </>
  );
}

function CarbonSettingsButton({ home, homeId, suggestedActor }: { home: string; homeId?: string; suggestedActor?: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="ghost" size="icon" aria-label={t("Settings", "设置")} onClick={() => setOpen(true)}><Settings2 /></Button>
      <SettingsDialog
        open={open}
        onOpenChange={setOpen}
        path=""
        carbonMode
        carbonScope={{ home }}
        notificationHomeId={homeId}
        showHomeBackup
        suggestedActor={suggestedActor}
      />
    </>
  );
}
