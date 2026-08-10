import { Bot, User } from "lucide-react";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import { actorKind, cn, initials } from "@/lib/utils";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

// Assignee is deliberately a compact button rather than passive decoration: ownership
// is the natural entry point to a worker's activity trail. It always absorbs the row
// click so an avatar interaction cannot accidentally open the surrounding task.
export function Assignee({
  actor,
  className,
  onOpenWorker,
}: {
  actor?: string;
  className?: string;
  onOpenWorker?: (actor: string) => void;
}) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  if (!actor) return null;
  const kind = actorKind(actor);
  const Icon = kind === "agent" ? Bot : User;
  const display = formatWorker(actor);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          className={cn("relative rounded-full p-0", className)}
          aria-label={t("Open Worker {worker}", "打开 Worker {worker}", { worker: display })}
          onPointerDown={(event) => event.stopPropagation()}
          onClick={(event) => {
            event.stopPropagation();
            onOpenWorker?.(actor);
          }}
        >
          <Avatar className="size-5">
            <AvatarFallback className="text-[9px]">{initials(display)}</AvatarFallback>
          </Avatar>
          <span
            className={cn(
              "absolute -right-1 -bottom-1 grid size-3 place-items-center rounded-full border bg-panel",
              kind === "agent" ? "text-brand" : "text-muted-foreground",
            )}
          >
            <Icon className="size-2" />
          </span>
        </Button>
      </TooltipTrigger>
      <TooltipContent>
        {kind === "agent" ? t("AI agent", "AI 智能体") : kind === "human" ? t("Human", "人类") : t("Assignee", "负责人")} · {display}
        {onOpenWorker ? ` · ${t("Open Worker", "打开 Worker")}` : ""}
      </TooltipContent>
    </Tooltip>
  );
}
