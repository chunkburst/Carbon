import { CarbonIdentitySettings } from "@/components/CarbonManagementSettings";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { CarbonScope } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";

export function WorkerIdentityManagerDialog({
  open,
  onOpenChange,
  scope,
  actor,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scope?: CarbonScope;
  actor?: string;
}) {
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("Identity and responsibilities", "身份与分工")}</DialogTitle>
          <DialogDescription>{actor ? t("Adjust this Worker's roles and the task types it may take in the selected project.", "调整这个 Worker 在当前项目中的身份，以及它可以接手的任务类型。") : t("Manage project roles and task-type coverage.", "管理当前项目的身份与任务类型分工。")}</DialogDescription>
        </DialogHeader>
        {scope ? <CarbonIdentitySettings scope={scope} initialActor={actor} /> : <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">{t("Choose a project before editing a Worker identity.", "请先选择具体项目，再编辑 Worker 身份。")}</p>}
      </DialogContent>
    </Dialog>
  );
}
