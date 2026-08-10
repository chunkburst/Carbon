import { useEffect, useMemo, useState } from "react";
import {
  Activity,
  BarChart3,
  ChevronRight,
  Clock3,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  RotateCcw,
  Trash2,
  UserRoundX,
  UsersRound,
} from "lucide-react";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
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
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import { WorkerContextMenu } from "@/components/WorkerContextMenu";
import { priorityLabel } from "@/components/PriorityIcon";
import { recentWorkTaskNavigationTarget, type TaskNavigationTarget } from "@/components/WorkLogTypes";
import {
  useDeleteCarbonWorker,
  usePatchWorkerAlias,
  useResetCarbonWorker,
  useWorkerAliases,
  useWorkerMetrics,
} from "@/lib/queries";
import type { CarbonHomeCluster, CarbonScopeInput, CarbonWorkerMetric } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";
import { cn, labelTone, timeAgo } from "@/lib/utils";
import { formatWorkerActor, type WorkerAliasMap } from "@/lib/worker-aliases";

type Scope = "all" | "cluster" | "project";
const EMPTY_ALIASES: WorkerAliasMap = {};

export type WorkerRecentWork = {
  taskId: string;
  title: string;
  status: string;
  clusterId?: string;
  projectId?: string;
  activity?: string;
  at: string;
};

export type WorkerLifecycleRecord = {
  actor?: string;
  createdAt?: string;
  updatedAt?: string;
  resetAt?: string;
  deletedAt?: string;
};

export type WorkerAggregate = {
  taskCount?: number;
  active?: number;
  completed?: number;
  open?: number;
  averageCycleSeconds?: number;
  average_cycle_seconds?: number;
  cycleSamples?: number;
  cycle_samples?: number;
  reopened?: number;
  reopenRate?: number;
  reopen_rate?: number;
};

type WorkerMetric = CarbonWorkerMetric & {
  lastActivity?: string;
  last_activity?: string;
  recentWork?: WorkerRecentWork[];
  recent_work?: WorkerRecentWork[];
};

type WorkerMetricsPayload = {
  workers: WorkerMetric[];
  aggregate?: WorkerAggregate;
};

export type WorkerLifecycleActions = {
  /** Reset only derived Worker metrics. Task files, assignments, leases, and provenance stay untouched. */
  onResetWorker?: (actor: string) => Promise<unknown> | unknown;
  /** Tombstone a Worker and clear its display alias; a later activity can recreate it. */
  onDeleteWorker?: (actor: string) => Promise<unknown> | unknown;
  pendingActor?: string;
};

export type WorkersProps = {
  /** Explicit home is accepted for route-level consistency; CarbonScope.home remains authoritative when omitted. */
  home?: string;
  path?: string;
  carbonScope?: CarbonScopeInput;
  clusters?: CarbonHomeCluster[];
  /** False for an independent project: keep home/project reports but hide cluster-only controls. */
  allowClusterScope?: boolean;
  /** Home-global lifecycle records keyed by canonical actor, supplied by the route's registry query. */
  workerRegistry?: Readonly<Record<string, WorkerLifecycleRecord>> | readonly WorkerLifecycleRecord[];
  lifecycleActions?: WorkerLifecycleActions;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  /** Canonical route callback for /worker/:actor. */
  onOpenWorker?: (actor: string) => void;
  /** @deprecated Use onOpenWorker. Retained for an already-mounted 0.3 shell. */
  onOpenOwner?: (actor: string) => void;
};

function duration(value?: number): string {
  if (value === undefined || value === null || value < 0) return "—";
  const minutes = Math.round(value / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(value / 3600);
  if (hours < 24) return `${hours}h`;
  return `${(hours / 24).toFixed(hours % 24 ? 1 : 0)}d`;
}

function percent(value?: number): string {
  if (value === undefined || value === null) return "—";
  return `${Math.round(value * (value <= 1 ? 100 : 1))}%`;
}

function completedCount(completedByPriority?: Partial<Record<string, number>>): number {
  return Object.values(completedByPriority ?? {}).reduce<number>((total, value) => total + (value ?? 0), 0);
}

function registryByActor(input: WorkersProps["workerRegistry"]): Record<string, WorkerLifecycleRecord> {
  if (!input) return {};
  if (Array.isArray(input)) {
    return Object.fromEntries(input.filter((record) => record.actor).map((record) => [record.actor!, record]));
  }
  return Object.fromEntries(Object.entries(input as Readonly<Record<string, WorkerLifecycleRecord>>));
}

/**
 * The Worker roster is intentionally a dense operating table instead of a dashboard.
 * Its local scope selector only changes reporting scope; it never changes the main
 * workspace route or the project selected in the global project switcher.
 */
export function Workers({
  home,
  path = "",
  carbonScope,
  clusters = [],
  allowClusterScope = true,
  workerRegistry,
  lifecycleActions,
  onOpenTask,
  onOpenWorker,
  onOpenOwner,
}: WorkersProps) {
  const { t } = useI18n();
  const carbonBase = useMemo(
    () => (typeof carbonScope === "object" && carbonScope !== null ? carbonScope : undefined),
    [carbonScope],
  );
  const resolvedHome = home ?? carbonBase?.home;
  const [scope, setScope] = useState<Scope>(() =>
    carbonScope && typeof carbonScope !== "string" && !carbonScope.clusterId && !carbonScope.projectId ? "all" : "project",
  );
  const [clusterId, setClusterId] = useState(carbonBase?.clusterId ?? "");
  const [projectId, setProjectId] = useState(carbonBase?.projectId ?? "");
  const [editingActor, setEditingActor] = useState<string | null>(null);
  const [aliasDraft, setAliasDraft] = useState("");
  const [lifecycle, setLifecycle] = useState<{ actor: string; kind: "reset" | "delete"; stage: 1 | 2 } | null>(null);
  const [confirmationText, setConfirmationText] = useState("");
  const [lifecyclePending, setLifecyclePending] = useState(false);

  const selectedCluster = useMemo(() => clusters.find((cluster) => cluster.id === clusterId), [clusterId, clusters]);
  const selectableProjects = useMemo(() => selectedCluster?.projects ?? [], [selectedCluster]);

  useEffect(() => {
    if (allowClusterScope && projectId && !selectableProjects.some((project) => project.id === projectId)) setProjectId("");
  }, [allowClusterScope, projectId, selectableProjects]);

  useEffect(() => {
    const nextClusterId = carbonBase?.clusterId ?? "";
    const nextProjectId = carbonBase?.projectId ?? "";
    setClusterId(nextClusterId);
    setProjectId(nextProjectId);
    setScope(nextProjectId ? "project" : "all");
  }, [carbonBase?.clusterId, carbonBase?.projectId]);

  useEffect(() => {
    if (scope === "project" && !projectId) setScope(clusterId && allowClusterScope ? "cluster" : "all");
    if (scope === "cluster" && (!clusterId || !allowClusterScope)) setScope("all");
  }, [allowClusterScope, clusterId, projectId, scope]);

  // An independent project has no widening selector. Keep a stale view state
  // from a previously opened grouped project from turning its metrics into a
  // home-wide request when the route changes in place.
  const effectiveScope: Scope = allowClusterScope ? scope : "project";
  const effectiveClusterId = allowClusterScope ? clusterId : carbonBase?.clusterId ?? "";
  const effectiveProjectId = allowClusterScope ? projectId : carbonBase?.projectId ?? "";
  const recentWorkSourceScope = useMemo<Pick<TaskNavigationTarget, "clusterId" | "projectId"> | undefined>(() => {
    if (effectiveScope === "all") return undefined;
    return {
      ...(effectiveClusterId ? { clusterId: effectiveClusterId } : {}),
      ...(effectiveScope === "project" && effectiveProjectId ? { projectId: effectiveProjectId } : {}),
    };
  }, [effectiveClusterId, effectiveProjectId, effectiveScope]);

  const metricScope = useMemo<CarbonScopeInput>(() => {
    if (!carbonBase) return carbonScope ?? path;
    return { home: resolvedHome, clusterId: effectiveClusterId || undefined, projectId: effectiveProjectId || undefined };
  }, [carbonBase, carbonScope, effectiveClusterId, effectiveProjectId, path, resolvedHome]);
  const metrics = useWorkerMetrics(metricScope, effectiveScope);
  const aliasesQuery = useWorkerAliases(resolvedHome);
  const patchAlias = usePatchWorkerAlias(resolvedHome);
  const resetWorker = useResetCarbonWorker(resolvedHome);
  const deleteWorker = useDeleteCarbonWorker(resolvedHome);
  const available = metrics.data?.available === true;
  const payload = metrics.data?.available ? (metrics.data.data as WorkerMetricsPayload) : undefined;
  const aliases = aliasesQuery.data?.available ? aliasesQuery.data.data.aliases : EMPTY_ALIASES;
  const aliasAvailable = aliasesQuery.data?.available === true;
  const openWorker = onOpenWorker ?? onOpenOwner;
  const registry = useMemo(() => registryByActor(workerRegistry), [workerRegistry]);

  const workers = useMemo(() => {
    const byActor = new Map<string, WorkerMetric>((payload?.workers ?? []).map((worker) => [worker.actor, worker]));
    for (const actor of Object.keys(aliases)) {
      if (!byActor.has(actor)) byActor.set(actor, { actor, active: 0, completed: 0 });
    }
    for (const [actor, record] of Object.entries(registry)) {
      if (!record.deletedAt && !byActor.has(actor)) byActor.set(actor, { actor, active: 0, completed: 0 });
    }
    return [...byActor.values()].sort((left, right) =>
      formatWorkerActor(left.actor, aliases).localeCompare(formatWorkerActor(right.actor, aliases), undefined, { sensitivity: "base" }),
    );
  }, [aliases, payload?.workers, registry]);

  const aggregate = payload?.aggregate;
  const active = aggregate?.active ?? workers.reduce<number>((sum, worker) => sum + worker.active, 0);
  const completed = aggregate?.completed ?? workers.reduce<number>((sum, worker) => sum + (worker.completed || completedCount(worker.completedByPriority ?? worker.completed_by_priority)), 0);
  const averageCycle = aggregate?.averageCycleSeconds ?? aggregate?.average_cycle_seconds;
  const reopenRate = aggregate?.reopenRate ?? aggregate?.reopen_rate;
  const taskCount = aggregate?.taskCount ?? completed + active;
  // A central route can still provide a custom implementation, but the normal
  // Carbon route is self-contained: lifecycle mutations are home-only and never
  // touch a task file. Keep the component usable without a routing adapter.
  const resolvedLifecycleActions: WorkerLifecycleActions | undefined = lifecycleActions ?? (resolvedHome
    ? {
      onResetWorker: async (actor) => {
        const result = await resetWorker.mutateAsync(actor);
        if (!result.available) throw new Error("Worker lifecycle needs a newer Carbon sidecar");
        return result.data;
      },
      onDeleteWorker: async (actor) => {
        const result = await deleteWorker.mutateAsync(actor);
        if (!result.available) throw new Error("Worker lifecycle needs a newer Carbon sidecar");
        return result.data;
      },
    }
    : undefined);

  const closeAliasEditor = () => {
    setEditingActor(null);
    setAliasDraft("");
  };
  const openAliasEditor = (actor: string) => {
    setEditingActor(actor);
    setAliasDraft(aliases[actor] ?? "");
  };
  const saveAlias = () => {
    if (!editingActor || !aliasDraft.trim()) return;
    patchAlias.mutate({ actor: editingActor, alias: aliasDraft.trim() }, { onSuccess: (result) => result.available && closeAliasEditor() });
  };
  const removeAlias = () => {
    if (!editingActor) return;
    patchAlias.mutate({ actor: editingActor, alias: "" }, { onSuccess: (result) => result.available && closeAliasEditor() });
  };
  const startLifecycle = (actor: string, kind: "reset" | "delete") => {
    setConfirmationText("");
    setLifecycle({ actor, kind, stage: 1 });
  };
  const finishLifecycle = async () => {
    if (!lifecycle || confirmationText !== lifecycle.actor) return;
    const action = lifecycle.kind === "reset" ? resolvedLifecycleActions?.onResetWorker : resolvedLifecycleActions?.onDeleteWorker;
    if (!action) return;
    setLifecyclePending(true);
    try {
      await action(lifecycle.actor);
      setLifecycle(null);
      setConfirmationText("");
    } catch {
      // Query mutations surface their failure through the shared toast boundary. Keep
      // the second confirmation open so the user can retry after resolving it.
    } finally {
      setLifecyclePending(false);
    }
  };

  const scopeLabel = t(
    effectiveScope === "all" ? "All clusters" : effectiveScope === "cluster" ? "Selected cluster" : "Selected project",
    effectiveScope === "all" ? "全部集群" : effectiveScope === "cluster" ? "所选集群" : "所选项目",
  );

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-12 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <UsersRound className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">Worker</h1>
            <p className="truncate text-xs text-muted-foreground">{t("Roster, delivery health, and recent work", "名册、交付指标与最近工作")}</p>
          </div>
        </div>
        {allowClusterScope && <ToggleGroup
          type="single"
          value={scope}
          variant="outline"
          size="sm"
          spacing={0}
          onValueChange={(value) => value && setScope(value as Scope)}
          aria-label={t("Worker reporting scope", "Worker 报告范围")}
        >
          <ToggleGroupItem value="all">{t("All", "全部")}</ToggleGroupItem>
          {allowClusterScope && <ToggleGroupItem value="cluster" disabled={!clusterId}>{t("Cluster", "集群")}</ToggleGroupItem>}
          <ToggleGroupItem value="project" disabled={!projectId}>{t("Project", "项目")}</ToggleGroupItem>
        </ToggleGroup>}
      </header>

      {carbonBase && clusters.length > 0 && allowClusterScope && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/20 px-4 py-1.5">
          <span className="text-xs text-muted-foreground">{t("Report", "报告")}</span>
          <Select
            value={clusterId || "all"}
            onValueChange={(value) => {
              const next = value === "all" ? "" : value;
              setClusterId(next);
              setProjectId("");
              if (!next) setScope("all");
              else if (scope === "all" || scope === "project") setScope("cluster");
            }}
          >
            <SelectTrigger className="h-7 max-w-60 min-w-36 text-xs">{clusterId ? selectedCluster?.name ?? clusterId : t("All clusters", "全部集群")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("All clusters", "全部集群")}</SelectItem>
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
              setScope(next ? "project" : "cluster");
            }}
          >
            <SelectTrigger className="h-7 max-w-60 min-w-36 text-xs">{projectId ? selectableProjects.find((project) => project.id === projectId)?.name ?? projectId : t("All projects", "全部项目")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("All projects", "全部项目")}</SelectItem>
              {selectableProjects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
            </SelectContent>
          </Select>
          <span className="ml-auto text-xs text-muted-foreground">{scopeLabel}</span>
        </div>
      )}

      <div className="min-w-0 flex-1 overflow-y-auto">
        {!available && !metrics.isLoading && (
          <Alert className="m-4 mb-0 rounded-lg">
            <AlertTitle>{t("Worker metrics need Carbon stable v2", "Worker 指标需要 Carbon stable v2")}</AlertTitle>
            <AlertDescription>
              {t(
                "This view does not estimate delivery data from incomplete task records. Upgrade the local sidecar to enable scoped Worker metrics.",
                "此视图不会从不完整任务记录估算交付数据。请升级本地 sidecar 以启用带范围的 Worker 指标。",
              )}
            </AlertDescription>
          </Alert>
        )}

        <section className="grid grid-cols-2 border-b sm:grid-cols-4 xl:grid-cols-5" aria-label={t("Worker aggregate metrics", "Worker 汇总指标")}>
          <AggregateMetric icon={Activity} label={t("Active", "活跃中")} value={available ? String(active) : "—"} />
          <AggregateMetric icon={BarChart3} label={t("Completed", "已完成")} value={available ? String(completed) : "—"} />
          <AggregateMetric icon={Clock3} label={t("Average cycle", "平均周期")} value={available ? duration(averageCycle) : "—"} detail={aggregate?.cycleSamples ? t("{count} samples", "{count} 个样本", { count: aggregate.cycleSamples ?? aggregate.cycle_samples ?? 0 }) : undefined} />
          <AggregateMetric icon={RefreshCw} label={t("Reopen rate", "重开率")} value={available ? percent(reopenRate) : "—"} />
          <AggregateMetric className="col-span-2 hidden xl:block" icon={UsersRound} label={t("Tasks in scope", "范围内任务")} value={available ? String(taskCount) : "—"} />
        </section>

        <section className="min-w-0">
          <div className="flex h-10 items-center justify-between border-b px-4">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-medium">{t("Worker roster", "Worker 名册")}</h2>
              {!metrics.isLoading && <span className="text-xs text-muted-foreground">{workers.length}</span>}
            </div>
            <span className="text-xs text-muted-foreground">{t("Activity, delivery, and recent work", "活动、交付与最近工作")}</span>
          </div>

          {metrics.isLoading || aliasesQuery.isLoading ? (
            <WorkerTableSkeleton />
          ) : workers.length ? (
            <Table className="min-w-[900px] text-[13px]">
              <TableHeader>
                <TableRow>
                  <TableHead className="h-8 w-56 px-4 text-xs">Worker</TableHead>
                  <TableHead className="h-8 w-28 text-xs">{t("Activity", "活动")}</TableHead>
                  <TableHead className="h-8 w-48 text-xs">{t("Completed", "已完成")}</TableHead>
                  <TableHead className="h-8 w-24 text-xs">{t("Cycle", "周期")}</TableHead>
                  <TableHead className="h-8 w-24 text-xs">{t("Reopen", "重开")}</TableHead>
                  <TableHead className="h-8 text-xs">{t("Recent work", "最近工作")}</TableHead>
                  <TableHead className="h-8 w-11 px-2 text-right text-xs"><span className="sr-only">{t("Actions", "操作")}</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {workers.map((worker) => {
                  const record = registry[worker.actor];
                  const byPriority = worker.completedByPriority ?? worker.completed_by_priority;
                  const count = worker.completed || completedCount(byPriority);
                  const recent = worker.recentWork ?? worker.recent_work ?? [];
                  const lastActivity = worker.lastActivity ?? worker.last_activity;
                  const pending = lifecyclePending || resolvedLifecycleActions?.pendingActor === worker.actor;
                  const canonicalLifecycleActor = worker.actor !== "unassigned";
                  return (
                    <WorkerContextMenu
                      key={worker.actor}
                      actor={worker.actor}
                      displayName={formatWorkerActor(worker.actor, aliases)}
                      pending={pending}
                      onOpenWorker={openWorker}
                      onEditAlias={canonicalLifecycleActor && aliasAvailable ? () => openAliasEditor(worker.actor) : undefined}
                      onReset={canonicalLifecycleActor && resolvedLifecycleActions?.onResetWorker ? () => startLifecycle(worker.actor, "reset") : undefined}
                      onDelete={canonicalLifecycleActor && resolvedLifecycleActions?.onDeleteWorker ? () => startLifecycle(worker.actor, "delete") : undefined}
                    >
                    <tr
                      tabIndex={0}
                      data-carbon-context-surface
                      aria-label={t("Worker {actor}", "Worker {actor}", { actor: worker.actor })}
                      className="group h-13 border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted"
                    >
                      <TableCell className="px-4 py-2">
                        <button
                          type="button"
                          className={cn("block min-w-0 text-left", openWorker && "rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
                          onClick={() => openWorker?.(worker.actor)}
                          disabled={!openWorker}
                        >
                          <WorkerIdentity actor={worker.actor} aliases={aliases} active={worker.active > 0} deleted={Boolean(record?.deletedAt)} />
                        </button>
                      </TableCell>
                      <TableCell className="py-2">
                        <div className="space-y-0.5">
                          <span className={cn("text-sm font-medium", worker.active > 0 && "text-success")}>{worker.active}</span>
                          <span className="ml-1 text-xs text-muted-foreground">{t("active", "活跃")}</span>
                          {lastActivity && <p className="truncate text-[10px] text-muted-foreground">{timeAgo(lastActivity)}</p>}
                        </div>
                      </TableCell>
                      <TableCell className="py-2">
                        <div className="flex max-w-44 flex-wrap items-center gap-1">
                          <span className="mr-1 text-sm font-medium">{count}</span>
                          {Object.entries(byPriority ?? {}).map(([priority, value]) => (
                            <Badge key={priority} variant="secondary" className={cn("carbon-label h-4 px-1.5 text-[10px]", labelTone(priority))}>
                              {priorityLabel(priority)} {value}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="py-2 font-mono text-xs">{duration(worker.averageCycleSeconds ?? worker.average_cycle_seconds)}</TableCell>
                      <TableCell className="py-2 font-mono text-xs">{percent(worker.reopenRate ?? worker.reopen_rate)}</TableCell>
                      <TableCell className="max-w-80 py-2">
                        <RecentWork items={recent} sourceScope={recentWorkSourceScope} onOpenTask={onOpenTask} empty={t("No recent task activity", "暂无最近任务活动")} />
                      </TableCell>
                      <TableCell className="px-2 py-2 text-right">
                        <WorkerActions
                          actor={worker.actor}
                          aliases={aliases}
                          aliasAvailable={canonicalLifecycleActor && aliasAvailable}
                          pending={pending}
                          canReset={canonicalLifecycleActor && Boolean(resolvedLifecycleActions?.onResetWorker)}
                          canDelete={canonicalLifecycleActor && Boolean(resolvedLifecycleActions?.onDeleteWorker)}
                          onEditAlias={() => openAliasEditor(worker.actor)}
                          onReset={() => startLifecycle(worker.actor, "reset")}
                          onDelete={() => startLifecycle(worker.actor, "delete")}
                          labels={{
                            actions: t("Worker actions", "Worker 操作"),
                            alias: t("Edit alias", "编辑别名"),
                            reset: t("Reset metrics", "重置指标"),
                            delete: t("Delete Worker", "删除 Worker"),
                          }}
                        />
                      </TableCell>
                    </tr>
                    </WorkerContextMenu>
                  );
                })}
              </TableBody>
            </Table>
          ) : (
            <div className="px-4 py-12 text-center text-sm text-muted-foreground">
              {t("No Worker activity in this reporting scope yet.", "此报告范围内暂无 Worker 活动。")}
            </div>
          )}
        </section>
      </div>

      <Dialog open={editingActor !== null} onOpenChange={(open) => !open && closeAliasEditor()}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("Worker alias", "Worker 别名")}</DialogTitle>
            <DialogDescription>
              {t("The alias is display-only. Carbon continues to use the canonical actor for assignment and audit records.", "别名仅用于显示；Carbon 仍会使用原始 actor 进行分配和审计记录。")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <p className="rounded-md border bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground">{editingActor}</p>
            <label className="grid gap-1.5 text-sm">
              <span>{t("Alias", "别名")}</span>
              <Input
                autoFocus
                value={aliasDraft}
                maxLength={256}
                placeholder={t("For example: codex1", "例如：codex1")}
                onChange={(event) => setAliasDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    saveAlias();
                  }
                }}
              />
            </label>
          </div>
          <DialogFooter className="sm:justify-between">
            <Button variant="destructive" disabled={!editingActor || !aliases[editingActor] || patchAlias.isPending} onClick={removeAlias}>
              <Trash2 data-icon="inline-start" />
              {t("Remove alias", "移除别名")}
            </Button>
            <div className="flex gap-2">
              <Button variant="outline" onClick={closeAliasEditor}>{t("Cancel", "取消")}</Button>
              <Button disabled={!aliasDraft.trim() || patchAlias.isPending} onClick={saveAlias}>{t("Save alias", "保存别名")}</Button>
            </div>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <WorkerLifecycleDialog
        state={lifecycle}
        confirmationText={confirmationText}
        onConfirmationTextChange={setConfirmationText}
        pending={lifecyclePending}
        onClose={() => {
          if (!lifecyclePending) {
            setLifecycle(null);
            setConfirmationText("");
          }
        }}
        onContinue={() => setLifecycle((current) => current ? { ...current, stage: 2 } : null)}
        onConfirm={() => void finishLifecycle()}
      />
    </div>
  );
}

function AggregateMetric({ icon: Icon, label, value, detail, className }: {
  icon: typeof Activity;
  label: string;
  value: string;
  detail?: string;
  className?: string;
}) {
  return (
    <dl className={cn("flex min-h-16 items-center gap-2 border-r px-4 last:border-r-0", className)}>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0">
        <dt className="text-xs text-muted-foreground">{label}</dt>
        <dd className="text-base font-semibold tabular-nums">{value}</dd>
        {detail && <span className="block truncate text-[10px] text-muted-foreground">{detail}</span>}
      </div>
    </dl>
  );
}

function WorkerTableSkeleton() {
  return (
    <div className="space-y-px">
      {Array.from({ length: 6 }, (_, index) => (
        <div key={index} className="grid h-14 grid-cols-[14rem_7rem_12rem_6rem_6rem_1fr_2.75rem] items-center gap-3 border-b px-4">
          <div className="h-7 w-40 animate-pulse rounded bg-muted" />
          <div className="h-4 w-10 animate-pulse rounded bg-muted" />
          <div className="h-4 w-28 animate-pulse rounded bg-muted" />
          <div className="h-4 w-12 animate-pulse rounded bg-muted" />
          <div className="h-4 w-12 animate-pulse rounded bg-muted" />
          <div className="h-4 w-52 animate-pulse rounded bg-muted" />
        </div>
      ))}
    </div>
  );
}

function RecentWork({
  items,
  sourceScope,
  onOpenTask,
  empty,
}: {
  items: WorkerRecentWork[];
  sourceScope?: Pick<TaskNavigationTarget, "clusterId" | "projectId">;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  empty: string;
}) {
  if (!items.length) return <span className="text-xs text-muted-foreground">{empty}</span>;
  return (
    <div className="space-y-0.5">
      {items.slice(0, 2).map((item) => {
        const taskTarget = recentWorkTaskNavigationTarget(item, sourceScope);
        return (
          <button
            key={`${item.taskId}:${item.at}`}
            type="button"
            disabled={!onOpenTask || !taskTarget}
            onClick={() => taskTarget && onOpenTask?.(taskTarget)}
            className={cn("flex max-w-full items-center gap-1.5 text-left text-xs", onOpenTask && taskTarget && "hover:text-brand focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
          >
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{item.taskId}</span>
            <span className="truncate">{item.title}</span>
            <span className="shrink-0 text-[10px] text-muted-foreground">{timeAgo(item.at)}</span>
          </button>
        );
      })}
    </div>
  );
}

function WorkerActions({
  actor,
  aliases,
  aliasAvailable,
  pending,
  canReset,
  canDelete,
  onEditAlias,
  onReset,
  onDelete,
  labels,
}: {
  actor: string;
  aliases: WorkerAliasMap;
  aliasAvailable: boolean;
  pending: boolean;
  canReset: boolean;
  canDelete: boolean;
  onEditAlias: () => void;
  onReset: () => void;
  onDelete: () => void;
  labels: { actions: string; alias: string; reset: string; delete: string };
}) {
  const canManageLifecycle = canReset || canDelete;
  if (!aliasAvailable && !canManageLifecycle) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-xs" aria-label={labels.actions} disabled={pending}>
          <MoreHorizontal />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuLabel className="truncate font-mono text-[10px]">{formatWorkerActor(actor, aliases)}</DropdownMenuLabel>
        {aliasAvailable && <DropdownMenuItem onSelect={onEditAlias}><Pencil />{labels.alias}</DropdownMenuItem>}
        {canManageLifecycle && (
          <>
            {aliasAvailable && <DropdownMenuSeparator />}
            {canReset && <DropdownMenuItem onSelect={onReset}><RotateCcw />{labels.reset}</DropdownMenuItem>}
            {canDelete && <DropdownMenuItem variant="destructive" onSelect={onDelete}><UserRoundX />{labels.delete}</DropdownMenuItem>}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function WorkerLifecycleDialog({
  state,
  confirmationText,
  onConfirmationTextChange,
  pending,
  onClose,
  onContinue,
  onConfirm,
}: {
  state: { actor: string; kind: "reset" | "delete"; stage: 1 | 2 } | null;
  confirmationText: string;
  onConfirmationTextChange: (value: string) => void;
  pending: boolean;
  onClose: () => void;
  onContinue: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  const isDelete = state?.kind === "delete";
  const title = isDelete ? t("Delete Worker?", "删除 Worker？") : t("Reset Worker metrics?", "重置 Worker 指标？");
  const firstDescription = isDelete
    ? t(
      "This does not change task files, assignments, leases, provenance, or Work Logs. It only removes this Worker's display alias and stores a lifecycle tombstone. New activity automatically recreates the Worker.",
      "这不会改动任务文件、分配、租约、溯源记录或 Work Logs。它只会移除此 Worker 的显示别名并写入生命周期墓碑；新的活动会自动重建该 Worker。",
    )
    : t(
      "This does not change task files, assignments, leases, provenance, or Work Logs. It only starts derived Worker metrics from this moment forward.",
      "这不会改动任务文件、分配、租约、溯源记录或 Work Logs。它只会从此刻开始重新计算派生 Worker 指标。",
    );
  const secondDescription = t(
    "Type the canonical actor exactly to make this change.",
    "请准确输入 canonical actor 以确认此操作。",
  );

  return (
    <AlertDialog open={state !== null} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{state?.stage === 1 ? firstDescription : secondDescription}</AlertDialogDescription>
        </AlertDialogHeader>
        {state?.stage === 2 && (
          <label className="grid gap-1.5 text-sm">
            <span className="text-muted-foreground">{t("Canonical actor", "Canonical actor")}</span>
            <Input autoFocus value={confirmationText} onChange={(event) => onConfirmationTextChange(event.target.value)} placeholder={state.actor} />
          </label>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("Cancel", "取消")}</AlertDialogCancel>
          {state?.stage === 1 ? (
            <Button variant={isDelete ? "destructive" : "default"} onClick={onContinue}>{t("Continue", "继续")}</Button>
          ) : (
            <Button variant={isDelete ? "destructive" : "default"} disabled={pending || confirmationText !== state?.actor} onClick={onConfirm}>
              {isDelete ? t("Delete Worker", "删除 Worker") : t("Reset metrics", "重置指标")}
            </Button>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
