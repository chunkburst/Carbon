import { ClockAlert, CircleHelp } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import type { Task } from "@/lib/api";
import { cn, statusLabel, timeAgo } from "@/lib/utils";

function durationLabel(seconds: number | undefined, t: ReturnType<typeof useI18n>["t"]): string {
  if (!seconds || seconds <= 0) return t("the configured period", "项目设定周期");
  if (seconds % 86_400 === 0) return t("{count} days", "{count} 天", { count: seconds / 86_400 });
  if (seconds % 3_600 === 0) return t("{count} hours", "{count} 小时", { count: seconds / 3_600 });
  return t("{count} minutes", "{count} 分钟", { count: Math.max(1, Math.round(seconds / 60)) });
}

export function ActivityHealthBadge({
  task,
  thresholdSeconds,
  compact = false,
  className,
}: {
  task: Task;
  thresholdSeconds?: number;
  compact?: boolean;
  className?: string;
}) {
  const { t } = useI18n();
  if (task.activityHealth !== "stagnant") return null;

  const threshold = durationLabel(thresholdSeconds, t);
  const lastActivity = task.lastMeaningfulAt ? timeAgo(task.lastMeaningfulAt) : "";
  const workflow = statusLabel(task.status);
  const explanation = lastActivity
    ? t(
      "No meaningful activity for {threshold}; the workflow status is still {status}. Last meaningful activity: {lastActivity}.",
      "已超过 {threshold} 没有有效动作；任务状态仍是“{status}”。上次有效动作在 {lastActivity}。",
      { threshold, status: workflow, lastActivity },
    )
    : t(
      "No meaningful activity for {threshold}; the workflow status is still {status}.",
      "已超过 {threshold} 没有有效动作；任务状态仍是“{status}”。",
      { threshold, status: workflow },
    );

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant="outline"
          aria-label={t("Stagnant task", "任务停滞")}
          className={cn(
            "border-warning/35 bg-warning/10 text-warning",
            compact && "h-4 gap-0.5 px-1.5 text-[10px] font-medium",
            className,
          )}
        >
          <ClockAlert />
          {t("Stagnant", "停滞")}
        </Badge>
      </TooltipTrigger>
      <TooltipContent className="max-w-72 leading-5">{explanation}</TooltipContent>
    </Tooltip>
  );
}

export function UnknownActivityHealthBadge({ task }: { task: Task }) {
  const { t } = useI18n();
  if (task.activityHealth !== "unknown") return null;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="text-muted-foreground">
          <CircleHelp />{t("Activity unknown", "活动时间未知")}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>{t("This task has no usable activity timestamp yet.", "此任务还没有可用的有效活动时间。")}</TooltipContent>
    </Tooltip>
  );
}
