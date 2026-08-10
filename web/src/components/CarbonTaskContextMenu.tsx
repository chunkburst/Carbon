import type { ReactElement } from "react";
import { ClipboardCopy, ExternalLink, MessageSquareCode, Text, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { StatusIcon } from "@/components/StatusIcon";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { useI18n } from "@/lib/i18n";
import type { Status, Task } from "@/lib/api";

type CarbonTaskContextMenuProps = {
  children: ReactElement;
  task: Task;
  status: Status;
  transitioning?: boolean;
  taskHref?: (task: Task) => string;
  statusLabel?: (status: string) => string;
  onOpenTask: (task: Task) => void;
  onTransition: (task: Task, to: string) => void;
  onTrashTask?: (task: Task) => void;
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

// Native ContextMenu supplies keyboard invocation (Shift+F10 / Menu) and Escape
// dismissal. Every action also stops propagation so it remains an action on this task,
// not an accidental row activation.
export function CarbonTaskContextMenu({
  children,
  task,
  status,
  transitioning = false,
  taskHref,
  statusLabel,
  onOpenTask,
  onTransition,
  onTrashTask,
}: CarbonTaskContextMenuProps) {
  const { t } = useI18n();
  const copy = async (value: string, success: string) => {
    try {
      await copyText(value);
      toast.success(success);
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  const href = taskHref?.(task) ?? window.location.href;

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuLabel>{task.id}</ContextMenuLabel>
        <ContextMenuItem
          onSelect={(event) => {
            event.stopPropagation();
            onOpenTask(task);
          }}
        >
          <ExternalLink />
          {t("Open task details", "打开任务详情")}
        </ContextMenuItem>
        <ContextMenuItem
          onSelect={(event) => {
            event.stopPropagation();
            void copy(task.id, t("Task ID copied", "任务 ID 已复制"));
          }}
        >
          <ClipboardCopy />
          {t("Copy task ID", "复制任务 ID")}
        </ContextMenuItem>
        <ContextMenuItem
          onSelect={(event) => {
            event.stopPropagation();
            void copy(task.title, t("Task title copied", "任务标题已复制"));
          }}
        >
          <Text />
          {t("Copy task title", "复制任务标题")}
        </ContextMenuItem>
        <ContextMenuItem
          onSelect={(event) => {
            event.stopPropagation();
            void copy(href, t("Task link copied", "任务链接已复制"));
          }}
        >
          <ClipboardCopy />
          {t("Copy task link", "复制任务链接")}
        </ContextMenuItem>
        <ContextMenuItem
          onSelect={(event) => {
            event.stopPropagation();
            const prompt = `Work on Carbon task ${task.id}: ${task.title}\n${href}`;
            void copy(prompt, t("Agent prompt copied", "智能体提示词已复制"));
          }}
        >
          <MessageSquareCode />
          {t("Copy Agent prompt", "复制智能体提示词")}
        </ContextMenuItem>
        <ContextMenuSeparator />
        <ContextMenuSub>
          <ContextMenuSubTrigger>{t("Change status", "修改状态")}</ContextMenuSubTrigger>
          <ContextMenuSubContent>
            {(status.states ?? []).map((next) => (
              <ContextMenuItem
                key={next}
                disabled={transitioning || next === task.status}
                onSelect={(event) => {
                  event.stopPropagation();
                  onTransition(task, next);
                }}
              >
                <StatusIcon status={next} closed={status.closed} initial={status.initial} />
                {statusLabel?.(next) ?? next}
              </ContextMenuItem>
            ))}
          </ContextMenuSubContent>
        </ContextMenuSub>
        {onTrashTask && (
          <>
            <ContextMenuSeparator />
            <ContextMenuItem
              variant="destructive"
              disabled={transitioning || !task.version}
              onSelect={(event) => {
                event.stopPropagation();
                onTrashTask(task);
              }}
            >
              <Trash2 />
              {t("Move to trash", "移入回收站")}
            </ContextMenuItem>
          </>
        )}
      </ContextMenuContent>
    </ContextMenu>
  );
}
