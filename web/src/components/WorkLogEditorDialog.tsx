import { useEffect, useState } from "react";
import { FilePenLine, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import { visibilityDetail } from "@/components/WorkLogVisibilityBadge";
import { WORK_LOG_VISIBILITIES, workLogDraft, type WorkLog, type WorkLogDraft, type WorkLogVisibility } from "@/components/WorkLogTypes";
import { useI18n } from "@/lib/i18n";
import { timeAgo } from "@/lib/utils";

export type WorkLogEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  log?: WorkLog | null;
  defaultProjectId?: string;
  defaultTaskId?: string;
  pending?: boolean;
  onSubmit: (draft: WorkLogDraft, expectedVersion?: string) => Promise<unknown> | unknown;
};

export function WorkLogEditorDialog({
  open,
  onOpenChange,
  log,
  defaultProjectId,
  defaultTaskId,
  pending = false,
  onSubmit,
}: WorkLogEditorDialogProps) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<WorkLogDraft>(() => ({ ...workLogDraft(log), projectId: log?.projectId ?? defaultProjectId, taskId: log?.taskId ?? defaultTaskId }));
  const [tagsText, setTagsText] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    const next = { ...workLogDraft(log), projectId: log?.projectId ?? defaultProjectId, taskId: log?.taskId ?? defaultTaskId };
    setDraft(next);
    setTagsText((next.tags ?? []).join(", "));
    setError("");
  }, [defaultProjectId, defaultTaskId, log, open]);

  const set = <K extends keyof WorkLogDraft>(key: K, value: WorkLogDraft[K]) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = async () => {
    const tags = [...new Set(tagsText.split(",").map((tag) => tag.trim()).filter(Boolean))];
    const next: WorkLogDraft = {
      ...draft,
      title: draft.title.trim(),
      body: draft.body?.trim() || undefined,
      projectId: draft.projectId?.trim() || undefined,
      taskId: draft.taskId?.trim() || undefined,
      tags,
    };
    if (!next.title) {
      setError(t("Title is required.", "标题不能为空。"));
      return;
    }
    if (next.visibility === "project_public" && !next.projectId) {
      setError(t("Project visibility needs a project ID.", "项目可见性需要项目 ID。"));
      return;
    }
    setError("");
    try {
      await onSubmit(next, log?.version);
      onOpenChange(false);
    } catch {
      // The caller owns transport feedback. Keep the editor open for a retry.
    }
  };

  const visibility = visibilityDetail(draft.visibility, t);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><FilePenLine className="size-4 text-brand" />{log ? t("Edit Work Log", "编辑 Work Log") : t("New Work Log", "新建 Work Log")}</DialogTitle>
          <DialogDescription>
            {t("Work Logs are durable operational notes. The server stamps Worker and audit identity.", "Work Logs 是持久化的运营记录。服务端会盖章 Worker 与审计身份。")}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-3">
          <label className="grid gap-1.5 text-sm">
            <span>{t("Title", "标题")}</span>
            <Input autoFocus value={draft.title} maxLength={240} onChange={(event) => set("title", event.target.value)} placeholder={t("What changed or was learned?", "发生了什么变化或学到了什么？")} />
          </label>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1.5 text-sm">
              <span>{t("Visibility", "可见性")}</span>
              <Select value={draft.visibility} onValueChange={(value) => set("visibility", value as WorkLogVisibility)}>
                <SelectTrigger className="w-full">{visibility.label}</SelectTrigger>
                <SelectContent>
                  {WORK_LOG_VISIBILITIES.map((item) => <SelectItem key={item} value={item}>{visibilityDetail(item, t).label}</SelectItem>)}
                </SelectContent>
              </Select>
            </label>
            <div className="rounded-md border bg-muted/25 px-3 py-2 text-xs text-muted-foreground">
              <span className="mb-1 flex items-center gap-1.5 font-medium text-foreground"><ShieldCheck className="size-3.5" />{t("Access", "访问权限")}</span>
              <span>{visibility.description}</span>
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="grid gap-1.5 text-sm">
              <span>{t("Project ID", "项目 ID")}{draft.visibility === "project_public" && <span className="text-destructive"> *</span>}</span>
              <Input value={draft.projectId ?? ""} onChange={(event) => set("projectId", event.target.value)} placeholder={t("Optional except Project visibility", "项目可见性时必填")} />
            </label>
            <label className="grid gap-1.5 text-sm">
              <span>{t("Task ID", "任务 ID")}</span>
              <Input value={draft.taskId ?? ""} onChange={(event) => set("taskId", event.target.value)} placeholder={t("Optional linked task", "可选关联任务")} />
            </label>
          </div>

          <label className="grid gap-1.5 text-sm">
            <span>{t("Tags", "标签")}</span>
            <Input value={tagsText} maxLength={2048} onChange={(event) => setTagsText(event.target.value)} placeholder={t("Comma-separated, for example: handoff, decision", "用逗号分隔，例如：交接、决策")} />
          </label>

          <label className="grid gap-1.5 text-sm">
            <span>{t("Details", "详情")}</span>
            <Textarea value={draft.body ?? ""} maxLength={64 * 1024} className="min-h-36" onChange={(event) => set("body", event.target.value)} placeholder={t("Context, outcome, links, or next action…", "背景、结果、链接或下一步行动…")} />
          </label>

          {log && (
            <div className="grid gap-x-4 gap-y-1 border-t pt-3 text-xs text-muted-foreground sm:grid-cols-2">
              <span className="sm:col-span-2 text-[10px] font-medium tracking-wide uppercase">{t("Audit fields", "审计字段")}</span>
              <span className="flex items-center gap-1.5">{t("Worker", "Worker")}<WorkerIdentity actor={log.worker} compact /></span>
              <span>{t("Cluster", "集群")} <code className="font-mono">{log.clusterId}</code></span>
              <span>{t("Created", "创建")} {timeAgo(log.createdAt)} · {log.createdBy}</span>
              <span>{t("Updated", "更新")} {timeAgo(log.updatedAt)} · {log.updatedBy}</span>
              <span className="sm:col-span-2">{t("Version", "版本")} <code className="break-all font-mono">{log.version ?? "—"}</code></span>
            </div>
          )}
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>{t("Cancel", "取消")}</Button>
          <Button disabled={pending} onClick={() => void submit()}>{log ? t("Save Work Log", "保存 Work Log") : t("Create Work Log", "创建 Work Log")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
