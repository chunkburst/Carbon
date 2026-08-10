import { useEffect, useMemo, useState, type MouseEvent } from "react";
import { ChevronRight, FilePlus2, ListChecks, Search, SlidersHorizontal } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { WorkLogEditorDialog } from "@/components/WorkLogEditorDialog";
import { WorkLogDetailsDialog } from "@/components/WorkLogDetailsDialog";
import { WorkLogTable } from "@/components/WorkLogTable";
import { WORK_LOG_VISIBILITIES, type TaskNavigationTarget, type WorkLog, type WorkLogDraft, type WorkLogFilters, type WorkLogVisibility } from "@/components/WorkLogTypes";
import { visibilityDetail } from "@/components/WorkLogVisibilityBadge";
import type {
  CarbonHomeCluster,
  CarbonScopeInput,
  CarbonWorkLogCreate,
  CarbonWorkLogUpdate,
} from "@/lib/carbon-api";
import {
  useCarbonWorkLogs,
  useCreateCarbonWorkLog,
  useDeleteCarbonWorkLog,
  useUpdateCarbonWorkLog,
} from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { useIdentity } from "@/lib/identity";

type ScopeMode = WorkLogFilters["scope"];

function preserveNativeTextContextMenu(event: MouseEvent<HTMLElement>) {
  const target = event.target;
  if (target instanceof Element && target.closest("input, textarea, [contenteditable='true']")) {
    event.stopPropagation();
  }
}

export type WorkLogsProps = {
  home?: string;
  carbonScope?: CarbonScopeInput;
  clusters?: CarbonHomeCluster[];
  /** False for an independent project: no cluster selector is rendered. */
  allowClusterScope?: boolean;
  onOpenWorker?: (actor: string) => void;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  initialWorker?: string;
  initialTaskId?: string;
  initialVisibility?: WorkLogVisibility;
};

/**
 * A compact audit ledger. Scope controls are local filters only; using them never
 * changes the project selected by Carbon's main project switcher.
 */
export function WorkLogs({
  home,
  carbonScope,
  clusters = [],
  allowClusterScope = true,
  onOpenWorker,
  onOpenTask,
  initialWorker = "",
  initialTaskId = "",
  initialVisibility,
}: WorkLogsProps) {
  const { t } = useI18n();
  const { actor: currentActor } = useIdentity();
  const carbonBase = useMemo(
    () => (typeof carbonScope === "object" && carbonScope !== null ? carbonScope : undefined),
    [carbonScope],
  );
  const resolvedHome = home ?? carbonBase?.home;
  const [scopeMode, setScopeMode] = useState<ScopeMode>(() =>
    carbonBase?.projectId ? "project" : carbonBase?.clusterId ? "cluster" : "all",
  );
  const [clusterId, setClusterId] = useState(carbonBase?.clusterId ?? "");
  const [projectId, setProjectId] = useState(carbonBase?.projectId ?? "");
  const [worker, setWorker] = useState(initialWorker);
  const [taskId, setTaskId] = useState(initialTaskId);
  const [visibility, setVisibility] = useState<WorkLogVisibility | "all">(initialVisibility ?? "all");
  const [editorLog, setEditorLog] = useState<WorkLog | null | undefined>(undefined);
  const [viewLog, setViewLog] = useState<WorkLog | null>(null);
  const [deleteLog, setDeleteLog] = useState<WorkLog | null>(null);
  const [writeError, setWriteError] = useState("");

  const selectedCluster = useMemo(() => clusters.find((cluster) => cluster.id === clusterId), [clusterId, clusters]);
  const selectableProjects = useMemo(() => selectedCluster?.projects ?? [], [selectedCluster]);

  useEffect(() => {
    if (allowClusterScope && !clusterId && clusters.length) setClusterId(clusters[0].id);
  }, [allowClusterScope, clusterId, clusters]);

  useEffect(() => {
    if (allowClusterScope && projectId && !selectableProjects.some((project) => project.id === projectId)) setProjectId("");
  }, [allowClusterScope, projectId, selectableProjects]);

  useEffect(() => {
    const nextClusterId = carbonBase?.clusterId ?? "";
    const nextProjectId = carbonBase?.projectId ?? "";
    setClusterId(nextClusterId);
    setProjectId(nextProjectId);
    setScopeMode(nextProjectId ? "project" : nextClusterId ? "cluster" : "all");
  }, [carbonBase?.clusterId, carbonBase?.projectId]);

  useEffect(() => {
    if (scopeMode === "project" && !projectId) setScopeMode(clusterId && allowClusterScope ? "cluster" : "all");
    if (scopeMode === "cluster" && (!clusterId || !allowClusterScope)) setScopeMode("all");
  }, [allowClusterScope, clusterId, projectId, scopeMode]);

  // A route can switch from a grouped project to an independent project
  // without unmounting this page. Treat that route as project-only even while
  // the prior grouped view's selector state is being reconciled.
  const effectiveScopeMode: ScopeMode = allowClusterScope ? scopeMode : "project";
  const effectiveClusterId = allowClusterScope ? clusterId : carbonBase?.clusterId ?? "";
  const effectiveProjectId = allowClusterScope ? projectId : carbonBase?.projectId ?? "";

  const requestScope = useMemo<CarbonScopeInput>(() => {
    if (!carbonBase) return carbonScope ?? (resolvedHome ? { home: resolvedHome } : "");
    // A grouped project may widen only through an explicit cluster selection;
    // an independent project retains its project anchor.
    return { home: resolvedHome, clusterId: effectiveClusterId || undefined, projectId: effectiveScopeMode === "project" ? effectiveProjectId || undefined : undefined };
  }, [carbonBase, carbonScope, effectiveClusterId, effectiveProjectId, effectiveScopeMode, resolvedHome]);
  const canRead = typeof requestScope === "string"
    ? Boolean(requestScope)
    : Boolean(requestScope.home && (requestScope.clusterId || requestScope.projectId));
  const canWrite = typeof requestScope === "string"
    ? Boolean(requestScope)
    : Boolean(requestScope.clusterId || requestScope.projectId || requestScope.legacyPath);
  const filters = useMemo(() => ({
    worker: worker.trim() || undefined,
    taskId: taskId.trim() || undefined,
    visibility: visibility === "all" ? undefined : visibility,
    projectId: effectiveScopeMode === "project" ? effectiveProjectId || undefined : undefined,
    limit: 100,
  }), [effectiveProjectId, effectiveScopeMode, taskId, visibility, worker]);
  const logsQuery = useCarbonWorkLogs(requestScope, filters, canRead);
  const createLog = useCreateCarbonWorkLog(requestScope);
  const updateLog = useUpdateCarbonWorkLog(requestScope);
  const removeLog = useDeleteCarbonWorkLog(requestScope);
  const available = logsQuery.data?.available === true;
  const rawLogs = useMemo<WorkLog[]>(
    () => logsQuery.data?.available ? logsQuery.data.data.worklogs as WorkLog[] : [],
    [logsQuery.data],
  );
  const logs = useMemo<WorkLog[]>(() => rawLogs.filter((log) => {
    if (effectiveScopeMode === "all") return true;
    // Standalone project logs intentionally have no cluster ID. Compare a
    // cluster only when one was explicitly selected, then retain the regular
    // project boundary below.
    if (effectiveClusterId && log.clusterId !== effectiveClusterId) return false;
    return effectiveScopeMode !== "project" || log.projectId === effectiveProjectId;
  }), [effectiveClusterId, effectiveProjectId, effectiveScopeMode, rawLogs]);
  const pendingId = updateLog.isPending ? editorLog?.id : removeLog.isPending ? deleteLog?.id : undefined;
  const filtersActive = Boolean(worker.trim() || taskId.trim() || visibility !== "all");

  const resetFilters = () => {
    setWorker("");
    setTaskId("");
    setVisibility("all");
  };
  const openCreate = () => {
    setWriteError("");
    setEditorLog(null);
  };
  const openEdit = (log: WorkLog) => {
    if (!log.version) {
      setWriteError(t("This Work Log has no optimistic version. Refresh it before editing.", "此 Work Log 没有乐观锁版本。请刷新后再编辑。"));
      return;
    }
    setWriteError("");
    setEditorLog(log);
  };
  const submit = async (draft: WorkLogDraft, expectedVersion?: string) => {
    setWriteError("");
    try {
      if (editorLog?.id) {
        if (!expectedVersion) throw new Error(t("A fresh Work Log version is required before saving.", "保存前需要最新的 Work Log 版本。"));
        const input: CarbonWorkLogUpdate = {
          visibility: draft.visibility,
          title: draft.title,
          // The HTTP compatibility contract uses empty strings to deliberately
          // clear optional fields; omissions mean leave the existing value alone.
          body: draft.body ?? "",
          projectId: draft.projectId ?? "",
          taskId: draft.taskId ?? "",
          tags: draft.tags ?? [],
          expectedVersion,
        };
        const result = await updateLog.mutateAsync({ id: editorLog.id, input });
        if (!result.available) throw new Error(t("Work Logs need a newer Carbon sidecar.", "Work Logs 需要更新的 Carbon sidecar。"));
      } else {
        const input: CarbonWorkLogCreate = {
          visibility: draft.visibility,
          title: draft.title,
          ...(draft.body ? { body: draft.body } : {}),
          ...(draft.projectId ? { projectId: draft.projectId } : {}),
          ...(draft.taskId ? { taskId: draft.taskId } : {}),
          ...(draft.tags?.length ? { tags: draft.tags } : {}),
        };
        const result = await createLog.mutateAsync(input);
        if (!result.available) throw new Error(t("Work Logs need a newer Carbon sidecar.", "Work Logs 需要更新的 Carbon sidecar。"));
      }
      setEditorLog(undefined);
    } catch (error) {
      setWriteError(messageFor(error, t("Could not save Work Log.", "无法保存 Work Log。")));
      throw error;
    }
  };
  const confirmDelete = async () => {
    if (!deleteLog) return;
    if (!deleteLog.version) {
      setWriteError(t("This Work Log has no optimistic version. Refresh it before deleting.", "此 Work Log 没有乐观锁版本。请刷新后再删除。"));
      setDeleteLog(null);
      return;
    }
    setWriteError("");
    try {
      const result = await removeLog.mutateAsync({ id: deleteLog.id, expectedVersion: deleteLog.version });
      if (!result.available) throw new Error(t("Work Logs need a newer Carbon sidecar.", "Work Logs 需要更新的 Carbon sidecar。"));
      setDeleteLog(null);
    } catch (error) {
      setWriteError(messageFor(error, t("Could not delete Work Log.", "无法删除 Work Log。")));
    }
  };

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel" onContextMenuCapture={preserveNativeTextContextMenu}>
      <header className="flex min-h-12 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <ListChecks className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">Work Logs</h1>
            <p className="truncate text-xs text-muted-foreground">{t("Operational notes with durable audit fields", "带有持久审计字段的运营记录")}</p>
          </div>
        </div>
        <Button size="sm" disabled={!canWrite} onClick={openCreate} title={!canWrite ? t("Choose a project cluster to create a Work Log.", "请选择项目集群后再创建 Work Log。") : undefined}>
          <FilePlus2 data-icon="inline-start" />
          {t("New Work Log", "新建 Work Log")}
        </Button>
      </header>

      {carbonBase && clusters.length > 0 && allowClusterScope && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/20 px-4 py-1.5">
          <span className="text-xs text-muted-foreground">{t("Read scope", "读取范围")}</span>
          <Select
            value={clusterId || undefined}
            onValueChange={(value) => {
              setClusterId(value);
              setProjectId("");
              if (scopeMode !== "all") setScopeMode("cluster");
            }}
          >
            <SelectTrigger className="h-7 max-w-60 min-w-36 text-xs">{clusterId ? selectedCluster?.name ?? clusterId : t("Choose cluster", "选择集群")}</SelectTrigger>
            <SelectContent>
              {clusters.map((cluster) => <SelectItem key={cluster.id} value={cluster.id}>{cluster.name}</SelectItem>)}
            </SelectContent>
          </Select>
          <ChevronRight className="size-3 text-muted-foreground" />
          <Select
            value={projectId || "all"}
            disabled={!clusterId}
            onValueChange={(value) => {
              const next = value === "all" ? "" : value;
              setProjectId(next);
              setScopeMode(next ? "project" : "cluster");
            }}
          >
            <SelectTrigger className="h-7 max-w-60 min-w-36 text-xs">{projectId ? selectableProjects.find((project) => project.id === projectId)?.name ?? projectId : t("All projects", "全部项目")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("All projects", "全部项目")}</SelectItem>
              {selectableProjects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
            </SelectContent>
          </Select>
          <ToggleGroup
            type="single"
            value={scopeMode}
            variant="outline"
            size="sm"
            spacing={0}
            onValueChange={(value) => value && setScopeMode(value as ScopeMode)}
            className="ml-auto"
            aria-label={t("Work Log scope", "Work Log 范围")}
          >
            <ToggleGroupItem value="all">{t("All", "全部")}</ToggleGroupItem>
            {allowClusterScope && <ToggleGroupItem value="cluster" disabled={!clusterId}>{t("Cluster", "集群")}</ToggleGroupItem>}
            <ToggleGroupItem value="project" disabled={!projectId}>{t("Project", "项目")}</ToggleGroupItem>
          </ToggleGroup>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
          <div className="flex min-w-44 flex-1 items-center gap-2 rounded-md border bg-muted/15 px-2">
            <Search className="size-3.5 shrink-0 text-muted-foreground" />
            <Input value={worker} onChange={(event) => setWorker(event.target.value)} className="h-7 min-w-0 border-0 bg-transparent px-0 text-xs shadow-none focus-visible:ring-0" placeholder={t("Filter canonical Worker", "筛选 canonical Worker")} />
          </div>
          <Input value={taskId} onChange={(event) => setTaskId(event.target.value)} className="h-8 w-44 font-mono text-xs" placeholder={t("Task ID", "任务 ID")} />
          <Select value={visibility} onValueChange={(value) => setVisibility(value as WorkLogVisibility | "all")}>
            <SelectTrigger className="h-8 min-w-36 text-xs">{visibility === "all" ? t("Any visibility", "任意可见性") : visibilityDetail(visibility, t).label}</SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("Any visibility", "任意可见性")}</SelectItem>
              {WORK_LOG_VISIBILITIES.map((item) => <SelectItem key={item} value={item}>{visibilityDetail(item, t).label}</SelectItem>)}
            </SelectContent>
          </Select>
          {filtersActive && <Button variant="ghost" size="sm" onClick={resetFilters}><SlidersHorizontal data-icon="inline-start" />{t("Reset", "重置")}</Button>}
          <span className="ml-auto text-xs text-muted-foreground">{logsQuery.isLoading ? t("Loading…", "加载中…") : `${logs.length} ${t("entries", "条")}`}</span>
        </div>

        {!available && !logsQuery.isLoading && (
          <Alert className="m-4 mb-0">
            <AlertTitle>{t("Work Logs need Carbon stable v2", "Work Logs 需要 Carbon stable v2")}</AlertTitle>
            <AlertDescription>{t("The sidecar did not expose the audit-log API. Carbon will not synthesize notes locally.", "当前 sidecar 未提供审计日志 API。Carbon 不会在本地伪造记录。")}</AlertDescription>
          </Alert>
        )}
        {writeError && <Alert variant="destructive" className="m-4 mb-0"><AlertTitle>{t("Work Log action failed", "Work Log 操作失败")}</AlertTitle><AlertDescription>{writeError}</AlertDescription></Alert>}
        <div className="min-w-0 flex-1 overflow-auto">
          {logsQuery.isLoading ? <WorkLogTableSkeleton /> : available && (
            <WorkLogTable
              logs={logs}
              pendingID={pendingId}
              onOpenWorker={onOpenWorker}
              onOpenTask={onOpenTask}
              onEdit={openEdit}
              onDelete={(log) => {
                setWriteError("");
                setDeleteLog(log);
              }}
              onView={setViewLog}
              canEdit={(log) => Boolean(currentActor && log.worker === currentActor)}
              canDelete={(log) => Boolean(currentActor && log.worker === currentActor)}
              emptyLabel={t("No Work Logs match this scope or filter.", "没有符合此范围或筛选条件的 Work Logs。")}
            />
          )}
        </div>
      </div>

      <WorkLogEditorDialog
        open={editorLog !== undefined}
        onOpenChange={(open) => !open && setEditorLog(undefined)}
        log={editorLog ?? undefined}
        defaultProjectId={effectiveScopeMode === "project" ? effectiveProjectId || undefined : undefined}
        pending={createLog.isPending || updateLog.isPending}
        onSubmit={submit}
      />
      <WorkLogDetailsDialog
        open={viewLog !== null}
        onOpenChange={(open) => !open && setViewLog(null)}
        log={viewLog}
        onOpenWorker={onOpenWorker}
        onOpenTask={onOpenTask}
      />
      <ConfirmDeleteDialog
        open={deleteLog !== null}
        onOpenChange={(open) => !open && setDeleteLog(null)}
        title={t("Delete Work Log?", "删除 Work Log？")}
        description={t("This permanently removes this operational record. Its task linkage and audit fields cannot be restored from the Work Log store.", "这会永久移除此运营记录。其任务关联和审计字段无法从 Work Log 存储恢复。")}
        confirmLabel={t("Delete Work Log", "删除 Work Log")}
        pending={removeLog.isPending}
        onConfirm={() => void confirmDelete()}
      />
    </div>
  );
}

function WorkLogTableSkeleton() {
  return (
    <div className="space-y-px">
      {Array.from({ length: 7 }, (_, index) => (
        <div key={index} className="grid h-14 grid-cols-[6rem_12rem_minmax(16rem,1fr)_8rem_10rem_9rem_2.5rem] items-center gap-3 border-b px-4">
          <div className="h-4 w-12 animate-pulse rounded bg-muted" />
          <div className="h-5 w-28 animate-pulse rounded bg-muted" />
          <div className="h-4 w-56 animate-pulse rounded bg-muted" />
          <div className="h-5 w-20 animate-pulse rounded bg-muted" />
          <div className="h-4 w-28 animate-pulse rounded bg-muted" />
          <div className="h-4 w-24 animate-pulse rounded bg-muted" />
        </div>
      ))}
    </div>
  );
}

function messageFor(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}
