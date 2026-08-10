import { CircleDot, ClockAlert, ScanEye } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { ExecutionState } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

export function SessionStatus({ state, compact = false }: { state?: ExecutionState; compact?: boolean }) {
  const { t } = useI18n();
  const states = {
    active: { label: t("Active", "进行中"), icon: CircleDot, className: "text-brand" },
    stalled: { label: t("Stalled", "已停滞"), icon: ClockAlert, className: "text-destructive" },
    awaiting_review: { label: t("Awaiting review", "等待审核"), icon: ScanEye, className: "text-foreground" },
  } satisfies Record<ExecutionState, { label: string; icon: typeof CircleDot; className: string }>;
  if (!state) return null;
  const { label, icon: Icon, className } = states[state];

  if (compact) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span aria-label={label} className={cn("inline-flex shrink-0", className)}>
            <Icon className="size-3.5" />
          </span>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    );
  }

  return (
    <Badge variant="outline" className={cn("font-normal", className)}>
      <Icon />
      {label}
    </Badge>
  );
}
