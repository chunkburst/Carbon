import { ClipboardCopy, ExternalLink, Eye, FileText, MessageSquareDashed, MoreHorizontal, Pencil, Trash2, UserRound } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import { WorkLogVisibilityBadge } from "@/components/WorkLogVisibilityBadge";
import { workLogCoordinationDraft, workLogTaskNavigationTarget, type TaskNavigationTarget, type WorkLog } from "@/components/WorkLogTypes";
import { useI18n } from "@/lib/i18n";
import { cn, timeAgo } from "@/lib/utils";
import type { WorkerAliasMap } from "@/lib/worker-aliases";

export type WorkLogTableProps = {
  logs: readonly WorkLog[];
  aliases?: WorkerAliasMap;
  compact?: boolean;
  pendingID?: string;
  onOpenTask?: (target: TaskNavigationTarget) => void;
  onOpenWorker?: (actor: string) => void;
  onView?: (log: WorkLog) => void;
  onEdit?: (log: WorkLog) => void;
  onDelete?: (log: WorkLog) => void;
  canEdit?: (log: WorkLog) => boolean;
  canDelete?: (log: WorkLog) => boolean;
  emptyLabel?: string;
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

export function WorkLogTable({
  logs,
  aliases,
  compact = false,
  pendingID,
  onOpenTask,
  onOpenWorker,
  onView,
  onEdit,
  onDelete,
  canEdit,
  canDelete,
  emptyLabel,
}: WorkLogTableProps) {
  const { t } = useI18n();
  const copyLogId = async (id: string) => {
    try {
      await copyText(id);
      toast.success(t("Work log ID copied", "工作日志 ID 已复制"));
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  if (!logs.length) {
    return <div className="px-4 py-10 text-center text-sm text-muted-foreground">{emptyLabel ?? t("No work logs match this view.", "没有符合当前视图的工作日志。")}</div>;
  }
  return (
    <Table className={cn("min-w-[760px] text-[13px]", compact && "min-w-[620px]")}>
      <TableHeader>
        <TableRow>
          <TableHead className="h-8 w-24 px-4 text-xs">{t("Updated", "更新")}</TableHead>
          {!compact && <TableHead className="h-8 w-48 text-xs">{t("Agent", "智能体")}</TableHead>}
          <TableHead className="h-8 text-xs">{t("Entry", "记录")}</TableHead>
          <TableHead className="h-8 w-28 text-xs">{t("Visibility", "可见性")}</TableHead>
          {!compact && <TableHead className="h-8 w-40 text-xs">{t("Scope", "范围")}</TableHead>}
          {!compact && <TableHead className="h-8 w-36 text-xs">{t("Tags", "标签")}</TableHead>}
          {(onView || onEdit || onDelete) && <TableHead className="h-8 w-10 text-right text-xs"><span className="sr-only">{t("Actions", "操作")}</span></TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>
        {logs.map((log) => {
          const pending = pendingID === log.id;
          const coordination = workLogCoordinationDraft(log);
          const displayTags = coordination?.userTags ?? log.tags ?? [];
          // Coordination drafts are an append-only message stream. Replies are
          // new Work Logs in the same thread, never edits of the original.
          const editAllowed = Boolean(!coordination && onEdit && (canEdit?.(log) ?? true));
          const deleteAllowed = Boolean(!coordination && onDelete && (canDelete?.(log) ?? true));
          const taskTarget = workLogTaskNavigationTarget(log);
          return (
            <ContextMenu key={log.id}>
              <ContextMenuTrigger asChild>
                <tr
                  tabIndex={0}
                  data-carbon-context-surface
                  data-carbon-task-surface
                  aria-label={t("Work log {id}", "工作日志 {id}", { id: log.id })}
                  className="group border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted"
                >
                  <TableCell className="px-4 py-2 align-top">
                    <span className="block text-xs tabular-nums">{timeAgo(log.updatedAt)}</span>
                    <span className="block font-mono text-[10px] text-muted-foreground">{log.id}</span>
                  </TableCell>
                  {!compact && (
                    <TableCell className="py-2 align-top">
                      <button
                        type="button"
                        disabled={!onOpenWorker}
                        onClick={() => onOpenWorker?.(log.worker)}
                        className={cn("block min-w-0 text-left", onOpenWorker && "rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
                      >
                        <WorkerIdentity actor={log.worker} aliases={aliases} compact />
                      </button>
                    </TableCell>
                  )}
                  <TableCell className="max-w-[32rem] py-2 align-top">
                    <div className="flex min-w-0 items-center gap-1.5">
                      {coordination
                        ? <MessageSquareDashed className="size-3.5 shrink-0 text-brand" />
                        : <FileText className="size-3.5 shrink-0 text-muted-foreground" />}
                      <span className="truncate font-medium">{log.title}</span>
                      {coordination && <Badge variant="secondary" className="h-5 shrink-0 px-1.5 text-[10px]">{t("Team draft", "团队草稿")}</Badge>}
                    </div>
                    {log.body && <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">{firstLine(log.body)}</p>}
                    {compact && <div className="mt-0.5"><WorkerIdentity actor={log.worker} aliases={aliases} compact /></div>}
                  </TableCell>
                  <TableCell className="py-2 align-top"><WorkLogVisibilityBadge visibility={log.visibility} /></TableCell>
                  {!compact && (
                    <TableCell className="py-2 align-top">
                      <ScopeLinks log={log} onOpenTask={onOpenTask} />
                    </TableCell>
                  )}
                  {!compact && (
                    <TableCell className="py-2 align-top">
                      <div className="flex flex-wrap gap-1">
                        {coordination?.thread && <span className="rounded border border-brand/25 bg-brand/10 px-1.5 py-0.5 font-mono text-[10px] text-brand">#{coordination.thread}</span>}
                        {coordination && <span className="rounded border bg-muted/35 px-1.5 py-0.5 text-[10px] text-muted-foreground" title={coordination.recipients.join(", ")}>{coordination.recipients.length ? t("To {count}", "发给 {count} 位", { count: coordination.recipients.length }) : t("Project broadcast", "项目广播")}</span>}
                        {displayTags.slice(0, coordination ? 1 : 3).map((tag) => <span key={tag} className="rounded border bg-muted/35 px-1.5 py-0.5 text-[10px] text-muted-foreground">{tag}</span>)}
                        {displayTags.length > (coordination ? 1 : 3) && <span className="text-[10px] text-muted-foreground">+{displayTags.length - (coordination ? 1 : 3)}</span>}
                      </div>
                    </TableCell>
                  )}
                  {(onView || editAllowed || deleteAllowed) && (
                    <TableCell className="px-2 py-2 align-top text-right">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon-xs" aria-label={t("Work log actions", "工作日志操作")} disabled={pending}><MoreHorizontal /></Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-52">
                          <DropdownMenuLabel className="truncate font-mono text-[10px]">{log.id}</DropdownMenuLabel>
                          {onView && <DropdownMenuItem onSelect={() => onView(log)}><Eye />{t("View details", "查看详情")}</DropdownMenuItem>}
                          {onOpenTask && taskTarget && <DropdownMenuItem onSelect={() => onOpenTask(taskTarget)}><ExternalLink />{t("Open linked task", "打开关联任务")}</DropdownMenuItem>}
                          {onOpenWorker && <DropdownMenuItem onSelect={() => onOpenWorker(log.worker)}><UserRound />{t("Open agent profile", "打开智能体档案")}</DropdownMenuItem>}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem onSelect={() => void copyLogId(log.id)}><ClipboardCopy />{t("Copy work log ID", "复制工作日志 ID")}</DropdownMenuItem>
                          {(editAllowed || deleteAllowed) && <DropdownMenuSeparator />}
                          {editAllowed && <DropdownMenuItem onSelect={() => onEdit?.(log)}><Pencil />{t("Edit", "编辑")}</DropdownMenuItem>}
                          {deleteAllowed && <DropdownMenuItem variant="destructive" onSelect={() => onDelete?.(log)}><Trash2 />{t("Delete", "删除")}</DropdownMenuItem>}
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  )}
                </tr>
              </ContextMenuTrigger>
              <ContextMenuContent className="min-w-52">
                <ContextMenuLabel className="max-w-64 truncate font-mono text-[10px]">{log.id}</ContextMenuLabel>
                <ContextMenuGroup>
                  {onView && <ContextMenuItem disabled={pending} onSelect={() => onView(log)}><Eye />{t("View details", "查看详情")}</ContextMenuItem>}
                  {onOpenTask && taskTarget && <ContextMenuItem onSelect={() => onOpenTask(taskTarget)}><ExternalLink />{t("Open linked task", "打开关联任务")}</ContextMenuItem>}
                  {onOpenWorker && <ContextMenuItem onSelect={() => onOpenWorker(log.worker)}><UserRound />{t("Open agent profile", "打开智能体档案")}</ContextMenuItem>}
                </ContextMenuGroup>
                <ContextMenuSeparator />
                <ContextMenuGroup>
                  <ContextMenuItem onSelect={() => void copyLogId(log.id)}><ClipboardCopy />{t("Copy work log ID", "复制工作日志 ID")}</ContextMenuItem>
                </ContextMenuGroup>
                {(editAllowed || deleteAllowed) && <ContextMenuSeparator />}
                {(editAllowed || deleteAllowed) && (
                  <ContextMenuGroup>
                    {editAllowed && <ContextMenuItem disabled={pending} onSelect={() => onEdit?.(log)}><Pencil />{t("Edit", "编辑")}</ContextMenuItem>}
                    {deleteAllowed && <ContextMenuItem variant="destructive" disabled={pending} onSelect={() => onDelete?.(log)}><Trash2 />{t("Delete", "删除")}</ContextMenuItem>}
                  </ContextMenuGroup>
                )}
              </ContextMenuContent>
            </ContextMenu>
          );
        })}
      </TableBody>
    </Table>
  );
}

function ScopeLinks({ log, onOpenTask }: { log: WorkLog; onOpenTask?: (target: TaskNavigationTarget) => void }) {
  const { t } = useI18n();
  const taskTarget = workLogTaskNavigationTarget(log);
  return (
    <div className="space-y-0.5 text-xs text-muted-foreground">
      <span className="block truncate font-mono" title={log.clusterId}>{t("Cluster", "集群")} · {log.clusterId}</span>
      {log.projectId && <span className="block truncate font-mono" title={log.projectId}>{t("Project", "项目")} · {log.projectId}</span>}
      {log.taskId && (
        <button
          type="button"
          disabled={!onOpenTask || !taskTarget}
          onClick={() => taskTarget && onOpenTask?.(taskTarget)}
          className={cn("block font-mono", onOpenTask && taskTarget && "text-brand hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}
        >
          {t("Task", "任务")} · {log.taskId}
        </button>
      )}
    </div>
  );
}

function firstLine(value: string): string {
  return value.split(/\r?\n/).find((line) => line.trim())?.trim() ?? "";
}
