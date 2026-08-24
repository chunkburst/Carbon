import { useEffect, useMemo, useState, type MouseEvent } from "react";
import { ClipboardCopy, RotateCcw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { Input } from "@/components/ui/input";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCarbonTrash, useEmptyTrash, useRestoreTrash } from "@/lib/queries";
import type { CarbonScopeInput, CarbonTrashItem } from "@/lib/carbon-api";
import { timeAgo } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

type ProjectChoice = { id: string; name: string };

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

function preserveNativeTextContextMenu(event: MouseEvent<HTMLElement>) {
  const target = event.target;
  if (target instanceof Element && target.closest("input, textarea, [contenteditable='true']")) {
    event.stopPropagation();
  }
}

export function Trash({
  path = "",
  carbonScope,
  projects = [],
}: {
  path?: string;
  carbonScope?: CarbonScopeInput;
  projects?: ProjectChoice[];
}) {
  const { t } = useI18n();
  const scope = carbonScope ?? path;
  const isClusterScope = typeof carbonScope === "object" && Boolean(carbonScope.clusterId) && !carbonScope.projectId;
  const trash = useCarbonTrash(scope);
  const restore = useRestoreTrash(path, scope);
  const empty = useEmptyTrash(path, scope);
  const [confirmEmpty, setConfirmEmpty] = useState(false);
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  // Omit projectId to preserve the original project. An explicitly empty value is
  // still available for deliberate cluster-wide restoration.
  const [restoreTarget, setRestoreTarget] = useState("original");
  const available = trash.data?.available === true;
  const items = useMemo<CarbonTrashItem[]>(() => trash.data?.available ? trash.data.data.entries ?? [] : [], [trash.data]);
  const needle = filter.trim().toLowerCase();
  const visible = useMemo(
    () => items.filter((item) => !needle || [item.id, item.title, item.project_id, item.assignee, item.type, ...(item.labels ?? [])]
      .filter(Boolean)
      .some((value) => value!.toLowerCase().includes(needle))),
    [items, needle],
  );
  const selectedVisible = visible.filter((item) => selected.has(item.id));
  const allVisibleSelected = visible.length > 0 && selectedVisible.length === visible.length;

  useEffect(() => {
    const ids = new Set(items.map((item) => item.id));
    setSelected((current) => new Set([...current].filter((id) => ids.has(id))));
  }, [items]);

  const toggle = (id: string, checked: boolean) => {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };
  // A route change can leave a dialog-local choice behind. Never send the
  // cluster-wide sentinel from an independent project's trash view.
  const effectiveRestoreTarget = !isClusterScope && restoreTarget === "cluster" ? "original" : restoreTarget;
  const projectIdForRestore = effectiveRestoreTarget === "original" ? undefined : effectiveRestoreTarget === "cluster" ? "" : effectiveRestoreTarget;
  const restoreOne = (item: CarbonTrashItem) => restore.mutate({
    id: item.id,
    projectId: projectIdForRestore,
    expectedVersion: item.etag ?? item.version,
  });
  const copyTaskId = async (id: string) => {
    try {
      await copyText(id);
      toast.success(t("Task ID copied", "任务 ID 已复制"));
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  const restoreSelected = () => {
    for (const item of selectedVisible) restoreOne(item);
  };

  return (
    <div className="flex h-full min-w-0 flex-col" onContextMenuCapture={preserveNativeTextContextMenu}>
      <header className="flex min-h-12 shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2">
        <div>
          <h1 className="text-base font-semibold">{t("Trash", "回收站")}</h1>
          <p className="text-xs text-muted-foreground">
            {isClusterScope
              ? t("Restore tasks from this cluster, including shared work, or permanently empty the cluster trash.", "恢复此集群（含共享任务）的已删除任务，或永久清空集群回收站。")
              : t("Restore soft-deleted tasks or permanently empty this project’s trash.", "恢复软删除的任务，或永久清空此项目回收站。")}
          </p>
        </div>
        <Button variant="destructive" size="sm" disabled={!available || !items.length || empty.isPending} onClick={() => setConfirmEmpty(true)}>
          <Trash2 data-icon="inline-start" />
          {t("Empty trash", "清空回收站")}
        </Button>
      </header>
      <div className="min-w-0 flex-1 overflow-y-auto p-4">
        {!available && !trash.isLoading && (
          <Alert>
            <AlertTitle>{t("Trash is not available yet", "回收站暂不可用")}</AlertTitle>
            <AlertDescription>{t("Update Carbon before deleting here. This version will not pretend a permanent delete can be restored.", "请先更新 Carbon 再从这里删除；当前版本不会把永久删除伪装成可恢复操作。")}</AlertDescription>
          </Alert>
        )}
        {trash.isLoading ? (
          <p className="text-sm text-muted-foreground">{t("Loading trash…", "正在加载回收站…")}</p>
        ) : available && items.length ? (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <Input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder={t("Filter trashed tasks", "筛选回收站任务")} className="h-8 min-w-52 flex-1 text-sm" />
              <Select value={effectiveRestoreTarget} onValueChange={setRestoreTarget}>
                <SelectTrigger className="h-8 w-52 text-sm">{effectiveRestoreTarget === "original" ? t("Original project", "原项目") : effectiveRestoreTarget === "cluster" ? t("Cluster-wide", "集群范围") : projects.find((project) => project.id === effectiveRestoreTarget)?.name ?? effectiveRestoreTarget}</SelectTrigger>
                <SelectContent>
                  <SelectItem value="original">{t("Restore to original project", "恢复至原项目")}</SelectItem>
                  {isClusterScope && <SelectItem value="cluster">{t("Restore cluster-wide", "恢复至集群范围")}</SelectItem>}
                  {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <Button variant="outline" size="sm" disabled={!selectedVisible.length || restore.isPending} onClick={restoreSelected}>
                <RotateCcw data-icon="inline-start" />
                {t("Restore selected", "恢复所选")} ({selectedVisible.length})
              </Button>
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-9"><Checkbox checked={allVisibleSelected ? true : selectedVisible.length ? "indeterminate" : false} aria-label={t("Select visible trash entries", "选择可见回收站条目")} onCheckedChange={(checked) => setSelected(checked === true ? new Set(visible.map((item) => item.id)) : new Set())} /></TableHead>
                  <TableHead>{t("Task", "任务")}</TableHead>
                  <TableHead>{t("Deleted", "删除时间")}</TableHead>
                  <TableHead>{t("Project", "项目")}</TableHead>
                  <TableHead className="text-right">{t("Actions", "操作")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visible.map((item) => {
                  const trashedAt = item.trash?.trashed_at;
                  const projectId = item.project_id ?? item.trash?.original_project_id;
                  return (
                    <ContextMenu key={item.id}>
                      <ContextMenuTrigger asChild>
                        <tr
                          tabIndex={0}
                          data-carbon-context-surface
                          data-carbon-task-surface
                          aria-label={t("Trashed task {id}", "回收站任务 {id}", { id: item.id })}
                          className="border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted"
                        >
                          <TableCell><Checkbox checked={selected.has(item.id)} aria-label={t("Select {id}", "选择 {id}", { id: item.id })} onCheckedChange={(checked) => toggle(item.id, checked === true)} /></TableCell>
                          <TableCell>
                            <div className="font-medium">{item.title}</div>
                            <div className="font-mono text-xs text-muted-foreground">{item.id}</div>
                          </TableCell>
                          <TableCell>{trashedAt ? timeAgo(trashedAt) || trashedAt : "—"}</TableCell>
                          <TableCell>{projectId || "—"}</TableCell>
                          <TableCell className="text-right">
                            <Button size="sm" variant="outline" disabled={restore.isPending} onClick={() => restoreOne(item)}>
                              <RotateCcw data-icon="inline-start" />
                              {t("Restore", "恢复")}
                            </Button>
                          </TableCell>
                        </tr>
                      </ContextMenuTrigger>
                      <ContextMenuContent>
                        <ContextMenuLabel className="max-w-64 truncate font-mono text-[10px]">{item.id}</ContextMenuLabel>
                        <ContextMenuGroup>
                          <ContextMenuItem disabled={restore.isPending} onSelect={() => restoreOne(item)}>
                            <RotateCcw />
                            {t("Restore", "恢复")}
                          </ContextMenuItem>
                          <ContextMenuItem onSelect={() => void copyTaskId(item.id)}>
                            <ClipboardCopy />
                            {t("Copy task ID", "复制任务 ID")}
                          </ContextMenuItem>
                        </ContextMenuGroup>
                      </ContextMenuContent>
                    </ContextMenu>
                  );
                })}
              </TableBody>
            </Table>
            {!visible.length && <p className="text-sm text-muted-foreground">{t("No trashed tasks match this filter.", "没有匹配筛选条件的回收站任务。")}</p>}
          </div>
        ) : available ? (
          <p className="text-sm text-muted-foreground">{t("Trash is empty.", "回收站为空。")}</p>
        ) : null}
      </div>
      <ConfirmDeleteDialog
        open={confirmEmpty}
        onOpenChange={setConfirmEmpty}
        title={isClusterScope ? t("Permanently empty cluster trash?", "永久清空集群回收站？") : t("Permanently empty trash?", "永久清空回收站？")}
        description={isClusterScope
          ? t("This cannot be undone. Every restorable task record in this cluster, including shared work, will be permanently removed.", "此操作无法撤销。此集群中包括共享任务在内的所有可恢复任务记录都将被永久删除。")
          : t("This cannot be undone. Restorable task records in this project will be permanently removed.", "此操作无法撤销。此项目中可恢复的任务记录会被永久删除。")}
        confirmLabel={t("Empty permanently", "永久清空")}
        pending={empty.isPending}
        onConfirm={() => empty.mutate(undefined, { onSuccess: (result) => result.available && setConfirmEmpty(false) })}
      />
    </div>
  );
}
