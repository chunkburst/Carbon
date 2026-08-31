import { useEffect, useMemo, useState } from "react";
import {
  Activity,
  BarChart3,
  ChevronRight,
  Clock3,
  ClipboardList,
  Gauge,
  History,
  ListChecks,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  RotateCcw,
  Search,
  ShieldCheck,
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
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Progress } from "@/components/ui/progress";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { WorkerContextMenu } from "@/components/WorkerContextMenu";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import { WorkerIdentityManagerDialog } from "@/components/WorkerIdentityManagerDialog";
import { priorityLabel } from "@/components/PriorityIcon";
import { recentWorkTaskNavigationTarget, type TaskNavigationTarget } from "@/components/WorkLogTypes";
import {
  useCarbonWorkerIdentities,
  useDeleteCarbonWorker,
  usePatchWorkerAlias,
  useResetCarbonWorker,
  useWorkerAliases,
  useWorkerMetrics,
} from "@/lib/queries";
import type { CarbonHomeCluster, CarbonScope, CarbonScopeInput, CarbonWorkerIdentity, CarbonWorkerMetric } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, labelTone, timeAgo } from "@/lib/utils";
import { formatWorkerActor, type WorkerAliasMap } from "@/lib/worker-aliases";
import { workerRoleLabel } from "@/lib/worker-roles";

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
 * Worker is a delivery-console directory. It reports durable task signals, not
 * presence: “recent record” means attributed task activity, never online status.
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
  const [query, setQuery] = useState("");
  const [editingActor, setEditingActor] = useState<string | null>(null);
  const [aliasDraft, setAliasDraft] = useState("");
  const [lifecycle, setLifecycle] = useState<{ actor: string; kind: "reset" | "delete"; stage: 1 | 2 } | null>(null);
  const [confirmationText, setConfirmationText] = useState("");
  const [lifecyclePending, setLifecyclePending] = useState(false);
  const [identityManagerOpen, setIdentityManagerOpen] = useState(false);
  const [identityEditorActor, setIdentityEditorActor] = useState<string>();

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
  const identityScope = useMemo<CarbonScope | undefined>(() => {
    if (!carbonBase?.home || effectiveScope !== "project" || !effectiveProjectId) return undefined;
    return { home: carbonBase.home, clusterId: effectiveClusterId || undefined, projectId: effectiveProjectId };
  }, [carbonBase, effectiveClusterId, effectiveProjectId, effectiveScope]);
  const metrics = useWorkerMetrics(metricScope, effectiveScope);
  const identitiesQuery = useCarbonWorkerIdentities(identityScope ?? "", Boolean(identityScope));
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
  const identityPayload = identitiesQuery.data?.available ? identitiesQuery.data.data : undefined;
  const identityAvailable = identityPayload !== undefined;
  const identityModeEnabled = identityPayload?.modeEnabled === true;
  const identities = useMemo(() => identityPayload?.records ?? [], [identityPayload]);
  const identitiesByActor = useMemo(() => new Map(identities.map((record) => [record.actor, record])), [identities]);

  const workers = useMemo(() => {
    const byActor = new Map<string, WorkerMetric>((payload?.workers ?? []).map((worker) => [worker.actor, worker]));
    for (const actor of Object.keys(aliases)) {
      if (!byActor.has(actor)) byActor.set(actor, { actor, active: 0, completed: 0 });
    }
    for (const [actor, record] of Object.entries(registry)) {
      if (!record.deletedAt && !byActor.has(actor)) byActor.set(actor, { actor, active: 0, completed: 0 });
    }
    for (const identity of identities) {
      if (!byActor.has(identity.actor)) byActor.set(identity.actor, { actor: identity.actor, active: 0, completed: 0 });
    }
    return [...byActor.values()].sort((left, right) =>
      formatWorkerActor(left.actor, aliases).localeCompare(formatWorkerActor(right.actor, aliases), undefined, { sensitivity: "base" }),
    );
  }, [aliases, identities, payload?.workers, registry]);
  const visibleWorkers = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return workers;
    return workers.filter((worker) => {
      const identity = identitiesByActor.get(worker.actor);
      const searchable = [
        worker.actor,
        formatWorkerActor(worker.actor, aliases),
        ...(identity?.roles ?? []),
        identity?.role,
        ...(identity?.types ?? []),
      ].filter(Boolean).join(" ").toLocaleLowerCase();
      return searchable.includes(needle);
    });
  }, [aliases, identitiesByActor, query, workers]);
  const busiestActiveLoad = useMemo(
    () => Math.max(1, ...workers.map((worker) => worker.active)),
    [workers],
  );

  const aggregate = payload?.aggregate;
  const active = aggregate?.active ?? workers.reduce<number>((sum, worker) => sum + worker.active, 0);
  const completed = aggregate?.completed ?? workers.reduce<number>((sum, worker) => sum + (worker.completed || completedCount(worker.completedByPriority ?? worker.completed_by_priority)), 0);
  const averageCycle = aggregate?.averageCycleSeconds ?? aggregate?.average_cycle_seconds;
  const reopenRate = aggregate?.reopenRate ?? aggregate?.reopen_rate;
  const taskCount = aggregate?.taskCount ?? completed + active;
  const resolvedLifecycleActions: WorkerLifecycleActions | undefined = lifecycleActions ?? (resolvedHome
    ? {
      onResetWorker: async (actor) => {
        const result = await resetWorker.mutateAsync(actor);
        if (!result.available) throw new Error("Carbon needs an update before team management is available");
        return result.data;
      },
      onDeleteWorker: async (actor) => {
        const result = await deleteWorker.mutateAsync(actor);
        if (!result.available) throw new Error("Carbon needs an update before team management is available");
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
  const openIdentityManager = (actor?: string) => {
    setIdentityEditorActor(actor);
    setIdentityManagerOpen(true);
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
      // Query mutations surface failures through the shared toast boundary. Keep the
      // second confirmation open so a human can retry after resolving the cause.
    } finally {
      setLifecyclePending(false);
    }
  };

  const scopeLabel = t(
    effectiveScope === "all" ? "All clusters" : effectiveScope === "cluster" ? "Selected cluster" : "Selected project",
    effectiveScope === "all" ? "全部集群" : effectiveScope === "cluster" ? "所选集群" : "所选项目",
  );
  const resultLabel = query.trim()
    ? t("{visible} of {total} agents", "{visible}/{total} 个智能体", { visible: visibleWorkers.length, total: workers.length })
    : t("{count} agents", "{count} 个智能体", { count: workers.length });

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <UsersRound className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="text-sm font-semibold">{t("Agent team", "智能体团队")}</h1>
            </div>
            <p className="truncate text-xs text-muted-foreground">{t("Check each Worker's current focus and recent contributions", "查看每位 Worker 当前在忙什么、最近完成了哪些工作")}</p>
          </div>
        </div>
        <div className="flex w-full min-w-0 flex-1 flex-wrap items-center justify-end gap-2 sm:w-auto sm:flex-none">
          <Button
            variant="outline"
            size="sm"
            onClick={() => openIdentityManager()}
            aria-label={t("Manage identity and responsibilities", "管理身份与分工")}
          >
            <ShieldCheck data-icon="inline-start" />
            {t("Identity and responsibilities", "身份与分工")}
            {identityScope && identityAvailable && <Badge variant="secondary" className="ml-0.5 min-w-5 justify-center px-1.5 tabular-nums">{identities.length}</Badge>}
          </Button>
          <InputGroup className="order-last min-w-44 flex-1 sm:order-none sm:w-64 sm:flex-none">
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Search agents, roles, or task types", "搜索智能体、角色或任务类型")}
              aria-label={t("Search agents", "搜索智能体")}
            />
          </InputGroup>
          {allowClusterScope && <ToggleGroup
            type="single"
            value={scope}
            variant="outline"
            size="sm"
            spacing={0}
            onValueChange={(value) => value && setScope(value as Scope)}
            aria-label={t("Agent team scope", "智能体团队范围")}
          >
            <ToggleGroupItem value="all">{t("All", "全部")}</ToggleGroupItem>
            <ToggleGroupItem value="cluster" disabled={!clusterId}>{t("Cluster", "集群")}</ToggleGroupItem>
            <ToggleGroupItem value="project" disabled={!projectId}>{t("Project", "项目")}</ToggleGroupItem>
          </ToggleGroup>}
        </div>
      </header>

      {carbonBase && clusters.length > 0 && allowClusterScope && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-muted/20 px-4 py-1.5">
          <span className="text-xs text-muted-foreground">{t("View", "查看范围")}</span>
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
            <SelectTrigger className="h-7 min-w-36 max-w-60 text-xs">{clusterId ? selectedCluster?.name ?? clusterId : t("All clusters", "全部集群")}</SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">{t("All clusters", "全部集群")}</SelectItem>
                {clusters.map((cluster) => <SelectItem key={cluster.id} value={cluster.id}>{cluster.name}</SelectItem>)}
              </SelectGroup>
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
            <SelectTrigger className="h-7 min-w-36 max-w-60 text-xs">{projectId ? selectableProjects.find((project) => project.id === projectId)?.name ?? projectId : t("All projects", "全部项目")}</SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">{t("All projects", "全部项目")}</SelectItem>
                {selectableProjects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="ml-auto text-xs text-muted-foreground">{scopeLabel}</span>
        </div>
      )}

      <div className="min-w-0 flex-1 overflow-y-auto">
        {!available && !metrics.isLoading && (
          <Alert className="m-4 mb-0">
            <AlertTitle>{t("Work statistics are not available yet", "工作统计暂不可用")}</AlertTitle>
            <AlertDescription>
              {t(
                "This view does not guess statistics from incomplete task records. Update Carbon to see team statistics for this scope.",
                "此视图不会根据不完整的任务记录猜测数据。更新 Carbon 后即可查看当前范围的团队统计。",
              )}
            </AlertDescription>
          </Alert>
        )}

        <main className="mx-auto flex w-full max-w-7xl flex-col gap-4 p-4">
          <Card size="sm">
            <CardHeader>
              <CardTitle>{t("Team overview", "团队概览")}</CardTitle>
              <CardDescription>{t("See what is moving and how the work is progressing", "看看有多少事情正在推进，整体进展如何")}</CardDescription>
              <CardAction>
                <Badge variant="outline">{scopeLabel}</Badge>
              </CardAction>
            </CardHeader>
            <CardContent>
              <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4 xl:grid-cols-5" aria-label={t("Team work statistics", "团队工作统计")}>
                <AggregateMetric icon={Gauge} label={t("In progress", "处理中")} value={available ? String(active) : "—"} />
                <AggregateMetric icon={BarChart3} label={t("Delivered", "已交付")} value={available ? String(completed) : "—"} />
                <AggregateMetric icon={Clock3} label={t("Average completion time", "平均完成用时")} value={available ? duration(averageCycle) : "—"} detail={aggregate?.cycleSamples ? t("Based on {count} completed tasks", "基于 {count} 条已完成任务", { count: aggregate.cycleSamples ?? aggregate.cycle_samples ?? 0 }) : undefined} />
                <AggregateMetric icon={RefreshCw} label={t("Returned tasks", "返工率")} value={available ? percent(reopenRate) : "—"} />
                <AggregateMetric className="col-span-2 sm:col-span-1" icon={ClipboardList} label={t("Tasks", "任务数")} value={available ? String(taskCount) : "—"} />
              </dl>
            </CardContent>
          </Card>

          <section className="min-w-0" aria-labelledby="worker-roster-heading">
            <div className="flex flex-wrap items-end justify-between gap-2 px-1">
              <div className="min-w-0">
                <h2 id="worker-roster-heading" className="text-sm font-medium">{t("Agents at a glance", "智能体一览")}</h2>
                <p className="truncate text-xs text-muted-foreground">{t("Organized from task activity so you can see each agent's recent work. This is not an online indicator.", "按任务记录整理，方便看清每个智能体最近在做什么；这里不代表在线状态。")}</p>
              </div>
              <Badge variant="outline">{resultLabel}</Badge>
            </div>

            {metrics.isLoading || aliasesQuery.isLoading ? (
              <WorkerConsoleSkeleton />
            ) : visibleWorkers.length ? (
              <div className="mt-3 grid min-w-0 grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
                {visibleWorkers.map((worker) => {
                  const record = registry[worker.actor];
                  const canonicalLifecycleActor = worker.actor !== "unassigned";
                  const pending = lifecyclePending || resolvedLifecycleActions?.pendingActor === worker.actor;
                  return (
                    <WorkerConsoleCard
                      key={worker.actor}
                      worker={worker}
                      aliases={aliases}
                      identity={identitiesByActor.get(worker.actor)}
                      identityAvailable={identityAvailable}
                      identityModeEnabled={identityModeEnabled}
                      deleted={Boolean(record?.deletedAt)}
                      busiestActiveLoad={busiestActiveLoad}
                      sourceScope={recentWorkSourceScope}
                      onOpenTask={onOpenTask}
                      onOpenWorker={openWorker}
                      pending={pending}
                      aliasAvailable={canonicalLifecycleActor && aliasAvailable}
                      canReset={canonicalLifecycleActor && Boolean(resolvedLifecycleActions?.onResetWorker)}
                      canDelete={canonicalLifecycleActor && Boolean(resolvedLifecycleActions?.onDeleteWorker)}
                      canEditIdentity={canonicalLifecycleActor && Boolean(identityScope)}
                      onEditAlias={() => openAliasEditor(worker.actor)}
                      onEditIdentity={() => openIdentityManager(worker.actor)}
                      onReset={() => startLifecycle(worker.actor, "reset")}
                      onDelete={() => startLifecycle(worker.actor, "delete")}
                    />
                  );
                })}
              </div>
            ) : (
              <WorkerEmptyState filtered={Boolean(query.trim())} />
            )}
          </section>
        </main>
      </div>

      <Dialog open={editingActor !== null} onOpenChange={(open) => !open && closeAliasEditor()}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("Agent display name", "智能体显示名称")}</DialogTitle>
            <DialogDescription>
              {t("This name is only for display. Carbon still uses the connection ID for task assignments and records.", "名称仅用于显示；Carbon 仍会使用连接标识进行任务分配和记录。")}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="gap-3">
            <Field>
              <FieldLabel>{t("Connection ID", "连接标识")}</FieldLabel>
              <p className="rounded-md border bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground">{editingActor}</p>
            </Field>
            <Field>
              <FieldLabel htmlFor="worker-alias">{t("Display name", "显示名称")}</FieldLabel>
              <Input
                id="worker-alias"
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
            </Field>
          </FieldGroup>
          <DialogFooter className="flex-wrap gap-2 sm:justify-between">
            <Button variant="destructive" disabled={!editingActor || !aliases[editingActor] || patchAlias.isPending} onClick={removeAlias}>
              <Trash2 data-icon="inline-start" />
              {t("Use connection ID as name", "恢复默认名称")}
            </Button>
            <div className="flex gap-2">
              <Button variant="outline" onClick={closeAliasEditor}>{t("Cancel", "取消")}</Button>
              <Button disabled={!aliasDraft.trim() || patchAlias.isPending} onClick={saveAlias}>{t("Save display name", "保存显示名称")}</Button>
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
      <WorkerIdentityManagerDialog
        open={identityManagerOpen}
        onOpenChange={(open) => {
          setIdentityManagerOpen(open);
          if (!open) setIdentityEditorActor(undefined);
        }}
        scope={identityScope}
        actor={identityEditorActor}
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
    <div className={cn("flex min-w-0 items-center gap-2 rounded-xl bg-muted/45 px-3 py-2.5", className)}>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0">
        <dt className="truncate text-xs text-muted-foreground">{label}</dt>
        <dd className="text-base font-semibold tabular-nums">{value}</dd>
        {detail && <span className="block truncate text-[10px] text-muted-foreground">{detail}</span>}
      </div>
    </div>
  );
}

type WorkerConsoleCardProps = {
  worker: WorkerMetric;
  aliases: WorkerAliasMap;
  identity?: CarbonWorkerIdentity;
  identityAvailable: boolean;
  identityModeEnabled: boolean;
  deleted: boolean;
  busiestActiveLoad: number;
  sourceScope?: Pick<TaskNavigationTarget, "clusterId" | "projectId">;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  onOpenWorker?: (actor: string) => void;
  pending: boolean;
  aliasAvailable: boolean;
  canReset: boolean;
  canDelete: boolean;
  canEditIdentity: boolean;
  onEditAlias: () => void;
  onEditIdentity: () => void;
  onReset: () => void;
  onDelete: () => void;
};

function WorkerConsoleCard({
  worker,
  aliases,
  identity,
  identityAvailable,
  identityModeEnabled,
  deleted,
  busiestActiveLoad,
  sourceScope,
  onOpenTask,
  onOpenWorker,
  pending,
  aliasAvailable,
  canReset,
  canDelete,
  canEditIdentity,
  onEditAlias,
  onEditIdentity,
  onReset,
  onDelete,
}: WorkerConsoleCardProps) {
  const { t } = useI18n();
  const completed = worker.completed || completedCount(worker.completedByPriority ?? worker.completed_by_priority);
  const byPriority = worker.completedByPriority ?? worker.completed_by_priority;
  const recent = worker.recentWork ?? worker.recent_work ?? [];
  const lastActivity = worker.lastActivity ?? worker.last_activity;
  const loadPercent = Math.min(100, Math.round((worker.active / busiestActiveLoad) * 100));
  const displayName = formatWorkerActor(worker.actor, aliases);
  const activitySummary = worker.active === 1
    ? t("Working on 1 task", "正在处理 1 个任务")
    : worker.active > 1
      ? t("Working on {count} tasks", "正在处理 {count} 个任务", { count: worker.active })
      : lastActivity
        ? t("Has contributed recently", "最近参与过任务")
        : t("No tasks joined yet", "还没参与过任务");
  const activeTaskLabel = worker.active === 1
    ? t("Working on 1 task", "正在做 1 个")
    : t("Working on {count} tasks", "正在做 {count} 个", { count: worker.active });

  return (
    <WorkerContextMenu
      actor={worker.actor}
      displayName={displayName}
      pending={pending}
      onOpenWorker={onOpenWorker}
      onEditAlias={aliasAvailable ? onEditAlias : undefined}
      onEditIdentity={canEditIdentity ? onEditIdentity : undefined}
      onReset={canReset ? onReset : undefined}
      onDelete={canDelete ? onDelete : undefined}
    >
      <Card size="sm" data-carbon-context-surface className="min-w-0">
        <CardHeader>
          <button
            type="button"
            className={cn("min-w-0 text-left", onOpenWorker && "rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
            disabled={!onOpenWorker}
            onClick={() => onOpenWorker?.(worker.actor)}
          >
            <WorkerIdentity actor={worker.actor} aliases={aliases} active={worker.active > 0} deleted={deleted} />
          </button>
          <CardDescription>{activitySummary}</CardDescription>
          <CardAction>
            <div className="flex items-center gap-1.5">
              {deleted && <Badge variant="destructive">{t("Removed", "已移出团队")}</Badge>}
              <WorkerActions
                actor={worker.actor}
                aliases={aliases}
                aliasAvailable={aliasAvailable}
                pending={pending}
                canReset={canReset}
                canDelete={canDelete}
                canEditIdentity={canEditIdentity}
                onEditAlias={onEditAlias}
                onEditIdentity={onEditIdentity}
                onReset={onReset}
                onDelete={onDelete}
                labels={{
                  actions: t("Agent actions", "智能体操作"),
                  alias: t("Edit display name", "编辑显示名称"),
                  identity: t("Edit identity and responsibilities", "编辑身份与分工"),
                  reset: t("Restart work statistics", "重新统计工作数据"),
                  delete: t("Remove from team", "移出智能体团队"),
                }}
              />
            </div>
          </CardAction>
        </CardHeader>

        <CardContent className="flex flex-col gap-3">
          <WorkerCapability identity={identity} identityAvailable={identityAvailable} identityModeEnabled={identityModeEnabled} />
          <Separator />

          <section className="flex flex-col gap-1.5" aria-label={t("Tasks in progress", "正在处理")}>
            <div className="flex items-center justify-between gap-2">
              <span className="flex items-center gap-1.5 text-xs font-medium"><Gauge className="size-3.5 text-muted-foreground" />{t("In progress", "正在处理")}</span>
              <Badge variant={worker.active > 0 ? "secondary" : "outline"}>{worker.active > 0 ? activeTaskLabel : t("No task in progress", "暂无处理中任务")}</Badge>
            </div>
            <Progress value={loadPercent} aria-label={worker.active > 0 ? activeTaskLabel : t("No task in progress", "暂无处理中任务")} />
            <p className="text-[10px] text-muted-foreground">{t("This compares only active tasks; it does not show whether an agent is online.", "这里只比较正在处理的任务数量，不代表智能体是否在线。")}</p>
          </section>

          <dl className="grid grid-cols-3 gap-2">
            <WorkerDatum icon={ListChecks} label={t("Delivered", "已交付")} value={String(completed)} />
            <WorkerDatum icon={Clock3} label={t("Average completion time", "平均完成用时")} value={duration(worker.averageCycleSeconds ?? worker.average_cycle_seconds)} />
            <WorkerDatum icon={RefreshCw} label={t("Returned tasks", "返工率")} value={percent(worker.reopenRate ?? worker.reopen_rate)} />
          </dl>
          <DeliveryPriorityBadges values={byPriority} />

          <Separator />
          <section className="flex flex-col gap-2" aria-label={t("Recent contributions", "最近参与")}>
            <div className="flex items-center justify-between gap-2">
              <span className="flex min-w-0 items-center gap-1.5 text-xs font-medium"><History className="size-3.5 shrink-0 text-muted-foreground" />{t("Recent contributions", "最近参与")}</span>
              <span className="shrink-0 text-[10px] text-muted-foreground">{lastActivity ? t("Last joined {when}", "上次参与 {when}", { when: timeAgo(lastActivity) }) : t("No activity yet", "暂时还没有")}</span>
            </div>
            <RecentWork items={recent} sourceScope={sourceScope} onOpenTask={onOpenTask} empty={t("Nothing new to show here yet", "最近还没有新动态")} />
          </section>
        </CardContent>
      </Card>
    </WorkerContextMenu>
  );
}

function WorkerCapability({ identity, identityAvailable, identityModeEnabled }: {
  identity?: CarbonWorkerIdentity;
  identityAvailable: boolean;
  identityModeEnabled: boolean;
}) {
  const { t } = useI18n();
  if (!identityAvailable) {
    return (
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="outline">{t("Profile details unavailable", "身份信息暂不可用")}</Badge>
        <span className="text-xs text-muted-foreground">{t("This project cannot read agent profile details right now.", "暂时无法读取此项目的智能体身份信息。")}</span>
      </div>
    );
  }
  if (!identityModeEnabled) {
    return (
      <section className="flex flex-col gap-1.5" aria-label={t("Role and task types", "角色与可接任务")}>
        <div className="flex flex-wrap items-center gap-1.5"><Badge variant="outline">{t("Free collaboration", "自由协作")}</Badge><span className="text-xs text-muted-foreground">{t("Roles are not enforced while this mode is off.", "当前不按身份限制任务认领。")}</span></div>
        {identity && <div className="flex flex-wrap items-center gap-1.5">{(identity.roles?.length ? identity.roles : [identity.role]).map((role) => <Badge key={role} variant="secondary">{workerRoleLabel(role, t)}</Badge>)}{identity.types.map((type) => <Badge key={type} variant="outline">{carbonTaskTypeLabel(type, t)}</Badge>)}</div>}
      </section>
    );
  }
  if (!identity) {
    return (
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="outline">{t("Profile not set", "身份待设置")}</Badge>
        <span className="text-xs text-muted-foreground">{t("This agent has not chosen a role or task types yet.", "这个智能体还没有选择角色或可接任务类型。")}</span>
      </div>
    );
  }
  return (
    <section className="flex flex-col gap-1.5" aria-label={t("Role and task types", "角色与可接任务")}>
      <span className="text-xs text-muted-foreground">{t("Role and task types", "角色与可接任务")}</span>
      <div className="flex flex-wrap items-center gap-1.5">
        {(identity.roles?.length ? identity.roles : [identity.role]).map((role) => <Badge key={role} variant="secondary">{workerRoleLabel(role, t)}</Badge>)}
        {identity.types.map((type) => <Badge key={type} variant="outline">{carbonTaskTypeLabel(type, t)}</Badge>)}
      </div>
    </section>
  );
}

function WorkerDatum({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-lg bg-muted/45 px-2.5 py-2">
      <dt className="flex items-center gap-1 text-[10px] text-muted-foreground"><Icon className="size-3 shrink-0" />{label}</dt>
      <dd className="mt-0.5 truncate text-sm font-semibold tabular-nums">{value}</dd>
    </div>
  );
}

function DeliveryPriorityBadges({ values }: { values?: Partial<Record<string, number>> }) {
  const { t } = useI18n();
  const entries = Object.entries(values ?? {}).filter(([, value]) => (value ?? 0) > 0).slice(0, 4);
  if (!entries.length) return null;
  return (
    <div className="flex flex-wrap items-center gap-1" aria-label={t("Completed by priority", "按优先级完成")}>
      <span className="mr-1 text-[10px] text-muted-foreground">{t("Priority", "优先级")}</span>
      {entries.map(([priority, value]) => (
        <Badge key={priority} variant="secondary" className={cn("carbon-label", labelTone(priority))}>{priorityLabel(priority)} {value}</Badge>
      ))}
    </div>
  );
}

function WorkerConsoleSkeleton() {
  return (
    <div className="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      {Array.from({ length: 6 }, (_, index) => (
        <Card key={index} size="sm">
          <CardHeader>
            <Skeleton className="h-7 w-40" />
            <Skeleton className="h-3 w-52" />
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-2 w-full" />
            <div className="grid grid-cols-3 gap-2"><Skeleton className="h-12" /><Skeleton className="h-12" /><Skeleton className="h-12" /></div>
            <Skeleton className="h-11 w-full" />
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

function WorkerEmptyState({ filtered }: { filtered: boolean }) {
  const { t } = useI18n();
  return (
    <Empty className="mt-3 min-h-64">
      <EmptyHeader>
        <EmptyMedia variant="icon"><UsersRound /></EmptyMedia>
        <EmptyTitle>{filtered ? t("No matching agents", "没有匹配的智能体") : t("No agent has worked on a task here yet", "还没有智能体参与任务")}</EmptyTitle>
        <EmptyDescription>
          {filtered
            ? t("Try an agent name, role, or task type.", "换个智能体名称、角色或任务类型试试。")
            : t("Agents will appear here after they take part in a task.", "有智能体开始参与任务后，这里会自动显示。")}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
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
    <div className="flex flex-col gap-1">
      {items.slice(0, 2).map((item) => {
        const taskTarget = recentWorkTaskNavigationTarget(item, sourceScope);
        return (
          <button
            key={`${item.taskId}:${item.at}`}
            type="button"
            disabled={!onOpenTask || !taskTarget}
            onClick={() => taskTarget && onOpenTask?.(taskTarget)}
            className={cn("flex min-w-0 items-center gap-1.5 rounded-md px-1 py-1 text-left text-xs", onOpenTask && taskTarget && "hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
          >
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{item.taskId}</span>
            <span className="truncate">{item.title}</span>
            <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">{timeAgo(item.at)}</span>
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
  canEditIdentity,
  onEditAlias,
  onEditIdentity,
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
  canEditIdentity: boolean;
  onEditAlias: () => void;
  onEditIdentity: () => void;
  onReset: () => void;
  onDelete: () => void;
  labels: { actions: string; alias: string; identity: string; reset: string; delete: string };
}) {
  const canManageLifecycle = canReset || canDelete;
  if (!aliasAvailable && !canEditIdentity && !canManageLifecycle) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-xs" aria-label={labels.actions} disabled={pending}>
          <MoreHorizontal data-icon="inline-start" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuLabel className="truncate font-mono text-[10px]">{formatWorkerActor(actor, aliases)}</DropdownMenuLabel>
        <DropdownMenuGroup>
          {aliasAvailable && <DropdownMenuItem onSelect={onEditAlias}><Pencil />{labels.alias}</DropdownMenuItem>}
          {canEditIdentity && <DropdownMenuItem onSelect={onEditIdentity}><ShieldCheck />{labels.identity}</DropdownMenuItem>}
        </DropdownMenuGroup>
        {canManageLifecycle && (
          <>
            {(aliasAvailable || canEditIdentity) && <DropdownMenuSeparator />}
            <DropdownMenuGroup>
              {canReset && <DropdownMenuItem onSelect={onReset}><RotateCcw />{labels.reset}</DropdownMenuItem>}
              {canDelete && <DropdownMenuItem variant="destructive" onSelect={onDelete}><UserRoundX />{labels.delete}</DropdownMenuItem>}
            </DropdownMenuGroup>
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
  const title = isDelete ? t("Remove this agent from the team?", "将此智能体移出团队？") : t("Restart this agent's work statistics?", "重新统计此智能体的工作数据？");
  const firstDescription = isDelete
    ? t(
      "Tasks, task leads, work logs, and history stay intact. This only removes the agent from the team view and clears its display name. If the same connection works again, it will appear again automatically.",
      "不会删除任务、负责人、工作日志或历史记录。它只会将该智能体从团队列表中移除并清除显示名称；同一连接标识再次产生工作记录时，会自动重新出现。",
    )
    : t(
      "Tasks, task leads, work logs, and history stay unchanged. Only this agent's work statistics start fresh from now on.",
      "任务、负责人、工作日志和历史记录都不会改变；只会从此刻开始重新统计该智能体的工作数据。",
    );
  const secondDescription = t("To confirm, type the connection ID exactly.", "请输入完整连接标识以确认此操作。");

  return (
    <AlertDialog open={state !== null} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{state?.stage === 1 ? firstDescription : secondDescription}</AlertDialogDescription>
        </AlertDialogHeader>
        {state?.stage === 2 && (
          <FieldGroup className="gap-3">
            <Field>
              <FieldLabel htmlFor="worker-lifecycle-confirmation">{t("Connection ID", "连接标识")}</FieldLabel>
              <Input id="worker-lifecycle-confirmation" autoFocus value={confirmationText} onChange={(event) => onConfirmationTextChange(event.target.value)} placeholder={state.actor} />
            </Field>
          </FieldGroup>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("Cancel", "取消")}</AlertDialogCancel>
          {state?.stage === 1 ? (
            <Button variant={isDelete ? "destructive" : "default"} onClick={onContinue}>{t("Continue", "继续")}</Button>
          ) : (
            <Button variant={isDelete ? "destructive" : "default"} disabled={pending || confirmationText !== state?.actor} onClick={onConfirm}>
              {isDelete ? t("Remove from team", "移出智能体团队") : t("Restart statistics", "重新统计")}
            </Button>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
