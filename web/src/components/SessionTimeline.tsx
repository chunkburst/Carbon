import { Bot, GitBranch, Timer } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { SessionStatus } from "@/components/SessionStatus";
import { cn, timeAgo } from "@/lib/utils";
import type { AgentSession, ExecutionState } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

export function SessionTimeline({
  sessions,
  executionState,
  loading,
}: {
  sessions: AgentSession[];
  executionState?: ExecutionState;
  loading: boolean;
}) {
  const { t } = useI18n();
  if (loading) {
    return (
      <section className="mt-8 space-y-2">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-28 w-full" />
      </section>
    );
  }
  if (sessions.length === 0) return null;

  return (
    <section className="mt-8">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium text-muted-foreground">{t("Agent sessions", "智能体会话")}</h2>
        <SessionStatus state={executionState} />
      </div>
      <div className="mt-3 space-y-2">
        {sessions.map((session) => (
          <SessionCard key={session.id} session={session} />
        ))}
      </div>
    </section>
  );
}
function SessionCard({ session }: { session: AgentSession }) {
  const isLive = session.health === "active";
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();

  const heartbeat = session.live?.heartbeatAt;
  const detail = session.live?.progress || session.summary || session.cancelReason;

  return (
    <article className="rounded-lg border bg-background px-3.5 py-3">
      <div className="flex min-w-0 items-start gap-2.5">
        <span className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
          <Bot className="size-3.5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-sm font-medium">{formatWorker(session.actor)}</span>
            <HealthBadge health={session.health} />
            {/* For live sessions show heartbeat age; ended sessions show end/start time. */}
            <span className="ml-auto text-xs text-muted-foreground tabular-nums">
              {timeAgo(heartbeat ?? session.endedAt ?? session.startedAt)}
            </span>
          </div>
          {(session.client || session.model) && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">
              {[session.client, session.model].filter(Boolean).join(" · ")}
            </p>
          )}
        </div>
      </div>

      {detail && (
        <p className="mt-2.5 whitespace-pre-wrap rounded-md bg-muted/60 px-2.5 py-2 text-sm leading-relaxed">
          {detail}
        </p>
      )}

      <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {/* Branch/worktree */}
        {(session.branch || session.live?.worktree) && (
          <span className="flex min-w-0 items-center gap-1">
            <GitBranch className="size-3" />
            <span className="max-w-52 truncate">{session.branch || session.live?.worktree}</span>
          </span>
        )}
        {/* Heartbeat — only show the detail row for live sessions where the timestamp
            in the header might not be obvious enough. */}
        {isLive && heartbeat && (
          <span className="flex items-center gap-1">
            <Timer className="size-3" /> {t("Heartbeat {time}", "心跳 {time}", { time: timeAgo(heartbeat) })}
          </span>
        )}
      </div>
    </article>
  );
}

function HealthBadge({ health }: { health: AgentSession["health"] }) {
  const { t } = useI18n();
  const label = {
    active: t("active", "活跃"),
    stalled: t("stalled", "已停滞"),
    finished: t("finished", "已完成"),
    canceled: t("canceled", "已取消"),
  }[health];
  return (
    <Badge
      variant={health === "canceled" || health === "stalled" ? "destructive" : "outline"}
      className={cn(
        "h-4 px-1.5 text-[10px] font-normal",
        health === "active" && "text-brand",
        health === "finished" && "text-success",
      )}
    >
      {label}
    </Badge>
  );
}
