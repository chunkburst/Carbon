import { useEffect, useMemo, useState, type MouseEvent } from "react";
import { ChevronRight, FilePlus2, ListChecks, MessageSquareDashed, Search, SlidersHorizontal } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { WorkLogEditorDialog } from "@/components/WorkLogEditorDialog";
import { WorkLogDetailsDialog } from "@/components/WorkLogDetailsDialog";
import { WorkLogTable } from "@/components/WorkLogTable";
import { isWorkLogCoordinationDraft, WORK_LOG_VISIBILITIES, type TaskNavigationTarget, type WorkLog, type WorkLogDraft, type WorkLogFilters, type WorkLogVisibility } from "@/components/WorkLogTypes";
import { visibilityDetail } from "@/components/WorkLogVisibilityBadge";
import type {
  CarbonHomeCluster,
  CarbonScopeInput,
  CarbonWorkLogCreate,
  CarbonWorkLogUpdate,
} from "@/lib/carbon-api";
import {
  useCarbonWorkLogs,
  useCarbonWorkerIdentities,
  useCreateCarbonWorkLog,
  useDeleteCarbonWorkLog,
  useUpdateCarbonWorkLog,
} from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { useIdentity } from "@/lib/identity";

type ScopeMode = WorkLogFilters["scope"];
type EntryKind = "all" | "coordination" | "records";

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
  const [entryKind, setEntryKind] = useState<EntryKind>("all");
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
  const identitiesQuery = useCarbonWorkerIdentities(requestScope, canRead);
  const createLog = useCreateCarbonWorkLog(requestScope);
  const updateLog = useUpdateCarbonWorkLog(requestScope);
  const removeLog = useDeleteCarbonWorkLog(requestScope);
  const available = logsQuery.data?.available === true;
  const rawLogs = useMemo<WorkLog[]>(
    () => logsQuery.data?.available ? logsQuery.data.data.worklogs as WorkLog[] : [],
    [logsQuery.data],
  );
  const scopedLogs = useMemo<WorkLog[]>(() => rawLogs.filter((log) => {
    if (effectiveScopeMode === "all") return true;
    // Standalone project logs intentionally have no cluster ID. Compare a
    // cluster only when one was explicitly selected, then retain the regular
    // project boundary below.
    if (effectiveClusterId && log.clusterId !== effectiveClusterId) return false;
    return effectiveScopeMode !== "project" || log.projectId === effectiveProjectId;
  }), [effectiveClusterId, effectiveProjectId, effectiveScopeMode, rawLogs]);
  const logs = useMemo<WorkLog[]>(() => scopedLogs.filter((log) => {
    if (entryKind === "all") return true;
    const draft = isWorkLogCoordinationDraft(log);
    return entryKind === "coordination" ? draft : !draft;
  }), [entryKind, scopedLogs]);
  const identityModeEnabled = identitiesQuery.data?.available === true && identitiesQuery.data.data.modeEnabled;
  const pendingId = updateLog.isPending ? editorLog?.id : removeLog.isPending ? deleteLog?.id : undefined;
  const filtersActive = Boolean(worker.trim() || taskId.trim() || visibility !== "all" || entryKind !== "all");

  const resetFilters = () => {
    setWorker("");
    setTaskId("");
    setVisibility("all");
    setEntryKind("all");
  };
  const openCreate = () => {
    setWriteError("");
    setEditorLog(null);
  };
  const openEdit = (log: WorkLog) => {
    if (!log.version) {
      setWriteError(t("This work log is out of date. Refresh it before editing.", "这条工作日志已过期，请刷新后再编辑。"));
      return;
    }
    setWriteError("");
    setEditorLog(log);
  };
  const submit = async (draft: WorkLogDraft, expectedVersion?: string) => {
    setWriteError("");
    try {
      if (editorLog?.id) {
        if (!expectedVersion) throw new Error(t("Refresh this work log before saving your changes.", "请先刷新这条工作日志，再保存修改。"));
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
        if (!result.available) throw new Error(t("Work logs are not available in this Carbon installation yet.", "当前 Carbon 服务暂未提供工作日志功能。"));
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
        if (!result.available) throw new Error(t("Work logs are not available in this Carbon installation yet.", "当前 Carbon 服务暂未提供工作日志功能。"));
      }
      setEditorLog(undefined);
    } catch (error) {
      setWriteError(messageFor(error, t("Could not save this work log.", "无法保存这条工作日志。")));
      throw error;
    }
  };
  const confirmDelete = async () => {
    if (!deleteLog) return;
    if (!deleteLog.version) {
      setWriteError(t("This work log is out of date. Refresh it before deleting.", "这条工作日志已过期，请刷新后再删除。"));
      setDeleteLog(null);
      return;
    }
    setWriteError("");
    try {
      const result = await removeLog.mutateAsync({ id: deleteLog.id, expectedVersion: deleteLog.version });
      if (!result.available) throw new Error(t("Work logs are not available in this Carbon installation yet.", "当前 Carbon 服务暂未提供工作日志功能。"));
      setDeleteLog(null);
    } catch (error) {
      setWriteError(messageFor(error, t("Could not delete this work log.", "无法删除这条工作日志。")));
    }
  };

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel" onContextMenuCapture={preserveNativeTextContextMenu}>
      <header className="flex min-h-12 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <ListChecks className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">{t("Work logs", "工作日志")}</h1>
            <p className="truncate text-xs text-muted-foreground">{t("Keep progress, decisions, and handoffs in one place", "把进展、决策和交接集中记录下来")}</p>
          </div>
        </div>
        <Button size="sm" disabled={!canWrite} onClick={openCreate} title={!canWrite ? t("Choose a project before creating a work log.", "请选择项目后再创建工作日志。") : undefined}>
          <FilePlus2 data-icon="inline-start" />
          {t("New work log", "新建工作日志")}
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
            aria-label={t("Work log scope", "工作日志范围")}
          >
            <ToggleGroupItem value="all">{t("All", "全部")}</ToggleGroupItem>
            {allowClusterScope && <ToggleGroupItem value="cluster" disabled={!clusterId}>{t("Cluster", "集群")}</ToggleGroupItem>}
            <ToggleGroupItem value="project" disabled={!projectId}>{t("Project", "项目")}</ToggleGroupItem>
          </ToggleGroup>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        {identityModeEnabled && (
          <div className="flex items-start gap-2 border-b border-brand/20 bg-brand/8 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            <MessageSquareDashed className="mt-0.5 size-4 shrink-0 text-brand" />
            <p>
              <span className="font-medium text-foreground">{t("Team identity coordination is on.", "团队身份协作已启用。")}</span>{" "}
              {t(
                "Agents can share drafts before a task is taken over, choose teammates to receive them, continue a discussion, and pass work along. Regular private logs remain private.",
                "智能体可以在任务被接手前写下草稿、指定接收对象、延续讨论并分配后续工作；普通私有日志仍只对相关人员可见。",
              )}
            </p>
          </div>
        )}
        <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2">
          <div className="flex min-w-44 flex-1 items-center gap-2 rounded-md border bg-muted/15 px-2">
            <Search className="size-3.5 shrink-0 text-muted-foreground" />
            <Input value={worker} onChange={(event) => setWorker(event.target.value)} className="h-7 min-w-0 border-0 bg-transparent px-0 text-xs shadow-none focus-visible:ring-0" placeholder={t("Filter by agent or connection ID", "筛选智能体或连接标识")} />
          </div>
          <Input value={taskId} onChange={(event) => setTaskId(event.target.value)} className="h-8 w-44 font-mono text-xs" placeholder={t("Task ID", "任务 ID")} />
          <Select value={visibility} onValueChange={(value) => setVisibility(value as WorkLogVisibility | "all")}>
            <SelectTrigger className="h-8 min-w-36 text-xs">{visibility === "all" ? t("Any visibility", "任意可见性") : visibilityDetail(visibility, t).label}</SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("Any visibility", "任意可见性")}</SelectItem>
              {WORK_LOG_VISIBILITIES.map((item) => <SelectItem key={item} value={item}>{visibilityDetail(item, t).label}</SelectItem>)}
            </SelectContent>
          </Select>
          {identityModeEnabled && (
            <ToggleGroup
              type="single"
              value={entryKind}
              variant="outline"
              size="sm"
              spacing={0}
              onValueChange={(value) => value && setEntryKind(value as EntryKind)}
              aria-label={t("Work log entry type", "工作日志类型")}
            >
              <ToggleGroupItem value="all">{t("All", "全部")}</ToggleGroupItem>
              <ToggleGroupItem value="coordination">{t("Drafts", "草稿")}</ToggleGroupItem>
              <ToggleGroupItem value="records">{t("Records", "记录")}</ToggleGroupItem>
            </ToggleGroup>
          )}
          {filtersActive && <Button variant="ghost" size="sm" onClick={resetFilters}><SlidersHorizontal data-icon="inline-start" />{t("Reset", "重置")}</Button>}
          <span className="ml-auto text-xs text-muted-foreground">{logsQuery.isLoading ? t("Loading…", "加载中…") : `${logs.length} ${t("entries", "条")}`}</span>
        </div>

        {!available && !logsQuery.isLoading && (
          <Alert className="m-4 mb-0">
            <AlertTitle>{t("Work logs are not available yet", "工作日志暂不可用")}</AlertTitle>
            <AlertDescription>{t("This Carbon installation does not provide work logs for the selected project. Carbon will not invent records locally.", "当前 Carbon 服务未向所选项目提供工作日志。Carbon 不会在本地补造记录。")}</AlertDescription>
          </Alert>
        )}
        {writeError && <Alert variant="destructive" className="m-4 mb-0"><AlertTitle>{t("Could not update the work log", "工作日志操作未完成")}</AlertTitle><AlertDescription>{writeError}</AlertDescription></Alert>}
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
              canEdit={(log) => Boolean(currentActor && log.worker === currentActor && !isWorkLogCoordinationDraft(log))}
              canDelete={(log) => Boolean(currentActor && log.worker === currentActor && !isWorkLogCoordinationDraft(log))}
              emptyLabel={t("No work logs match this scope or filter.", "没有符合此范围或筛选条件的工作日志。")}
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
        title={t("Delete this work log?", "删除这条工作日志？")}
        description={t("This permanently removes this entry. Its linked task and record information cannot be restored.", "这会永久删除此条记录；关联任务和记录信息无法恢复。")}
        confirmLabel={t("Delete work log", "删除工作日志")}
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
