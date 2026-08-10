import { useMemo, useState } from "react";
import { Activity, Clock3, ListChecks, RefreshCw, UsersRound } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { WorkLogDetailsDialog } from "@/components/WorkLogDetailsDialog";
import { WorkLogTable } from "@/components/WorkLogTable";
import { recentWorkTaskNavigationTarget, type TaskNavigationTarget, type WorkLog } from "@/components/WorkLogTypes";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import { WorkerContextMenu } from "@/components/WorkerContextMenu";
import type { CarbonHomeCluster, CarbonScopeInput, CarbonWorkerMetric, CarbonWorkerRecentWork } from "@/lib/carbon-api";
import { useCarbonWorkLogs, useWorkerMetrics } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { cn, timeAgo } from "@/lib/utils";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

export type OwnerLogsProps = {
  home?: string;
  carbonScope?: CarbonScopeInput;
  clusters?: CarbonHomeCluster[];
  /** Omit for the Worker log directory; provide a canonical actor for profile mode. */
  actor?: string;
  onOpenWorker?: (actor: string) => void;
  onOpenTask?: (target: TaskNavigationTarget) => void;
};

/**
 * Route-friendly Worker log directory and profile view. It uses the same compact
 * table/timeline vocabulary as Carbon rather than a dashboard card wall.
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
  const metrics = useWorkerMetrics(statsScope, statsMode);
  // An independent project is a valid Work Log anchor; retain its project ID
  // instead of inventing a cluster for the read-only Worker profile.
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
  const logs = useMemo<WorkLog[]>(
    () => logsQuery.data?.available ? logsQuery.data.data.worklogs as WorkLog[] : [],
    [logsQuery.data],
  );
  const [viewLog, setViewLog] = useState<WorkLog | null>(null);

  if (actor) {
    return (
      <div className="flex h-full min-w-0 flex-col bg-panel">
        <header className="flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2">
          <div className="flex min-w-0 items-center gap-2.5">
            <UsersRound className="size-4 shrink-0 text-brand" />
            <div className="min-w-0">
              <h1 className="text-sm font-semibold">{t("Worker profile", "Worker 档案")}</h1>
              <WorkerIdentity actor={actor} active={(metric?.active ?? 0) > 0} />
            </div>
          </div>
          <span className="font-mono text-[10px] text-muted-foreground">{actor}</span>
        </header>
        <div className="min-w-0 flex-1 overflow-y-auto">
          {!metrics.data?.available && !metrics.isLoading && (
            <Alert className="m-4 mb-0"><AlertTitle>{t("Worker metrics need Carbon stable v2", "Worker 指标需要 Carbon stable v2")}</AlertTitle><AlertDescription>{t("Carbon does not infer profile metrics from incomplete legacy task records.", "Carbon 不会从不完整的旧任务记录推测档案指标。")}</AlertDescription></Alert>
          )}
          <WorkerMetricStrip worker={metric} />
          <section className="border-b">
            <SectionHeading title={t("Recent work", "最近工作")} detail={t("Latest task activity attributed to this Worker", "归属此 Worker 的最新任务活动")} />
            <RecentWorkTimeline items={metric?.recentWork ?? metric?.recent_work ?? []} sourceScope={recentWorkSourceScope} onOpenTask={onOpenTask} />
          </section>
          <section className="min-w-0">
            <SectionHeading title="Work Logs" detail={t("Operational notes and complete audit fields", "运营记录与完整审计字段")} count={logs.length} />
            {!hasLogScope ? (
              <p className="px-4 py-8 text-sm text-muted-foreground">{t("Choose a cluster before reading Work Logs.", "请选择集群后再读取 Work Logs。")}</p>
            ) : logsQuery.isLoading ? <LogSkeleton /> : logsQuery.data?.available ? (
              <div className="overflow-auto"><WorkLogTable logs={logs} compact onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} onView={setViewLog} /></div>
            ) : (
              <p className="px-4 py-8 text-sm text-muted-foreground">{t("This Carbon sidecar does not expose Work Logs.", "当前 Carbon sidecar 未提供 Work Logs。")}</p>
            )}
          </section>
        </div>
        <WorkLogDetailsDialog open={viewLog !== null} onOpenChange={(open) => !open && setViewLog(null)} log={viewLog} onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} />
      </div>
    );
  }

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-12 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <UsersRound className="size-4 shrink-0 text-brand" />
          <div className="min-w-0"><h1 className="text-sm font-semibold">{t("Worker log directory", "Worker 日志目录")}</h1><p className="truncate text-xs text-muted-foreground">{t("Open a Worker to inspect delivery metrics and Work Logs", "打开一个 Worker 查看交付指标和 Work Logs")}</p></div>
        </div>
      </header>
      <div className="min-w-0 flex-1 overflow-y-auto">
        {!metrics.data?.available && !metrics.isLoading && <Alert className="m-4 mb-0"><AlertTitle>{t("Worker metrics need Carbon stable v2", "Worker 指标需要 Carbon stable v2")}</AlertTitle><AlertDescription>{t("The directory remains intentionally empty rather than inventing delivery statistics.", "目录会保持为空，而不会虚构交付统计数据。")}</AlertDescription></Alert>}
        <section className="min-w-0 border-b">
          <SectionHeading title="Worker" detail={t("Select a row to open the Worker profile", "选择一行以打开 Worker 档案")} count={workers.length} />
          {metrics.isLoading ? <DirectorySkeleton /> : workers.length ? <WorkerDirectoryTable workers={workers} onOpenWorker={onOpenWorker} /> : metrics.data?.available ? <p className="px-4 py-10 text-sm text-muted-foreground">{t("No Worker activity in this scope yet.", "此范围内暂无 Worker 活动。")}</p> : null}
        </section>
        <section className="min-w-0">
          <SectionHeading title={t("Recent Work Logs", "最近 Work Logs")} detail={t("Read-only preview; each entry exposes all audit attributes", "只读预览；每条记录均可查看全部审计属性")} count={logs.length} />
          {!hasLogScope ? <p className="px-4 py-8 text-sm text-muted-foreground">{t("Choose a cluster before reading Work Logs.", "请选择集群后再读取 Work Logs。")}</p> : logsQuery.isLoading ? <LogSkeleton /> : logsQuery.data?.available ? <div className="overflow-auto"><WorkLogTable logs={logs} onOpenWorker={onOpenWorker} onOpenTask={onOpenTask} onView={setViewLog} /></div> : <p className="px-4 py-8 text-sm text-muted-foreground">{t("This Carbon sidecar does not expose Work Logs.", "当前 Carbon sidecar 未提供 Work Logs。")}</p>}
        </section>
      </div>
      <WorkLogDetailsDialog open={viewLog !== null} onOpenChange={(open) => !open && setViewLog(null)} log={viewLog} onOpenTask={onOpenTask} onOpenWorker={onOpenWorker} />
    </div>
  );
}

function SectionHeading({ title, detail, count }: { title: string; detail: string; count?: number }) {
  return <div className="flex min-h-10 items-center justify-between gap-3 border-b px-4 py-2"><div className="min-w-0"><h2 className="text-sm font-medium">{title}{count !== undefined && <span className="ml-1.5 text-xs font-normal text-muted-foreground">{count}</span>}</h2><p className="truncate text-xs text-muted-foreground">{detail}</p></div></div>;
}

function WorkerMetricStrip({ worker }: { worker?: CarbonWorkerMetric }) {
  const { t } = useI18n();
  const completed = worker?.completed ?? Object.values(worker?.completedByPriority ?? worker?.completed_by_priority ?? {}).reduce<number>((sum, value) => sum + (value ?? 0), 0);
  const cycle = worker?.averageCycleSeconds ?? worker?.average_cycle_seconds;
  const reopen = worker?.reopenRate ?? worker?.reopen_rate;
  return (
    <dl className="grid grid-cols-2 border-b sm:grid-cols-4">
      <Metric icon={Activity} label={t("Active", "活跃")} value={String(worker?.active ?? 0)} />
      <Metric icon={ListChecks} label={t("Completed", "已完成")} value={String(completed)} />
      <Metric icon={Clock3} label={t("Average cycle", "平均周期")} value={formatDuration(cycle)} />
      <Metric icon={RefreshCw} label={t("Reopen rate", "重开率")} value={formatPercent(reopen)} />
    </dl>
  );
}

function Metric({ icon: Icon, label, value }: { icon: typeof Activity; label: string; value: string }) {
  return <div className="flex min-h-16 items-center gap-2 border-r px-4 last:border-r-0"><Icon className="size-4 text-muted-foreground" /><div><dt className="text-xs text-muted-foreground">{label}</dt><dd className="text-base font-semibold tabular-nums">{value}</dd></div></div>;
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
  if (!items.length) return <p className="px-4 py-8 text-sm text-muted-foreground">{t("No recent task activity.", "暂无最近任务活动。")}</p>;
  return (
    <ol className="divide-y">
      {items.slice(0, 12).map((item) => {
        const taskTarget = recentWorkTaskNavigationTarget(item, sourceScope);
        return (
          <li key={`${item.taskId}:${item.at}`} className="grid grid-cols-[5.5rem_minmax(0,1fr)_6rem] items-center gap-3 px-4 py-2 text-sm">
            <button
              type="button"
              disabled={!onOpenTask || !taskTarget}
              onClick={() => taskTarget && onOpenTask?.(taskTarget)}
              className={cn("truncate text-left font-mono text-xs", onOpenTask && taskTarget && "text-brand hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
            >
              {item.taskId}
            </button>
            <span className="min-w-0"><span className="block truncate font-medium">{item.title}</span><span className="block truncate text-xs text-muted-foreground">{item.activity || item.status}</span></span>
            <time className="text-right text-xs text-muted-foreground">{timeAgo(item.at)}</time>
          </li>
        );
      })}
    </ol>
  );
}

function WorkerDirectoryTable({ workers, onOpenWorker }: { workers: CarbonWorkerMetric[]; onOpenWorker?: (actor: string) => void }) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  return (
    <table className="w-full min-w-[660px] text-[13px]">
      <thead className="border-b text-left text-xs text-muted-foreground"><tr><th className="h-8 px-4 font-medium">Worker</th><th className="h-8 w-24 font-medium">{t("Active", "活跃")}</th><th className="h-8 w-28 font-medium">{t("Completed", "已完成")}</th><th className="h-8 w-28 font-medium">{t("Cycle", "周期")}</th><th className="h-8 w-32 font-medium">{t("Last activity", "最后活动")}</th></tr></thead>
      <tbody>
        {workers.map((worker) => (
          <WorkerContextMenu key={worker.actor} actor={worker.actor} displayName={formatWorker(worker.actor)} onOpenWorker={onOpenWorker}>
            <tr
              tabIndex={0}
              data-carbon-context-surface
              aria-label={t("Worker {actor}", "Worker {actor}", { actor: worker.actor })}
              className="border-b transition-colors hover:bg-muted/30"
            >
              <td className="px-4 py-2"><button type="button" disabled={!onOpenWorker} onClick={() => onOpenWorker?.(worker.actor)} className={cn("block min-w-0 text-left", onOpenWorker && "rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}><WorkerIdentity actor={worker.actor} active={worker.active > 0} /></button></td>
              <td className={cn("py-2 font-medium", worker.active > 0 && "text-success")}>{worker.active}</td>
              <td className="py-2">{worker.completed || Object.values(worker.completedByPriority ?? worker.completed_by_priority ?? {}).reduce<number>((sum, value) => sum + (value ?? 0), 0)}</td>
              <td className="py-2 font-mono text-xs">{formatDuration(worker.averageCycleSeconds ?? worker.average_cycle_seconds)}</td>
              <td className="py-2 text-xs text-muted-foreground">{timeAgo(worker.lastActivity ?? worker.last_activity ?? "") || "—"}</td>
            </tr>
          </WorkerContextMenu>
        ))}
      </tbody>
    </table>
  );
}

function DirectorySkeleton() { return <div className="space-y-px">{Array.from({ length: 6 }, (_, index) => <div key={index} className="grid h-14 grid-cols-[1fr_6rem_7rem_7rem_8rem] gap-3 border-b px-4"><span className="my-auto h-5 w-36 animate-pulse rounded bg-muted" /><span className="my-auto h-4 w-9 animate-pulse rounded bg-muted" /><span className="my-auto h-4 w-10 animate-pulse rounded bg-muted" /><span className="my-auto h-4 w-12 animate-pulse rounded bg-muted" /></div>)}</div>; }
function LogSkeleton() { return <div className="space-y-px">{Array.from({ length: 4 }, (_, index) => <div key={index} className="grid h-14 grid-cols-[6rem_12rem_minmax(16rem,1fr)_8rem] gap-3 border-b px-4"><span className="my-auto h-4 w-12 animate-pulse rounded bg-muted" /><span className="my-auto h-5 w-28 animate-pulse rounded bg-muted" /><span className="my-auto h-4 w-48 animate-pulse rounded bg-muted" /><span className="my-auto h-5 w-20 animate-pulse rounded bg-muted" /></div>)}</div>; }

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
