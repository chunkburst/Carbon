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
        label: t("Worker private", "Worker 私有"),
        description: t("Hidden from other Workers; the owning Worker and local administrator can inspect it.", "对其他 Worker 隐藏；所属 Worker 与本地管理员可以查看。"),
      };
    case "project_public":
      return {
        label: t("Project", "项目"),
        description: t("Visible within the linked project.", "在关联项目内可见。"),
      };
    default:
      return {
        label: t("Global", "全局"),
        description: t("Visible across project clusters in the current Carbon Home.", "在当前 Carbon Home 内可跨项目集群查看。"),
      };
  }
}
