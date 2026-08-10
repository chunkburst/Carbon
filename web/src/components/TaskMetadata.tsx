import { AlertTriangle, LockKeyhole, UserRoundCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn, labelTone, timeAgo } from "@/lib/utils";
import type { Task } from "@/lib/api";
import { useI18n, type Translate } from "@/lib/i18n";
import { carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

function leaseLabel(task: Task, t: Translate): string | null {
  const lease = task.lease;
  if (!lease) return null;
  if (lease.expiresAt) {
    const expires = new Date(lease.expiresAt).getTime();
    if (!Number.isNaN(expires) && expires < Date.now()) return t("Lease expired", "租约已过期");
    const relative = timeAgo(lease.expiresAt);
    return relative ? t("Lease · {time}", "租约 · {time}", { time: relative }) : t("Lease active", "租约有效");
  }
  return t("Lease active", "租约有效");
}

export function TaskMetadata({ task, compact = false, className }: { task: Task; compact?: boolean; className?: string }) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  const lease = leaseLabel(task, t);
  const conflict = task.conflict;
  const pendingClaims = task.pendingClaims?.length ?? 0;
  const labels = task.labels ?? [];
  const hasMeta = task.projectId || task.type || task.importance || labels.length || lease || conflict || pendingClaims;
  if (!hasMeta) return null;

  return (
    <div className={cn("flex min-w-0 flex-wrap items-center gap-1", className)}>
      {task.projectId && (
        <Badge variant="outline" className="h-5 max-w-full gap-1 px-1.5 text-[10px] font-medium">
          <span className="truncate">{task.projectId}</span>
        </Badge>
      )}
      {task.type && !compact && (
        <Badge variant="secondary" className={cn("carbon-label h-5 px-1.5 text-[10px] font-medium", labelTone(task.type))}>
          {carbonTaskTypeLabel(task.type, t)}
        </Badge>
      )}
      {task.importance && !compact && (
        <Badge variant="secondary" className="h-5 px-1.5 text-[10px] font-medium text-foreground">
          {carbonImportanceLabel(task.importance, t)}
        </Badge>
      )}
      {labels.slice(0, compact ? 2 : 4).map((label) => (
        <Badge key={label} variant="secondary" className={cn("carbon-label h-5 max-w-28 px-1.5 text-[10px] font-medium", labelTone(label))}>
          <span className="truncate">{label}</span>
        </Badge>
      ))}
      {labels.length > (compact ? 2 : 4) && <Badge variant="outline" className="h-5 px-1.5 text-[10px]">+{labels.length - (compact ? 2 : 4)}</Badge>}
      {lease && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge variant="outline" className="h-5 gap-1 px-1.5 text-[10px] font-medium text-info">
              <LockKeyhole />
              {!compact && <span>{lease}</span>}
            </Badge>
          </TooltipTrigger>
          <TooltipContent>
            {task.lease?.holder ? `${lease} · ${formatWorker(task.lease.holder)}` : lease}
          </TooltipContent>
        </Tooltip>
      )}
      {conflict && conflict.state !== "resolved" && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge variant="destructive" className="h-5 gap-1 px-1.5 text-[10px] font-medium">
              <AlertTriangle />
              {!compact && <span>{t("Conflict", "冲突")}</span>}
            </Badge>
          </TooltipTrigger>
          <TooltipContent>{conflict.message || t("Task needs conflict resolution", "任务需要解决冲突")}</TooltipContent>
        </Tooltip>
      )}
      {pendingClaims > 0 && (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge variant="outline" className="h-5 gap-1 px-1.5 text-[10px] font-medium text-warning">
              <UserRoundCheck />
              {!compact && <span>{t(pendingClaims === 1 ? "{count} claim" : "{count} claims", "{count} 个认领申请", { count: pendingClaims })}</span>}
            </Badge>
          </TooltipTrigger>
          <TooltipContent>{t(pendingClaims === 1 ? "{count} pending assignment claim" : "{count} pending assignment claims", "{count} 个待审批的负责人申请", { count: pendingClaims })}</TooltipContent>
        </Tooltip>
      )}
    </div>
  );
}
