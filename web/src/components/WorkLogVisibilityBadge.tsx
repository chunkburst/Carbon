/* eslint-disable react-refresh/only-export-components -- visibility metadata is intentionally kept beside its rendered badge. */
import { Globe2, LockKeyhole, UsersRound } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { WorkLogVisibility } from "@/components/WorkLogTypes";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export function WorkLogVisibilityBadge({ visibility, className }: { visibility: WorkLogVisibility; className?: string }) {
  const { t } = useI18n();
  const detail = visibilityDetail(visibility, t);
  const Icon = visibility === "worker_private" ? LockKeyhole : visibility === "project_public" ? UsersRound : Globe2;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant={visibility === "worker_private" ? "outline" : "secondary"}
          className={cn(
            "h-5 gap-1 px-1.5 text-[10px] font-medium",
            visibility === "worker_private" && "border-warning/40 bg-warning/10 text-warning",
            className,
          )}
        >
          <Icon className="size-3" />
          {detail.label}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>{detail.description}</TooltipContent>
    </Tooltip>
  );
}

export function visibilityDetail(visibility: WorkLogVisibility, t: (en: string, zh: string) => string) {
  switch (visibility) {
    case "worker_private":
      return {
        label: t("Agent only", "仅相关智能体"),
        description: t("Only the recording agent and local administrator can view this entry.", "仅记录该日志的智能体和本机管理员可以查看。"),
      };
    case "project_public":
      return {
        label: t("Project", "项目"),
        description: t("Visible within the linked project.", "在关联项目内可见。"),
      };
    default:
      return {
        label: t("All projects", "全部项目"),
        description: t("Visible across projects in the current Carbon workspace.", "在当前 Carbon 工作区的多个项目中可见。"),
      };
  }
}
