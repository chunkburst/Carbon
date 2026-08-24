import { Bot, User } from "lucide-react";
import { Avatar, AvatarBadge, AvatarFallback } from "@/components/ui/avatar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import { actorKind, cn, initials } from "@/lib/utils";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

type AssigneeProps = {
  actor?: string;
  className?: string;
  /** Kept for already-mounted task views. An assignee remains non-navigational. */
  onOpenWorker?: (actor: string) => void;
  /** The holder is optional because task rows can render before lease data is available. */
  leaseHolder?: string;
};

/**
 * A compact task-owner boundary. It never implies that the assignee is currently
 * executing the task: the active lease, when present, is the execution authority.
 * It is intentionally non-interactive so surrounding task-row clicks keep their
 * original behaviour.
 */
export function Assignee({ actor, className, leaseHolder }: AssigneeProps) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  if (!actor) return null;
  const kind = actorKind(actor);
  const Icon = kind === "agent" ? Bot : User;
  const display = formatWorker(actor);
  const leaseDisplay = leaseHolder ? formatWorker(leaseHolder) : "";

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className={cn("inline-flex shrink-0 items-center gap-1 rounded-full border border-border/70 bg-muted/45 py-0.5 pr-1.5 pl-0.5", className)}
          aria-label={t("Task owner: {owner}", "任务负责人：{owner}", { owner: display })}
        >
          <Avatar className="size-5">
            <AvatarFallback className="text-[9px]">{initials(display)}</AvatarFallback>
            <AvatarBadge className="bg-muted text-muted-foreground"><Icon /></AvatarBadge>
          </Avatar>
          <span className="hidden max-w-24 truncate text-[10px] text-muted-foreground xl:inline">{display}</span>
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <div className="flex max-w-64 flex-col gap-1">
          <span>{t("Task lead", "任务负责人")} · {display}</span>
          <span className="text-xs text-muted-foreground">
            {leaseDisplay
              ? t("Currently handling: {worker}", "当前处理：{worker}", { worker: leaseDisplay })
              : t("The current handler appears after someone takes over the task.", "有人接手后，这里会显示当前处理者。")}
          </span>
        </div>
      </TooltipContent>
    </Tooltip>
  );
}
