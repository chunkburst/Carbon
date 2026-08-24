import { useMemo, useState } from "react";
import { Activity, ClipboardList, Clock3, Gauge, History, ListChecks, RefreshCw, ShieldCheck, UsersRound } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { WorkLogDetailsDialog } from "@/components/WorkLogDetailsDialog";
import { WorkLogTable } from "@/components/WorkLogTable";
import { recentWorkTaskNavigationTarget, type TaskNavigationTarget, type WorkLog } from "@/components/WorkLogTypes";
import type { CarbonHomeCluster, CarbonScopeInput, CarbonWorkerIdentity, CarbonWorkerMetric, CarbonWorkerRecentWork } from "@/lib/carbon-api";
import { useCarbonWorkLogs, useCarbonWorkerIdentities, useWorkerAliases, useWorkerMetrics } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, initials, timeAgo } from "@/lib/utils";
import { formatWorkerActor } from "@/lib/worker-aliases";

export type OwnerLogsProps = {
  home?: string;
  carbonScope?: CarbonScopeInput;
  clusters?: CarbonHomeCluster[];
  /** Omit for the Work Log ledger; provide a canonical actor for profile mode. */
  actor?: string;
  onOpenWorker?: (actor: string) => void;
  onOpenTask?: (target: TaskNavigationTarget) => void;
};

/**
 * A Worker profile is an audit-focused dossier: capability, present task load,
 * attributed task records, then durable Work Logs. The no-actor route is the
 * Work Log ledger only; it deliberately does not duplicate the Worker directory.
 */
export function OwnerLogs({ home, carbonScope, actor, onOpenWorker, onOpenTask }: OwnerLogsProps) {
  const { t } = useI18n();
  const carbonBase = useMemo(
    () => (typeof carbonScope === "object" && carbonScope !== null ? carbonScope : undefined),
    [carbonScope],
  );
  const resolvedHome = home ?? carbonBase?.home;
  const statsScope = useMemo<CarbonScopeInput>(
    () => carbonScope ?? (resolvedHome ? { home: resolvedHome } : ""),
    [carbonScope, resolvedHome],
  );
  const statsMode = carbonBase?.projectId ? "project" : carbonBase?.clusterId ? "cluster" : "all";
  const recentWorkSourceScope = useMemo<Pick<TaskNavigationTarget, "clusterId" | "projectId"> | undefined>(() => {
    const clusterId = carbonBase?.clusterId?.trim();
    const projectId = carbonBase?.projectId?.trim();
    if (!clusterId && !projectId) return undefined;
    return {
      ...(clusterId ? { clusterId } : {}),
      ...(projectId ? { projectId } : {}),
    };
  }, [carbonBase?.clusterId, carbonBase?.projectId]);
  const identityScope = useMemo<CarbonScopeInput>(() => {
    if (!carbonBase) return carbonScope ?? "";
    if (carbonBase.home && carbonBase.clusterId) return { home: carbonBase.home, clusterId: carbonBase.clusterId };
    if (carbonBase.home && carbonBase.projectId) return { home: carbonBase.home, projectId: carbonBase.projectId };
    return carbonBase;
  }, [carbonBase, carbonScope]);
  const metrics = useWorkerMetrics(statsScope, statsMode);
  const identitiesQuery = useCarbonWorkerIdentities(identityScope, Boolean(identityScope));
  const aliasesQuery = useWorkerAliases(resolvedHome);
  const logScope = useMemo<CarbonScopeInput>(() => {
    if (typeof carbonScope === "string") return carbonScope;
    if (!carbonBase) return "";
    return { home: resolvedHome, clusterId: carbonBase.clusterId, projectId: carbonBase.projectId };
  }, [carbonBase, carbonScope, resolvedHome]);
  const hasLogScope = typeof logScope === "string" ? Boolean(logScope) : Boolean(logScope.home && (logScope.clusterId || logScope.projectId));
  const logsQuery = useCarbonWorkLogs(logScope, { worker: actor, limit: actor ? 100 : 24 }, hasLogScope);
  const workers = useMemo<CarbonWorkerMetric[]>(
    () => metrics.data?.available ? metrics.data.data.workers ?? [] : [],
    [metrics.data],
  );
  const metric = useMemo(() => workers.find((worker) => worker.actor === actor), [actor, workers]);
  const identity = useMemo<CarbonWorkerIdentity | undefined>(
    () => identitiesQuery.data?.available && identitiesQuery.data.data.modeEnabled
      ? identitiesQuery.data.data.records?.find((record) => record.actor === actor)
      : undefined,
    [actor, identitiesQuery.data],
  );
  const identityAvailable = identitiesQuery.data?.available === true;
  const identityModeEnabled = identitiesQuery.data?.data?.modeEnabled === true;
  const aliases = aliasesQuery.data?.available ? aliasesQuery.data.data.aliases : {};
  const displayName = actor ? formatWorkerActor(actor, aliases) : "";
  const highestActiveLoad = useMemo(() => Math.max(1, ...workers.map((worker) => worker.active)), [workers]);
  const logs = useMemo<WorkLog[]>(
    () => logsQuery.data?.available ? logsQuery.data.data.worklogs as WorkLog[] : [],
    [logsQuery.data],
  );
  const [viewLog, setViewLog] = useState<WorkLog | null>(null);

  if (actor) {
    return (
      <div className="flex h-full min-w-0 flex-col bg-panel">
        <header className="flex min-h-14 shrink-0 items-center gap-2.5 border-b px-4 py-2">
          <UsersRound className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">{t("Agent profile", "智能体档案")}</h1>
            <p className="truncate text-xs text-muted-foreground">{t("Execution agent, task activity, and work logs", "执行智能体、任务参与记录和工作日志")}</p>
          </div>
        </header>
        <div className="min-w-0 flex-1 overflow-y-auto">
          {!metrics.data?.available && !metrics.isLoading && (
            <Alert className="m-4 mb-0">
              <AlertTitle>{t("Work statistics are not available yet", "工作统计暂不可用")}</AlertTitle>
              <AlertDescription>{t("Carbon does not guess profile statistics from incomplete task records.", "Carbon 不会根据不完整的任务记录猜测档案数据。")}</AlertDescription>
            </Alert>
          )}

          <main className="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4">
            <WorkerProfileSummary
              actor={actor}
              displayName={displayName}
              metric={metric}
              identity={identity}
              identityAvailable={identityAvailable}
              identityModeEnabled={identityModeEnabled}
              highestActiveLoad={highestActiveLoad}
            />

            <Tabs defaultValue="work">
                <TabsList aria-label={t("Agent profile sections", "智能体档案分区")}>
                <TabsTrigger value="work"><ClipboardList />{t("Task work", "任务工作")}</TabsTrigger>
                <TabsTrigger value="logs"><History />{t("Work logs", "工作日志")}</TabsTrigger>
              </TabsList>

              <TabsContent value="work" className="mt-3">
                <Card size="sm">
                  <CardHeader>
                    <CardTitle>{t("Recent task activity", "最近参与的任务")}</CardTitle>
                    <CardDescription>{t("Tasks this agent recently contributed to. This is not an online indicator.", "这里展示该智能体最近参与的任务，不代表当前在线状态。")}</CardDescription>
                    <CardAction><Badge variant="outline">{metric?.recentWork?.length ?? metric?.recent_work?.length ?? 0}</Badge></CardAction>
                  </CardHeader>
                  <CardContent>
                    {metrics.isLoading ? <RecentWorkSkeleton /> : <RecentWorkTimeline items={metric?.recentWork ?? metric?.recent_work ?? []} sourceScope={recentWorkSourceScope} onOpenTask={onOpenTask} />}
                  </CardContent>
                </Card>
              </TabsContent>

              <TabsContent value="logs" className="mt-3">
                <WorkLogsPanel
                  logs={logs}
                  loading={logsQuery.isLoading}
                  available={logsQuery.data?.available === true}
                  hasLogScope={hasLogScope}
                  onOpenWorker={onOpenWorker}
                  onOpenTask={onOpenTask}
                  onView={setViewLog}
                  compact
                />
              </TabsContent>
            </Tabs>
          </main>
        </div>
        <WorkLogDetailsDialog open={viewLog !== null} onOpenChange={(open) => !open && setViewLog(null)} log={viewLog} onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} />
      </div>
    );
  }

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-14 shrink-0 items-center gap-2.5 border-b px-4 py-2">
        <History className="size-4 shrink-0 text-brand" />
        <div className="min-w-0">
          <h1 className="text-sm font-semibold">{t("Work log history", "工作日志")}</h1>
          <p className="truncate text-xs text-muted-foreground">{t("Review team updates or open the related agent profile", "查看团队工作记录，或从记录打开对应的智能体档案")}</p>
        </div>
      </header>
      <div className="min-w-0 flex-1 overflow-y-auto">
        <main className="mx-auto w-full max-w-6xl p-4">
          <WorkLogsPanel
            logs={logs}
            loading={logsQuery.isLoading}
            available={logsQuery.data?.available === true}
            hasLogScope={hasLogScope}
            onOpenWorker={onOpenWorker}
            onOpenTask={onOpenTask}
            onView={setViewLog}
          />
        </main>
      </div>
      <WorkLogDetailsDialog open={viewLog !== null} onOpenChange={(open) => !open && setViewLog(null)} log={viewLog} onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} />
    </div>
  );
}

function WorkerProfileSummary({
  actor,
  displayName,
  metric,
  identity,
  identityAvailable,
  identityModeEnabled,
  highestActiveLoad,
}: {
  actor: string;
  displayName: string;
  metric?: CarbonWorkerMetric;
  identity?: CarbonWorkerIdentity;
  identityAvailable: boolean;
  identityModeEnabled: boolean;
  highestActiveLoad: number;
}) {
  const { t } = useI18n();
  const completed = metric?.completed ?? completedCount(metric?.completedByPriority ?? metric?.completed_by_priority);
  const cycle = metric?.averageCycleSeconds ?? metric?.average_cycle_seconds;
  const reopen = metric?.reopenRate ?? metric?.reopen_rate;
  const active = metric?.active ?? 0;
  const loadPercent = Math.min(100, Math.round((active / highestActiveLoad) * 100));

  return (
    <Card>
      <CardHeader>
        <div className="flex min-w-0 items-center gap-3">
          <Avatar size="lg">
            <AvatarFallback>{initials(displayName || actor)}</AvatarFallback>
          </Avatar>
          <div className="min-w-0">
            <CardTitle className="truncate">{displayName || actor}</CardTitle>
            <CardDescription className="truncate font-mono text-[10px]">{actor}</CardDescription>
          </div>
        </div>
        <CardDescription>{t("This is an execution agent profile. Each task shows its own task lead.", "这是执行智能体档案；每个任务会单独显示负责人。")}</CardDescription>
        <CardAction><IdentityStatus identity={identity} identityAvailable={identityAvailable} identityModeEnabled={identityModeEnabled} /></CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <section className="grid gap-4 lg:grid-cols-[minmax(0,1.15fr)_minmax(15rem,0.85fr)]" aria-label={t("Agent role and workload", "智能体角色与工作负载")}>
          <WorkerCapability identity={identity} identityAvailable={identityAvailable} identityModeEnabled={identityModeEnabled} />
          <div className="flex flex-col gap-2 rounded-xl bg-muted/45 p-3">
            <div className="flex items-center justify-between gap-2">
              <span className="flex items-center gap-1.5 text-xs font-medium"><Gauge className="size-3.5 text-muted-foreground" />{t("Current task load", "当前任务负载")}</span>
              <Badge variant={active > 0 ? "secondary" : "outline"}>{t("{count} active", "{count} 个活跃", { count: active })}</Badge>
            </div>
            <Progress value={loadPercent} aria-label={t("{count} active tasks", "{count} 个活跃任务", { count: active })} />
            <p className="text-[10px] text-muted-foreground">{t("Compared with the busiest agent in this range; this is not an online or availability signal.", "与当前范围内最忙的智能体相比；不代表在线或可用状态。")}</p>
          </div>
        </section>

        <Separator />
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <ProfileMetric icon={Activity} label={t("Active tasks", "活跃任务")} value={String(active)} />
          <ProfileMetric icon={ListChecks} label={t("Completed", "已完成")} value={String(completed)} />
          <ProfileMetric icon={Clock3} label={t("Average completion time", "平均完成用时")} value={formatDuration(cycle)} />
          <ProfileMetric icon={RefreshCw} label={t("Returned tasks", "返工率")} value={formatPercent(reopen)} />
        </dl>
      </CardContent>
    </Card>
  );
}

function IdentityStatus({ identity, identityAvailable, identityModeEnabled }: {
  identity?: CarbonWorkerIdentity;
  identityAvailable: boolean;
  identityModeEnabled: boolean;
}) {
  const { t } = useI18n();
  if (!identityAvailable) return <Badge variant="outline">{t("Profile details unavailable", "身份信息暂不可用")}</Badge>;
  if (!identityModeEnabled) return <Badge variant="outline">{t("Identity mode off", "身份模式未启用")}</Badge>;
  if (!identity) return <Badge variant="outline">{t("Profile not set", "档案待设置")}</Badge>;
  return <Badge variant="secondary">{identity.role}</Badge>;
}

function WorkerCapability({ identity, identityAvailable, identityModeEnabled }: {
  identity?: CarbonWorkerIdentity;
  identityAvailable: boolean;
  identityModeEnabled: boolean;
}) {
  const { t } = useI18n();
  if (!identityAvailable) {
    return (
      <div className="flex flex-col gap-1.5">
        <span className="flex items-center gap-1.5 text-xs font-medium"><ShieldCheck className="size-3.5 text-muted-foreground" />{t("Role and task types", "角色与可接任务")}</span>
        <p className="text-sm text-muted-foreground">{t("This Carbon installation does not provide agent profile details yet.", "当前 Carbon 服务暂未提供智能体身份详情。")}</p>
      </div>
    );
  }
  if (!identity || !identityModeEnabled) {
    return (
      <div className="flex flex-col gap-1.5">
        <span className="flex items-center gap-1.5 text-xs font-medium"><ShieldCheck className="size-3.5 text-muted-foreground" />{t("Role and task types", "角色与可接任务")}</span>
        <p className="text-sm text-muted-foreground">{identityModeEnabled ? t("This agent has not set a role or task types yet.", "此智能体尚未设置角色或可接任务类型。") : t("Role mode is off, so this project does not limit which task types an agent can take.", "身份模式未启用，因此项目不会限制智能体可接手的任务类型。")}</p>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <span className="flex items-center gap-1.5 text-xs font-medium"><ShieldCheck className="size-3.5 text-muted-foreground" />{t("Role and task types", "角色与可接任务")}</span>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="secondary">{identity.role}</Badge>
        {identity.types.map((type) => <Badge key={type} variant="outline">{carbonTaskTypeLabel(type, t)}</Badge>)}
      </div>
      <p className="text-[10px] text-muted-foreground">{t("When role mode is on, these task types are checked when work is taken over or handed off.", "启用身份模式后，接手或转交任务时会检查这些任务类型。")}</p>
    </div>
  );
}

function ProfileMetric({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-xl bg-muted/45 px-3 py-2.5">
      <dt className="flex items-center gap-1 text-xs text-muted-foreground"><Icon className="size-3.5 shrink-0" />{label}</dt>
      <dd className="mt-0.5 truncate text-base font-semibold tabular-nums">{value}</dd>
    </div>
  );
}

function RecentWorkTimeline({
  items,
  sourceScope,
  onOpenTask,
}: {
  items: CarbonWorkerRecentWork[];
  sourceScope?: Pick<TaskNavigationTarget, "clusterId" | "projectId">;
  onOpenTask?: (target: TaskNavigationTarget) => void;
}) {
  const { t } = useI18n();
  if (!items.length) {
    return (
      <Empty className="min-h-48 border-0 p-6">
        <EmptyHeader>
          <EmptyMedia variant="icon"><ClipboardList /></EmptyMedia>
          <EmptyTitle>{t("No task activity yet", "暂时没有任务动态")}</EmptyTitle>
          <EmptyDescription>{t("Task updates involving this agent will appear here.", "与此智能体相关的任务更新会显示在这里。")}</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }
  return (
    <ol className="flex flex-col gap-1">
      {items.slice(0, 16).map((item) => {
        const taskTarget = recentWorkTaskNavigationTarget(item, sourceScope);
        return (
          <li key={`${item.taskId}:${item.at}`}>
            <button
              type="button"
              disabled={!onOpenTask || !taskTarget}
              onClick={() => taskTarget && onOpenTask?.(taskTarget)}
              className={cn("grid w-full grid-cols-[minmax(4.5rem,auto)_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2 text-left", onOpenTask && taskTarget && "hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
            >
              <span className="truncate font-mono text-[10px] text-muted-foreground">{item.taskId}</span>
              <span className="min-w-0"><span className="block truncate text-sm font-medium">{item.title}</span><span className="block truncate text-xs text-muted-foreground">{item.activity || item.status}</span></span>
              <time className="text-right text-xs text-muted-foreground">{timeAgo(item.at)}</time>
            </button>
          </li>
        );
      })}
    </ol>
  );
}

function WorkLogsPanel({
  logs,
  loading,
  available,
  hasLogScope,
  onOpenWorker,
  onOpenTask,
  onView,
  compact = false,
}: {
  logs: WorkLog[];
  loading: boolean;
  available: boolean;
  hasLogScope: boolean;
  onOpenWorker?: (actor: string) => void;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  onView: (log: WorkLog) => void;
  compact?: boolean;
}) {
  const { t } = useI18n();
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>{t("Work logs", "工作日志")}</CardTitle>
        <CardDescription>{t("Saved notes about progress, decisions, and handoffs", "记录进展、决策和交接的工作笔记")}</CardDescription>
        <CardAction><Badge variant="outline">{logs.length}</Badge></CardAction>
      </CardHeader>
      <CardContent>
        {!hasLogScope ? (
          <LedgerEmptyState title={t("Choose a project", "请选择项目")} detail={t("Work logs are available for a selected project or project group.", "选择项目或项目集群后即可查看工作日志。")}/>
        ) : loading ? <WorkLogSkeleton /> : available ? (
          logs.length ? <div className="overflow-auto"><WorkLogTable logs={logs} compact={compact} onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} onView={onView} /></div> : <LedgerEmptyState title={t("No work logs yet", "暂时没有工作日志")} detail={t("New notes will appear here as agents record their work.", "智能体写下新的工作记录后，会显示在这里。")}/>
        ) : (
          <LedgerEmptyState title={t("Work logs unavailable", "工作日志暂不可用")} detail={t("This Carbon installation does not provide work logs for the selected project.", "当前 Carbon 服务未向所选项目提供工作日志。")}/>
        )}
      </CardContent>
    </Card>
  );
}

function LedgerEmptyState({ title, detail }: { title: string; detail: string }) {
  return (
    <Empty className="min-h-52 border-0 p-6">
      <EmptyHeader>
        <EmptyMedia variant="icon"><History /></EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{detail}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  );
}

function RecentWorkSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-14 w-full" />)}
    </div>
  );
}

function WorkLogSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-14 w-full" />)}
    </div>
  );
}

function completedCount(values?: Partial<Record<string, number>>): number {
  return Object.values(values ?? {}).reduce<number>((sum, value) => sum + (value ?? 0), 0);
}

function formatDuration(value?: number): string {
  if (value === undefined || value === null || value < 0) return "—";
  const minutes = Math.round(value / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(value / 3600);
  if (hours < 24) return `${hours}h`;
  return `${(hours / 24).toFixed(hours % 24 ? 1 : 0)}d`;
}

function formatPercent(value?: number): string {
  if (value === undefined || value === null) return "—";
  return `${Math.round(value * (value <= 1 ? 100 : 1))}%`;
}
