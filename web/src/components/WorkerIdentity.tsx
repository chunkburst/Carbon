import { Circle } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { cn, initials } from "@/lib/utils";
import { useWorkerAliasMap, type WorkerAliasMap } from "@/lib/worker-aliases";

export type WorkerIdentityProps = {
  actor: string;
  active?: boolean;
  deleted?: boolean;
  compact?: boolean;
  aliases?: WorkerAliasMap;
  className?: string;
};

/**
 * One display boundary for Worker identity. The actor stays canonical in every action;
 * aliases only change the readable label and always keep the canonical value visible.
 */
export function WorkerIdentity({ actor, active = false, deleted = false, compact = false, aliases: aliasesProp, className }: WorkerIdentityProps) {
  const { t } = useI18n();
  const contextAliases = useWorkerAliasMap();
  const aliases = aliasesProp ?? contextAliases;
  const alias = aliases[actor]?.trim();
  const display = alias || actor;

  return (
    <div className={cn("flex min-w-0 items-center gap-2", className)} title={actor}>
      <span
        aria-hidden="true"
        className={cn(
          "grid shrink-0 place-items-center rounded-md border bg-muted font-mono text-[10px] font-semibold text-muted-foreground",
          compact ? "size-5" : "size-7",
          deleted && "opacity-45",
        )}
      >
        {initials(actor)}
      </span>
      <span className="min-w-0">
        <span className={cn("flex min-w-0 items-center gap-1.5", deleted && "opacity-55")}>
          <Circle
            aria-label={active ? t("working", "进行中") : t("not working", "当前无任务")}
            className={cn("size-1.5 shrink-0 fill-muted-foreground text-muted-foreground", active && "fill-success text-success")}
          />
          <span className={cn("truncate font-medium", compact ? "text-xs" : "text-sm")}>{display}</span>
        </span>
        {alias && !compact && (
          <span className="block truncate font-mono text-[10px] text-muted-foreground">({actor})</span>
        )}
      </span>
    </div>
  );
}
