import type { ReactElement } from "react";
import { ClipboardCopy, ExternalLink, Pencil, RotateCcw, Text, UserRoundX } from "lucide-react";
import { toast } from "sonner";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { useI18n } from "@/lib/i18n";

type WorkerContextMenuProps = {
  /** Use a native, focusable trigger and mark it with data-carbon-context-surface. */
  children: ReactElement;
  actor: string;
  displayName: string;
  pending?: boolean;
  onOpenWorker?: (actor: string) => void;
  onEditAlias?: () => void;
  onReset?: () => void;
  onDelete?: () => void;
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

/**
 * The roster and Worker directory intentionally share the same object menu.
 * The trigger owns `data-carbon-context-surface`, so the workspace fallback
 * does not replace an actor-specific action with a background menu.
 */
export function WorkerContextMenu({
  children,
  actor,
  displayName,
  pending = false,
  onOpenWorker,
  onEditAlias,
  onReset,
  onDelete,
}: WorkerContextMenuProps) {
  const { t } = useI18n();
  const copy = async (value: string, success: string) => {
    try {
      await copyText(value);
      toast.success(success);
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  const hasManagementActions = Boolean(onEditAlias || onReset || onDelete);

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
      <ContextMenuContent className="min-w-52">
        <ContextMenuLabel className="max-w-64 truncate">{displayName}</ContextMenuLabel>
        <ContextMenuGroup>
          <ContextMenuItem disabled={!onOpenWorker} onSelect={() => onOpenWorker?.(actor)}>
            <ExternalLink />
            {t("Open agent profile", "打开智能体档案")}
          </ContextMenuItem>
        </ContextMenuGroup>
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuItem onSelect={() => void copy(actor, t("Connection ID copied", "连接标识已复制"))}>
            <ClipboardCopy />
            {t("Copy connection ID", "复制连接标识")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => void copy(displayName, t("Display name copied", "显示名称已复制"))}>
            <Text />
            {t("Copy display name", "复制显示名称")}
          </ContextMenuItem>
        </ContextMenuGroup>
        {hasManagementActions && <ContextMenuSeparator />}
        {hasManagementActions && (
          <ContextMenuGroup>
            {onEditAlias && <ContextMenuItem disabled={pending} onSelect={onEditAlias}><Pencil />{t("Edit display name", "编辑显示名称")}</ContextMenuItem>}
            {onReset && <ContextMenuItem disabled={pending} onSelect={onReset}><RotateCcw />{t("Restart work statistics", "重新统计工作数据")}</ContextMenuItem>}
            {onDelete && <ContextMenuItem variant="destructive" disabled={pending} onSelect={onDelete}><UserRoundX />{t("Remove from team", "移出智能体团队")}</ContextMenuItem>}
          </ContextMenuGroup>
        )}
      </ContextMenuContent>
    </ContextMenu>
  );
}
