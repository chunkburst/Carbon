import { ClipboardCopy, ExternalLink, FileText, MessageSquareDashed, UserRound } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { workLogCoordinationDraft, workLogTaskNavigationTarget, type TaskNavigationTarget, type WorkLog } from "@/components/WorkLogTypes";
import { WorkLogVisibilityBadge } from "@/components/WorkLogVisibilityBadge";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export type WorkLogDetailsDialogProps = {
  log?: WorkLog | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenWorker?: (actor: string) => void;
  onOpenTask?: (target: TaskNavigationTarget) => void;
};

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Clipboard access is unavailable");
}

/** Read-only, complete record view. It remains available for Agent-owned logs. */
export function WorkLogDetailsDialog({
  log,
  open,
  onOpenChange,
  onOpenWorker,
  onOpenTask,
}: WorkLogDetailsDialogProps) {
  const { t } = useI18n();
  if (!log) return null;
  const taskTarget = workLogTaskNavigationTarget(log);
  const coordination = workLogCoordinationDraft(log);
  const displayTags = coordination?.userTags ?? log.tags ?? [];
  const copyLogId = async () => {
    try {
      await copyText(log.id);
      toast.success(t("Work log ID copied", "工作日志 ID 已复制"));
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <ContextMenu>
          <ContextMenuTrigger asChild>
            <div data-carbon-context-surface className="contents">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {coordination ? <MessageSquareDashed className="size-4 text-brand" /> : <FileText className="size-4 text-brand" />}
            {coordination ? t("Team draft", "团队草稿") : t("Work log details", "工作日志详情")}
          </DialogTitle>
          <DialogDescription className="font-mono text-[11px]">{log.id}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="flex flex-wrap items-start justify-between gap-2 border-b pb-3">
            <div className="min-w-0">
              <h2 className="break-words text-base font-semibold">{log.title}</h2>
              <p className="mt-1 text-xs text-muted-foreground">{coordination
                ? t("A shared note for the team before the task is taken over", "任务接手前，供团队沟通和分工的共享草稿")
                : t("A saved record of the work", "已保存的工作记录")}</p>
            </div>
            <WorkLogVisibilityBadge visibility={log.visibility} />
          </div>

          {coordination && (
            <div className="rounded-lg border border-brand/25 bg-brand/8 px-3 py-2 text-xs leading-relaxed text-muted-foreground">
              {t(
                "This draft is shown only to selected agents. A broadcast is shared with the agents in this project. To reply, add a new draft in the same thread; the original stays as it was.",
                "此草稿仅对指定智能体可见；广播会发送给项目中的智能体。请在同一会话中新建草稿回复，原内容会保留不变。",
              )}
            </div>
          )}

          <dl className="grid gap-x-5 gap-y-2 text-sm sm:grid-cols-2">
            <Detail label={t("Agent", "智能体")}>
              <button type="button" disabled={!onOpenWorker} onClick={() => onOpenWorker?.(log.worker)} className={cn("text-left", onOpenWorker && "rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}>
                <WorkerIdentity actor={log.worker} compact />
              </button>
            </Detail>
            <Detail label={t("Visibility", "可见性")}><WorkLogVisibilityBadge visibility={log.visibility} /></Detail>
            <Detail label={t("Cluster ID", "集群 ID")}><code className="break-all font-mono text-xs">{log.clusterId || "—"}</code></Detail>
            <Detail label={t("Project ID", "项目 ID")}><code className="break-all font-mono text-xs">{log.projectId || "—"}</code></Detail>
            <Detail label={t("Task ID", "任务 ID")}>
              {log.taskId && onOpenTask && taskTarget ? (
                <Button variant="link" size="sm" className="h-auto px-0 font-mono text-xs" onClick={() => onOpenTask(taskTarget)}>{log.taskId}<ExternalLink className="size-3" /></Button>
              ) : <code className="break-all font-mono text-xs">{log.taskId || "—"}</code>}
            </Detail>
            {coordination && <Detail label={t("Recipients", "收件人")}>{coordination.recipients.length ? <span className="flex flex-wrap gap-1">{coordination.recipients.map((actor) => <code key={actor} className="rounded border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px]">{actor}</code>)}</span> : <span>{t("All agents in this project", "项目中的全部智能体")}</span>}</Detail>}
            {coordination && <Detail label={t("Thread", "会话")}><code className="break-all font-mono text-xs">{coordination.thread || t("No thread", "无会话")}</code></Detail>}
            <Detail label={t("Tags", "标签")}>
              {displayTags.length ? <span className="flex flex-wrap gap-1">{displayTags.map((tag) => <span key={tag} className="rounded border bg-muted/30 px-1.5 py-0.5 text-[10px]">{tag}</span>)}</span> : <span className="text-muted-foreground">—</span>}
            </Detail>
          </dl>

          <section className="border-y py-3">
            <h3 className="mb-1.5 text-xs font-medium text-muted-foreground">{coordination ? t("Message", "沟通内容") : t("Record details", "记录内容")}</h3>
            <p className="whitespace-pre-wrap break-words text-sm leading-6">{log.body || t("No additional details.", "没有额外详情。")}</p>
          </section>

          <dl className="grid gap-x-5 gap-y-2 text-xs text-muted-foreground sm:grid-cols-2">
            <Detail label={t("Created", "创建")}>{log.createdAt || "—"}</Detail>
            <Detail label={t("Recorded by", "记录人")}><code className="break-all font-mono">{log.createdBy || "—"}</code></Detail>
            <Detail label={t("Updated", "更新")}>{log.updatedAt || "—"}</Detail>
            <Detail label={t("Last updated by", "最后更新人")}><code className="break-all font-mono">{log.updatedBy || "—"}</code></Detail>
            <Detail label={t("Version", "版本")} className="sm:col-span-2"><code className="break-all font-mono">{log.version || "—"}</code></Detail>
          </dl>
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t("Close", "关闭")}</Button></DialogFooter>
            </div>
          </ContextMenuTrigger>
          <ContextMenuContent className="min-w-52">
            <ContextMenuLabel className="max-w-64 truncate font-mono text-[10px]">{log.id}</ContextMenuLabel>
            <ContextMenuGroup>
              {onOpenTask && taskTarget && <ContextMenuItem onSelect={() => onOpenTask(taskTarget)}><ExternalLink />{t("Open linked task", "打开关联任务")}</ContextMenuItem>}
              {onOpenWorker && <ContextMenuItem onSelect={() => onOpenWorker(log.worker)}><UserRound />{t("Open agent profile", "打开智能体档案")}</ContextMenuItem>}
            </ContextMenuGroup>
            <ContextMenuSeparator />
            <ContextMenuGroup>
              <ContextMenuItem onSelect={() => void copyLogId()}><ClipboardCopy />{t("Copy work log ID", "复制工作日志 ID")}</ContextMenuItem>
            </ContextMenuGroup>
          </ContextMenuContent>
        </ContextMenu>
      </DialogContent>
    </Dialog>
  );
}

function Detail({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={className}>
      <dt className="mb-0.5 text-[10px] font-medium tracking-wide text-muted-foreground uppercase">{label}</dt>
      <dd className="min-h-5 text-foreground">{children}</dd>
    </div>
  );
}
