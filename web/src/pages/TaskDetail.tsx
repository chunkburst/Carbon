import { useRef, useState } from "react";
import {
  ArrowLeft,
  AlertTriangle,
  Bot,
  Check as CheckMark,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleX,
  CornerLeftUp,
  FileText,
  GitBranch,
  GitCommit,
  Link2,
  Loader2,
  MoreHorizontal,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  UserPlus,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { agentPromptForTask, taskDeepLink } from "@/lib/connect";
import { currentActor } from "@/lib/identity";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Assignee } from "@/components/Assignee";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDefaultLayout } from "react-resizable-panels";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@/components/ui/resizable";
import { StatusIcon } from "@/components/StatusIcon";
import { Markdown } from "@/components/Markdown";
import { MarkdownEditor } from "@/components/MarkdownEditor";
import { LogView } from "@/components/LogView";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { PriorityIcon, PRIORITIES, priorityLabel } from "@/components/PriorityIcon";
import { SessionStatus } from "@/components/SessionStatus";
import { SessionTimeline } from "@/components/SessionTimeline";
import { CarbonTaskProperties } from "@/components/CarbonTaskProperties";
import { hasCarbonFeature } from "@/lib/carbon-api";
import {
  useAddNote,
  useAttest,
  useCarbonCapabilities,
  useClaimCarbonLease,
  useDeleteNote,
  useDeleteTask,
  useEditNote,
  useRunChecks,
  useRuns,
  useTask,
  useTaskGitContext,
  useTaskSessions,
  useTasks,
  useTransition,
  useUpdateTask,
} from "@/lib/queries";
import { cn, initials, statusLabel, timeAgo } from "@/lib/utils";
import { type Translate, useI18n } from "@/lib/i18n";
import type { ChangedFile, Check, GitCommit as GitCommitData, Run, SessionGitContext, Status, Task } from "@/lib/api";

const DETAIL_LAYOUT_ID = "carbon-detail-layout";
const LEGACY_DETAIL_LAYOUT_ID = "cairn-detail-layout";

const detailLayoutStorage = {
  getItem(storageKey: string): string | null {
    try {
      const current = localStorage.getItem(storageKey);
      if (current !== null) return current;

      const legacyKey = storageKey.replace(DETAIL_LAYOUT_ID, LEGACY_DETAIL_LAYOUT_ID);
      if (legacyKey === storageKey) return null;
      const legacy = localStorage.getItem(legacyKey);
      if (legacy === null) return null;
      try {
        localStorage.setItem(storageKey, legacy);
        localStorage.removeItem(legacyKey);
      } catch {
        // Keep the historical layout for this session if storage is restricted.
      }
      return legacy;
    } catch {
      return null;
    }
  },
  setItem(storageKey: string, value: string): void {
    localStorage.setItem(storageKey, value);
  },
};

export function TaskDetail({
  path,
  id,
  status,
  onBack,
  onOpenTask,
  onAddSubtask,
}: {
  path: string;
  id: string;
  status: Status;
  onBack: () => void;
  onOpenTask: (id: string) => void;
  onAddSubtask: (parentId: string) => void;
}) {
  const { t } = useI18n();
  const { data: task, isLoading } = useTask(path, id);
  const { data: runs } = useRuns(path, id);
  const { data: allTasks } = useTasks(path);
  const { data: sessions, isLoading: sessionsLoading } = useTaskSessions(path, id);
  const { data: gitContexts, isLoading: gitContextLoading } = useTaskGitContext(path, id);
  const leaseClaim = useClaimCarbonLease(path);
  const capabilities = useCarbonCapabilities(path);
  const transition = useTransition(path);
  const runChecks = useRunChecks(path);
  const attest = useAttest(path);
  const addNote = useAddNote(path);
  const update = useUpdateTask(path);
  const deleteTask = useDeleteTask(path);
  const editNote = useEditNote(path);
  const deleteNote = useDeleteNote(path);
  const [note, setNote] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmClaim, setConfirmClaim] = useState(false);
  const [claimReason, setClaimReason] = useState("");
  const [claimSubmitting, setClaimSubmitting] = useState(false);
  const [claimAwaitingApproval, setClaimAwaitingApproval] = useState(false);
  const claimSubmissionLock = useRef(false);
  const actor = currentActor() || t("current user", "当前用户");
  const leaseSupported = capabilities.data?.available === true && hasCarbonFeature(capabilities.data.data, "leases");
  const claimPending = leaseClaim.isPending || claimSubmitting;
  const claimChecking = capabilities.isLoading;
  const missingLeaseVersion = leaseSupported && !task?.version;

  const setClaimDialogOpen = (open: boolean) => {
    setConfirmClaim(open);
    if (open) setClaimAwaitingApproval(false);
    else setClaimReason("");
  };

  const confirmTaskClaim = () => {
    if (
      !task ||
      claimPending ||
      claimAwaitingApproval ||
      claimSubmissionLock.current ||
      claimChecking ||
      !leaseSupported ||
      !claimReason.trim() ||
      missingLeaseVersion
    ) {
      return;
    }

    claimSubmissionLock.current = true;
    setClaimSubmitting(true);
    const unlockClaim = () => {
      claimSubmissionLock.current = false;
      setClaimSubmitting(false);
    };

    leaseClaim.mutate(
      { id: task.id, reason: claimReason.trim(), expectedVersion: task.version },
      {
        onSuccess: (result) => {
          unlockClaim();
          if (!result.available) return;
          if (result.data.pending) setClaimAwaitingApproval(true);
          else setClaimDialogOpen(false);
        },
        onError: unlockClaim,
      },
    );
  };

  // Persist the resizable split (main / properties) to localStorage.
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: DETAIL_LAYOUT_ID,
    storage: detailLayoutStorage,
    panelIds: ["detail-main", "detail-props"],
  });

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b px-3">
        <Button variant="ghost" size="icon" aria-label={t("Back", "返回")} onClick={onBack}>
          <ArrowLeft />
        </Button>
        <div className="flex items-center gap-1.5 text-sm">
          <button onClick={onBack} className="font-medium hover:underline">
            {status.prefix}
          </button>
          <ChevronRight className="size-3.5 text-muted-foreground" />
          <span className="font-mono text-muted-foreground">{id}</span>
        </div>
        {task && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="ml-auto" aria-label={t("Task actions", "任务操作")}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => {
                  navigator.clipboard.writeText(taskDeepLink(path, task.id));
                  toast.success(t("Link copied", "链接已复制"));
                }}
              >
                <Link2 /> {t("Copy link", "复制链接")}
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => {
                  navigator.clipboard.writeText(
                    agentPromptForTask(task, path, currentActor(), window.location.origin),
                  );
                  toast.success(t("Agent prompt copied", "智能体提示词已复制"));
                }}
              >
                <Bot /> {t("Copy as agent prompt", "复制为智能体提示词")}
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onSelect={() => setConfirmDelete(true)}>
                <Trash2 /> {t("Delete task", "删除任务")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </header>

      {task && (
        <ConfirmDeleteDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t("Delete {id}?", "删除 {id}？", { id: task.id })}
          description={
            <>
              {t("This permanently deletes ", "这将永久删除")}
              <span className="font-medium">{task.title}</span>
              {t(". Tasks with sub-tasks or dependents can't be deleted until those are removed.", "。含有子任务或依赖者的任务，需先移除它们才能删除。")}
            </>
          }
          confirmLabel={t("Delete task", "删除任务")}
          pending={deleteTask.isPending}
          onConfirm={() => deleteTask.mutate(task.id, { onSuccess: onBack })}
        />
      )}

      {task && (
        <AlertDialog open={confirmClaim} onOpenChange={setClaimDialogOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("Claim this task?", "确认认领此任务？")}</AlertDialogTitle>
              <AlertDialogDescription>
                {claimChecking
                  ? t("Checking the available ownership safeguards…", "正在检查可用的负责人保护机制…")
                  : leaseSupported
                    ? t(
                        "This submits a lease request for {actor} with an optimistic version lock.",
                        "这会为 {actor} 提交带乐观版本锁的租约申请。",
                        { actor },
                      )
                    : t(
                        "This assigns {actor} as the task owner immediately.",
                        "确认后会立即将 {actor} 设置为该任务的负责人。",
                        { actor },
                      )}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="rounded-lg bg-muted p-3 text-sm">
              <p className="font-medium">{task.title}</p>
              <p className="mt-1 font-mono text-xs text-muted-foreground">{task.id}</p>
              <p className="mt-2 text-muted-foreground">
                {t("Actor: {actor}", "认领人：{actor}", { actor })}
              </p>
            </div>
            {leaseSupported ? (
              <>
                <FieldGroup className="gap-3">
                  <Field>
                    <FieldLabel htmlFor={`claim-reason-${task.id}`}>
                      {t("Ownership reason", "认领原因")}
                    </FieldLabel>
                    <Input
                      id={`claim-reason-${task.id}`}
                      value={claimReason}
                      onChange={(event) => setClaimReason(event.target.value)}
                      placeholder={t("Why are you claiming this task?", "说明为什么要认领此任务")}
                    />
                  </Field>
                </FieldGroup>
                {missingLeaseVersion && (
                  <Alert>
                    <RefreshCw />
                    <AlertDescription>
                      {t(
                        "This task has no current version. Refresh the task before requesting a lease so the optimistic lock can be enforced.",
                        "此任务缺少当前版本号。请先刷新任务，再申请租约，以确保乐观锁生效。",
                      )}
                    </AlertDescription>
                  </Alert>
                )}
              </>
            ) : <Alert><AlertDescription>{t("Direct legacy claiming is disabled because it cannot enforce a reason and optimistic version lock. Migrate this task to Carbon before claiming it.", "旧版直接认领无法强制记录原因和乐观版本锁，因此已禁用。请先将此任务迁移到 Carbon 再认领。")}</AlertDescription></Alert>}
            {claimAwaitingApproval && <Alert><AlertDescription>{t("This lease request is waiting for the current owner to approve it.", "此租约申请正在等待当前负责人审批。")}</AlertDescription></Alert>}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={claimPending}>{t("Cancel", "取消")}</AlertDialogCancel>
              <Button
                type="button"
                disabled={claimPending || claimAwaitingApproval || claimChecking || !leaseSupported || !claimReason.trim() || missingLeaseVersion}
                onClick={confirmTaskClaim}
              >
                {claimChecking
                  ? t("Checking…", "正在检查…")
                  : claimAwaitingApproval
                    ? t("Awaiting approval", "等待审批")
                  : claimPending
                    ? t("Claiming…", "正在认领…")
                    : t("Request lease", "申请租约")}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}

      {isLoading ? (
        <div className="mx-auto w-full max-w-2xl space-y-4 p-8">
          <Skeleton className="h-8 w-3/4" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : !task ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 text-center">
          <p className="text-sm text-muted-foreground">{t("Task {id} not found.", "未找到任务 {id}。", { id })}</p>
          <Button variant="outline" size="sm" onClick={onBack}>
            {t("Back to tasks", "返回任务列表")}
          </Button>
        </div>
      ) : (
        <ResizablePanelGroup
          orientation="horizontal"
          defaultLayout={defaultLayout}
          onLayoutChanged={onLayoutChanged}
          className="min-h-0 flex-1"
        >
          <ResizablePanel id="detail-main" defaultSize="68%" className="min-w-0">
            {/* Main column */}
            <main className="h-full overflow-y-auto">
            <div className="mx-auto max-w-2xl px-8 py-8">
              {task.parent && (
                <button
                  onClick={() => onOpenTask(task.parent!)}
                  className="mb-2 flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
                >
                  <CornerLeftUp className="size-3.5" />
                  <span className="font-mono">{task.parent}</span>
                  <span className="truncate">
                    {(allTasks ?? []).find((t) => t.id === task.parent)?.title}
                  </span>
                </button>
              )}
              <div className="mb-2 flex items-center gap-2">
                <StatusIcon status={task.status} closed={status.closed} initial={status.initial} />
                <span className="font-mono text-xs text-muted-foreground">{task.id}</span>
              </div>
              <EditableTitle
                title={task.title}
                saving={update.isPending}
                onSave={(title) => update.mutate({ id: task.id, fields: { title } })}
              />

              <SubTasks
                path={path}
                parentId={task.id}
                all={allTasks ?? []}
                status={status}
                onOpenTask={onOpenTask}
                onAddSubtask={() => onAddSubtask(task.id)}
              />

              <EditableBody
                body={task.body ?? ""}
                saving={update.isPending}
                onSave={(body) => update.mutate({ id: task.id, fields: { body } })}
              />

              <SessionTimeline
                sessions={sessions ?? []}
                executionState={task.executionState}
                loading={sessionsLoading}
              />

              <CodeContextPanel sessions={gitContexts ?? []} loading={gitContextLoading} />

              {/* Activity */}
              <section className="mt-10">
                <h2 className="text-xs font-medium text-muted-foreground">
                  {t("Activity", "动态")}
                </h2>
                <ol className="mt-3 space-y-3.5">
                  {(task.provenance ?? []).map((p, i) => (
                    <ActivityEntry
                      key={p.id || i}
                      entry={p}
                      onEdit={(text) =>
                        editNote.mutate({ id: task.id, text, note: p.id, index: i })
                      }
                      onDelete={() => deleteNote.mutate({ id: task.id, note: p.id, index: i })}
                      saving={editNote.isPending || deleteNote.isPending}
                    />
                  ))}
                </ol>

                <div className="mt-4 space-y-2">
                  <MarkdownEditor value={note} onChange={setNote} placeholder={t("Leave a note…", "留下备注…")} />
                  <div className="flex justify-end">
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={!note.trim() || addNote.isPending}
                      onClick={() =>
                        addNote.mutate(
                          { id: task.id, text: note.trim() },
                          { onSuccess: () => setNote("") },
                        )
                      }
                    >
                      {addNote.isPending && <Loader2 className="animate-spin" />}
                      {t("Add note", "添加备注")}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
            </main>
          </ResizablePanel>

          <ResizableHandle />

          {/* Properties panel */}
          <ResizablePanel
            id="detail-props"
            defaultSize="32%"
            minSize="24%"
            maxSize="50%"
            className="min-w-[260px]"
          >
            <aside className="h-full space-y-5 overflow-y-auto p-5">
            <Prop label={t("Status", "状态")}>
              <Select
                value={task.status}
                onValueChange={(to) => to !== task.status && transition.mutate({ id: task.id, to })}
              >
                <SelectTrigger className="h-8 w-full">
                  {transition.isPending ? (
                    <span className="flex items-center gap-2 text-sm">
                      <Loader2 className="size-3 animate-spin" />
                      {transition.variables &&
                      ((status.closed ?? []).includes(transition.variables.to) ||
                        status.review === transition.variables.to)
                        ? t("Running checks…", "正在运行检查…")
                        : t("Updating…", "正在更新…")}
                    </span>
                  ) : (
                    <SelectValue />
                  )}
                </SelectTrigger>
                <SelectContent>
                  {(status.states ?? []).map((s) => (
                    <SelectItem key={s} value={s}>
                      <span className="flex items-center gap-2">
                        <StatusIcon status={s} closed={status.closed} initial={status.initial} />
                        {localizedStatusLabel(s, t)}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Prop>

            <Prop label={t("Assignee", "负责人")}>
              {task.assignee ? (
                <div className="flex items-center gap-2 text-sm">
                  <Assignee actor={task.assignee} />
                  {task.assignee}
                </div>
              ) : (
                <Button
                  variant="outline"
                  className="w-full justify-start"
                  disabled={claimPending || claimChecking || !leaseSupported}
                  title={!claimChecking && !leaseSupported ? t("Safe claiming requires the Carbon lease workflow", "安全认领需要 Carbon 租约流程") : undefined}
                  onClick={() => setClaimDialogOpen(true)}
                >
                  {leaseClaim.isPending ? <Loader2 className="animate-spin" /> : <UserPlus />}
                  {t("Claim", "认领")}
                </Button>
              )}
            </Prop>

            <CarbonTaskProperties path={path} task={task} />

            {task.executionState && (
              <Prop label={t("Execution", "执行") }>
                <SessionStatus state={task.executionState} />
              </Prop>
            )}

            <Prop label={t("Ready", "就绪")}>
              {task.ready ? (
                <Badge className="bg-brand text-brand-foreground">{t("Ready", "就绪")}</Badge>
              ) : (
                <Badge variant="outline">{t("Blocked by deps", "被依赖项阻塞")}</Badge>
              )}
            </Prop>

            <Prop label={t("Priority", "优先级")}>
              <Select
                value={task.priority || "none"}
                onValueChange={(v) =>
                  update.mutate({ id: task.id, fields: { priority: v === "none" ? "" : v } })
                }
              >
                <SelectTrigger className="h-8 w-full">
                  <span className="flex items-center gap-2">
                    <PriorityIcon priority={task.priority} />
                    {priorityLabel(task.priority)}
                  </span>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">
                    <span className="flex items-center gap-2">
                      <PriorityIcon /> {t("No priority", "无优先级")}
                    </span>
                  </SelectItem>
                  {PRIORITIES.map((p) => (
                    <SelectItem key={p} value={p}>
                      <span className="flex items-center gap-2">
                        <PriorityIcon priority={p} /> {priorityLabel(p)}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Prop>

            <Prop label={t("Labels", "标签")}>
              <LabelsEditor
                labels={task.labels ?? []}
                onChange={(labels) => update.mutate({ id: task.id, fields: { labels } })}
              />
            </Prop>

            <Prop label={t("Parent", "父任务")}>
              <Select
                value={task.parent || "none"}
                onValueChange={(v) =>
                  update.mutate({ id: task.id, fields: { parent: v === "none" ? "" : v } })
                }
              >
                <SelectTrigger className="h-8 w-full">
                  <SelectValue placeholder={t("No parent", "无父任务")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("No parent", "无父任务")}</SelectItem>
                  {(allTasks ?? [])
                    .filter((t) => t.id !== task.id)
                    .map((t) => (
                      <SelectItem key={t.id} value={t.id}>
                        <span className="font-mono text-xs">{t.id}</span> {t.title}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </Prop>

            {task.deps && task.deps.length > 0 && (
              <Prop label={t("Depends on", "依赖于")}>
                <div className="flex flex-wrap gap-1.5">
                  {task.deps.map((d) => (
                    <Badge key={d} variant="outline" className="font-mono text-xs">
                      {d}
                    </Badge>
                  ))}
                </div>
              </Prop>
            )}

            <ChecksSection
              checks={task.checks ?? []}
              runs={runs ?? []}
              running={runChecks.isPending}
              saving={update.isPending}
              onRun={() => runChecks.mutate({ id: task.id })}
              onAttest={(i, pass) => attest.mutate({ id: task.id, index: i, pass })}
              attesting={attest.isPending}
              onSave={(checks) => update.mutate({ id: task.id, fields: { checks } })}
            />
            </aside>
          </ResizablePanel>
        </ResizablePanelGroup>
      )}
    </div>
  );
}

function Prop({
  label,
  action,
  children,
}: {
  label: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <h3 className="text-xs font-medium text-muted-foreground">{label}</h3>
        {action}
      </div>
      {children}
    </div>
  );
}

function CodeContextPanel({
  sessions,
  loading,
}: {
  sessions: SessionGitContext[];
  loading: boolean;
}) {
  const { t } = useI18n();
  if (loading) {
    return (
      <section className="mt-8">
        <h2 className="text-xs font-medium text-muted-foreground">{t("Code context", "代码上下文")}</h2>
        <div className="mt-3 space-y-2">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      </section>
    );
  }
  if (sessions.length === 0) return null;

  return (
    <section className="mt-8">
      <h2 className="text-xs font-medium text-muted-foreground">{t("Code context", "代码上下文")}</h2>
      <div className="mt-3 space-y-2">
        {sessions.map(({ session, context }) => (
          <article key={session.id} className="rounded-lg border bg-background px-3.5 py-3">
            <div className="flex min-w-0 items-center gap-2">
              <GitBranch className="size-3.5 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate text-sm font-medium">
                {context.branch || session.branch || t("Detached HEAD", "游离 HEAD")}
              </span>
              <Badge variant="outline" className="h-5 px-1.5 text-[10px] font-normal">
                {localizedStatusLabel(session.status, t)}
              </Badge>
            </div>

            <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
              {context.headStarted && <Ref label={t("start", "开始")} value={context.headStarted} />}
              {context.headFinished && <Ref label={t("finish", "完成")} value={context.headFinished} />}
              {!context.headFinished && context.currentHead && <Ref label={t("current", "当前")} value={context.currentHead} />}
            </div>

            {!context.available ? (
                <WarningList messages={[context.error || t("Git context is unavailable.", "Git 上下文不可用。")]} />
            ) : (
              <>
                {(context.warnings ?? []).length > 0 && (
                  <WarningList messages={(context.warnings ?? []).map((w) => w.message)} />
                )}
                <FileList title={t("Files changed", "已更改文件")} files={context.filesChanged ?? []} />
                <CommitList commits={context.commits ?? []} />
                <FileList title={t("Uncommitted", "未提交更改")} files={context.uncommitted ?? []} mutedEmpty />
              </>
            )}
          </article>
        ))}
      </div>
    </section>
  );
}

function Ref({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex items-center gap-1">
      <span>{label}</span>
      <span className="font-mono text-foreground">{shortSha(value)}</span>
    </span>
  );
}

function WarningList({ messages }: { messages: string[] }) {
  return (
    <div className="mt-2 space-y-1.5">
      {messages.map((message) => (
        <div
          key={message}
          className="flex gap-2 rounded-md border bg-muted/40 px-2.5 py-2 text-xs text-muted-foreground"
        >
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
          <span>{message}</span>
        </div>
      ))}
    </div>
  );
}

function FileList({
  title,
  files,
  mutedEmpty = false,
}: {
  title: string;
  files: ChangedFile[];
  mutedEmpty?: boolean;
}) {
  const { t } = useI18n();
  if (files.length === 0) {
    if (mutedEmpty) return null;
    return (
      <div className="mt-3 text-xs text-muted-foreground">
        <span className="font-medium">{title}</span>{t(": none", "：无")}
      </div>
    );
  }
  return (
    <div className="mt-3">
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <FileText className="size-3.5" />
        {title}
      </div>
      <div className="space-y-1">
        {files.slice(0, 8).map((file) => (
          <div
            key={`${file.status}:${file.oldPath ?? ""}:${file.path}`}
            className="flex min-w-0 items-center gap-2 text-xs"
          >
            <Badge variant="outline" className="h-5 w-8 justify-center px-0 font-mono text-[10px]">
              {file.status}
            </Badge>
            <span className="min-w-0 truncate font-mono text-muted-foreground">
              {file.oldPath ? `${file.oldPath} -> ${file.path}` : file.path}
            </span>
          </div>
        ))}
        {files.length > 8 && (
          <div className="text-xs text-muted-foreground">{t("+{count} more", "另有 {count} 个", { count: files.length - 8 })}</div>
        )}
      </div>
    </div>
  );
}

function CommitList({ commits }: { commits: GitCommitData[] }) {
  const { t } = useI18n();
  if (commits.length === 0) return null;
  return (
    <div className="mt-3">
      <div className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <GitCommit className="size-3.5" />
        {t("Commits", "提交记录")}
      </div>
      <div className="space-y-1">
        {commits.slice(0, 6).map((commit) => (
          <div key={commit.hash} className="flex min-w-0 items-center gap-2 text-xs">
            <span className="font-mono text-muted-foreground">{shortSha(commit.hash)}</span>
            <span className="min-w-0 truncate">{commit.subject}</span>
          </div>
        ))}
        {commits.length > 6 && (
          <div className="text-xs text-muted-foreground">{t("+{count} more", "另有 {count} 个", { count: commits.length - 6 })}</div>
        )}
      </div>
    </div>
  );
}

function shortSha(value: string) {
  return value.length > 7 ? value.slice(0, 7) : value;
}

// checkStatus maps a check's result (and whether a run is in flight) to its icon and pill
// styling. The left-edge icon makes the column scannable; the pill labels the state.
function checkStatus(result: string | undefined, running: boolean, t: Translate) {
  if (running) {
    return { Icon: Loader2, label: t("running", "运行中"), icon: "animate-spin text-muted-foreground", pill: "bg-muted text-muted-foreground" };
  }
  switch (result) {
    case "pass":
      return { Icon: CircleCheck, label: t("pass", "通过"), icon: "text-success", pill: "bg-success/10 text-success" };
    case "fail":
      return { Icon: CircleX, label: t("fail", "失败"), icon: "text-destructive", pill: "bg-destructive/10 text-destructive" };
    default:
      return { Icon: Circle, label: t("pending", "待处理"), icon: "text-muted-foreground/50", pill: "bg-muted text-muted-foreground" };
  }
}

function StatusPill({ className, children }: { className: string; children: React.ReactNode }) {
  return (
    <span className={cn("shrink-0 rounded-full px-1.5 py-0.5 text-[11px] font-medium", className)}>
      {children}
    </span>
  );
}

function CheckRow({
  check,
  run,
  running,
  onAttest,
  attesting,
}: {
  check: Check;
  run?: Run;
  running: boolean;
  onAttest: (pass: boolean) => void;
  attesting: boolean;
}) {
  const { t } = useI18n();
  const isManual = !check.cmd;
  const pending = (check.result ?? "pending") === "pending";
  const meta = checkStatus(check.result, running && !isManual, t);
  const expandable = !isManual && !!run;

  const lead = (
    <>
      <meta.Icon className={cn("size-4 shrink-0", meta.icon)} />
      <span className="min-w-0 flex-1 truncate text-sm">{check.desc}</span>
    </>
  );

  // Manual + pending: offer inline attest pass/fail (no command to run).
  if (isManual && pending) {
    return (
      <div className="flex items-center gap-2 px-3 py-2">
        {lead}
        <span className="flex shrink-0 items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon"
            className="size-6 text-success"
            aria-label={t("Attest pass", "确认通过")}
            disabled={attesting}
            onClick={() => onAttest(true)}
          >
            {attesting ? <Loader2 className="animate-spin" /> : <CheckMark className="size-3.5" />}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="size-6 text-destructive"
            aria-label={t("Attest fail", "确认失败")}
            disabled={attesting}
            onClick={() => onAttest(false)}
          >
            <X className="size-3.5" />
          </Button>
        </span>
      </div>
    );
  }

  // Command check with captured output: the whole row toggles an inline log panel.
  if (expandable) {
    return (
      <Collapsible>
        <CollapsibleTrigger className="group flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-foreground/[0.03]">
          {lead}
          <StatusPill className={meta.pill}>{meta.label}</StatusPill>
          <ChevronRight className="size-3.5 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-90" />
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="border-t bg-muted/30 px-3 py-2">
            <LogView run={run} />
          </div>
        </CollapsibleContent>
      </Collapsible>
    );
  }

  // Manual-passed/failed, or a command check not yet run: a plain status row.
  return (
    <div className="flex items-center gap-2 px-3 py-2">
      {lead}
      <StatusPill className={meta.pill}>{meta.label}</StatusPill>
    </div>
  );
}

function SubTasks({
  path,
  parentId,
  all,
  status,
  onOpenTask,
  onAddSubtask,
}: {
  path: string;
  parentId: string;
  all: Task[];
  status: Status;
  onOpenTask: (id: string) => void;
  onAddSubtask: () => void;
}) {
  const { t } = useI18n();
  const children = all.filter((t) => t.parent === parentId);
  const closed = new Set(status.closed ?? []);
  const done = children.filter((c) => closed.has(c.status)).length;
  const deleteTask = useDeleteTask(path);
  const [pendingDelete, setPendingDelete] = useState<Task | null>(null);

  return (
    <section className="mt-6">
      <div className="flex items-center justify-between">
        <h2 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
          {t("Sub-tasks", "子任务")}
          {children.length > 0 && (
            <span className="tabular-nums">
              {done}/{children.length}
            </span>
          )}
        </h2>
        <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={onAddSubtask}>
          <Plus className="size-3" /> {t("Add", "添加")}
        </Button>
      </div>
      {children.length > 0 && (
        <div className="mt-2 divide-y rounded-lg border">
          {children.map((c) => (
            <div
              key={c.id}
              className="group/sub flex w-full items-center gap-2 px-3 py-1.5 text-sm hover:bg-foreground/[0.04]"
            >
              <button
                onClick={() => onOpenTask(c.id)}
                className="flex min-w-0 flex-1 items-center gap-2 text-left"
              >
                <StatusIcon status={c.status} closed={status.closed} initial={status.initial} className="size-3.5" />
                <span className="w-16 shrink-0 font-mono text-xs text-muted-foreground">{c.id}</span>
                <span className="truncate">{c.title}</span>
              </button>
              <Button
                variant="ghost"
                size="icon"
                className="size-6 shrink-0 text-destructive opacity-0 transition-opacity group-hover/sub:opacity-100"
                aria-label={t("Delete {id}", "删除 {id}", { id: c.id })}
                onClick={() => setPendingDelete(c)}
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
      <ConfirmDeleteDialog
        open={!!pendingDelete}
        onOpenChange={(o) => !o && setPendingDelete(null)}
        title={pendingDelete ? t("Delete {id}?", "删除 {id}？", { id: pendingDelete.id }) : ""}
        description={
          <>
            {t("This permanently deletes ", "这将永久删除")}
            <span className="font-medium">{pendingDelete?.title}</span>
            {t(". Sub-tasks with their own children or dependents can't be deleted until those are removed.", "。含有子任务或依赖者的子任务，需先移除它们才能删除。")}
          </>
        }
        confirmLabel={t("Delete sub-task", "删除子任务")}
        pending={deleteTask.isPending}
        onConfirm={() =>
          pendingDelete &&
          deleteTask.mutate(pendingDelete.id, { onSuccess: () => setPendingDelete(null) })
        }
      />
    </section>
  );
}

// EditableTitle shows the task title as a heading; clicking it (or the pencil) swaps in an
// input. Enter/blur saves a non-empty change; Escape cancels.
function EditableTitle({
  title,
  saving,
  onSave,
}: {
  title: string;
  saving: boolean;
  onSave: (title: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(title);

  if (editing) {
    const commit = () => {
      const v = value.trim();
      if (v && v !== title) onSave(v);
      setEditing(false);
    };
    return (
      <Input
        autoFocus
        value={value}
        disabled={saving}
        onChange={(e) => setValue(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          } else if (e.key === "Escape") {
            setValue(title);
            setEditing(false);
          }
        }}
        className="!text-2xl h-auto py-1 font-semibold tracking-tight"
      />
    );
  }

  return (
    <h1
      className="group/title -mx-1 flex cursor-text items-start gap-2 rounded px-1 text-2xl font-semibold tracking-tight hover:bg-foreground/[0.03]"
      onClick={() => {
        setValue(title);
        setEditing(true);
      }}
    >
      <span className="min-w-0 flex-1">{title}</span>
      <Pencil className="mt-1.5 size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/title:opacity-100" />
    </h1>
  );
}

// EditableBody renders the markdown body with a hover "Edit" affordance; an empty body shows
// an "Add description" button. Editing swaps in the shared MarkdownEditor with save/cancel.
function EditableBody({
  body,
  saving,
  onSave,
}: {
  body: string;
  saving: boolean;
  onSave: (body: string) => void;
}) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const trimmed = body.trim();

  if (editing) {
    return (
      <div className="mt-5 space-y-2">
        <MarkdownEditor value={draft} onChange={setDraft} placeholder={t("Describe the task…", "描述任务…")} />
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>
            {t("Cancel", "取消")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={saving}
            onClick={() => {
              onSave(draft.trim());
              setEditing(false);
            }}
          >
            {saving && <Loader2 className="animate-spin" />}
            {t("Save", "保存")}
          </Button>
        </div>
      </div>
    );
  }

  const startEdit = () => {
    setDraft(body);
    setEditing(true);
  };

  if (!trimmed) {
    return (
      <Button variant="ghost" size="sm" className="mt-4 text-muted-foreground" onClick={startEdit}>
        <Plus className="size-3.5" /> {t("Add description", "添加描述")}
      </Button>
    );
  }

  return (
    <div className="group/body relative mt-5">
      <Button
        variant="ghost"
        size="icon"
        aria-label={t("Edit description", "编辑描述")}
        className="absolute right-0 top-0 size-6 opacity-0 transition-opacity group-hover/body:opacity-100"
        onClick={startEdit}
      >
        <Pencil className="size-3.5" />
      </Button>
      <Markdown>{trimmed}</Markdown>
    </div>
  );
}

// ChecksSection shows the task's checks (with run/attest) and toggles into a ChecksEditor for
// adding, removing, or modifying them. Editing replaces the whole list in one update; retained
// checks carry their result forward, new checks default to pending server-side.
function ChecksSection({
  checks,
  runs,
  running,
  saving,
  onRun,
  onAttest,
  attesting,
  onSave,
}: {
  checks: Check[];
  runs: Run[];
  running: boolean;
  saving: boolean;
  onRun: () => void;
  onAttest: (index: number, pass: boolean) => void;
  attesting: boolean;
  onSave: (checks: Check[]) => void;
}) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);

  if (editing) {
    return (
      <ChecksEditor
        checks={checks}
        saving={saving}
        onCancel={() => setEditing(false)}
        onSave={(next) => {
          onSave(next);
          setEditing(false);
        }}
      />
    );
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <h3 className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
          {t("Checks", "检查")}
          {checks.length > 0 && (
            <span className={cn("tabular-nums", checks.every((c) => c.result === "pass") && "text-success")}>
              {checks.filter((c) => c.result === "pass").length}/{checks.length}
            </span>
          )}
        </h3>
        <div className="flex items-center gap-1">
          {checks.some((c) => c.cmd) && (
            <Button
              variant="outline"
              size="sm"
              className="h-6 gap-1 px-2 text-xs"
              disabled={running}
              onClick={onRun}
            >
              {running ? <Loader2 className="size-3 animate-spin" /> : <Play className="size-3" />}
              {t("Run", "运行")}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-6 gap-1 px-2 text-xs"
            onClick={() => setEditing(true)}
          >
            <Pencil className="size-3" />
            {t("Edit", "编辑")}
          </Button>
        </div>
      </div>
      {checks.length > 0 ? (
        <div className="divide-y overflow-hidden rounded-lg border">
          {checks.map((c, i) => (
            <CheckRow
              key={i}
              check={c}
              run={c.cmd ? runs.find((r) => r.cmd === c.cmd) : undefined}
              running={running}
              onAttest={(pass) => onAttest(i, pass)}
              attesting={attesting}
            />
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("No checks.", "暂无检查项。")}</p>
      )}
    </div>
  );
}

// ChecksEditor edits the checks list in a local draft: each row has a description, an optional
// command (blank = a manual/attested check), and a remove button. Save emits the whole list.
function ChecksEditor({
  checks,
  saving,
  onCancel,
  onSave,
}: {
  checks: Check[];
  saving: boolean;
  onCancel: () => void;
  onSave: (checks: Check[]) => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<Check[]>(checks.map((c) => ({ ...c })));

  const setRow = (i: number, patch: Partial<Check>) =>
    setDraft((d) => d.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const removeRow = (i: number) => setDraft((d) => d.filter((_, j) => j !== i));
  const addRow = () => setDraft((d) => [...d, { desc: "", cmd: "" }]);

  const save = () => {
    // Drop blank-description rows; a command implies a non-manual check, else it's manual.
    const cleaned = draft
      .map((c) => ({ ...c, desc: c.desc.trim(), cmd: (c.cmd ?? "").trim() }))
      .filter((c) => c.desc)
      .map((c) => ({ ...c, type: c.cmd ? "" : "manual" }));
    onSave(cleaned);
  };

  return (
    <div className="space-y-2">
      <h3 className="text-xs font-medium text-muted-foreground">{t("Checks", "检查")}</h3>
      <div className="space-y-2 rounded-lg border p-2">
        {draft.length === 0 && (
          <p className="px-1 py-2 text-xs text-muted-foreground">{t("No checks. Add one below.", "暂无检查项。请在下方添加。")}</p>
        )}
        {draft.map((c, i) => (
          <div key={i} className="space-y-1.5 rounded-md border bg-muted/30 p-2">
            <div className="flex items-center gap-1.5">
              <Input
                value={c.desc}
                placeholder={t("What it verifies…", "要验证什么…")}
                onChange={(e) => setRow(i, { desc: e.target.value })}
                className="h-7 text-xs"
              />
              <Button
                variant="ghost"
                size="icon"
                className="size-7 shrink-0 text-destructive"
                aria-label={t("Remove check", "移除检查项")}
                onClick={() => removeRow(i)}
              >
                <X className="size-3.5" />
              </Button>
            </div>
            <Input
              value={c.cmd ?? ""}
              placeholder={t("Command (blank = manual check)", "命令（留空表示手动检查）")}
              onChange={(e) => setRow(i, { cmd: e.target.value })}
              className="h-7 font-mono text-xs"
            />
          </div>
        ))}
        <Button variant="ghost" size="sm" className="h-7 w-full justify-start text-xs" onClick={addRow}>
          <Plus className="size-3" /> {t("Add check", "添加检查项")}
        </Button>
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t("Cancel", "取消")}
        </Button>
        <Button size="sm" variant="secondary" disabled={saving} onClick={save}>
          {saving && <Loader2 className="animate-spin" />}
          {t("Save", "保存")}
        </Button>
      </div>
    </div>
  );
}

function LabelsEditor({
  labels,
  onChange,
}: {
  labels: string[];
  onChange: (labels: string[]) => void;
}) {
  const { t } = useI18n();
  const [input, setInput] = useState("");
  const add = () => {
    const v = input.trim();
    if (v && !labels.includes(v)) onChange([...labels, v]);
    setInput("");
  };
  return (
    <div className="space-y-1.5">
      {labels.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {labels.map((l) => (
            <Badge key={l} variant="secondary" className="gap-1 pr-1 text-xs font-normal">
              {l}
              <button
                aria-label={t("Remove {label}", "移除 {label}", { label: l })}
                onClick={() => onChange(labels.filter((x) => x !== l))}
                className="grid size-3.5 place-items-center rounded hover:bg-foreground/10"
              >
                <X className="size-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
      <Input
        value={input}
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            add();
          }
        }}
        placeholder={t("Add label…", "添加标签…")}
        className="h-7 text-xs"
      />
    </div>
  );
}

type ProvEntry = { id?: string; who: string; did: string; at: string; text?: string; editedAt?: string };

// ActivityEntry renders one provenance row. Entries that carry a note body are collapsible
// (collapsed by default, with a one-line preview) so long notes don't flood the log. Note
// entries (did === "note") can be edited or deleted inline; system entries are read-only.
function ActivityEntry({
  entry,
  onEdit,
  onDelete,
  saving,
}: {
  entry: ProvEntry;
  onEdit: (text: string) => void;
  onDelete: () => void;
  saving: boolean;
}) {
  const { t, locale } = useI18n();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const note = entry.text?.trim();
  const preview = note ? (note.split("\n").find((l) => l.trim()) ?? "") : "";
  const isNote = entry.did === "note";

  const startEdit = () => {
    setDraft(entry.text ?? "");
    setEditing(true);
    setOpen(true);
  };
  const save = () => {
    const v = draft.trim();
    if (v) onEdit(v);
    setEditing(false);
  };

  return (
    <li className="group flex gap-2">
      {/* chevron gutter — reserved on every row so avatars/names stay aligned */}
      <button
        aria-label={open ? t("Collapse note", "折叠备注") : t("Expand note", "展开备注")}
        onClick={note ? () => setOpen((o) => !o) : undefined}
        className={cn(
          "flex h-6 w-3.5 shrink-0 items-center justify-center",
          !note && "pointer-events-none",
        )}
      >
        {note && (
          <ChevronRight
            className={cn(
              "size-3.5 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
          />
        )}
      </button>
      <Avatar className="size-6 shrink-0">
        <AvatarFallback className="text-[9px]">{initials(entry.who)}</AvatarFallback>
      </Avatar>
      <div className="min-w-0 flex-1">
        <div
          className={cn(
            "flex min-h-6 items-center gap-1.5",
            note && !editing && "cursor-pointer select-none",
          )}
          onClick={note && !editing ? () => setOpen((o) => !o) : undefined}
        >
          <span className="shrink-0 text-sm font-medium">{entry.who}</span>
          <span className="shrink-0 text-xs text-muted-foreground">{entry.did}</span>
          {entry.editedAt && <span className="shrink-0 text-xs text-muted-foreground/70">{t("(edited)", "（已编辑）")}</span>}
          {note && !open && (
            <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground/80">{preview}</span>
          )}
          {isNote && !editing && (
            <span className="ml-auto flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
              <Button
                variant="ghost"
                size="icon"
                className="size-6"
                aria-label={t("Edit note", "编辑备注")}
                disabled={saving}
                onClick={(e) => {
                  e.stopPropagation();
                  startEdit();
                }}
              >
                <Pencil className="size-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-6 text-destructive"
                aria-label={t("Delete note", "删除备注")}
                disabled={saving}
                onClick={(e) => {
                  e.stopPropagation();
                  setConfirmDelete(true);
                }}
              >
                <Trash2 className="size-3.5" />
              </Button>
            </span>
          )}
          <span
            className={cn(
              "shrink-0 text-xs text-muted-foreground tabular-nums",
              isNote && !editing ? "ml-1.5 group-hover:ml-0" : "ml-auto",
            )}
            title={new Date(entry.at).toLocaleString(locale)}
          >
            {timeAgo(entry.at)}
          </span>
        </div>
        {editing ? (
          <div className="mt-1.5 space-y-2">
            <MarkdownEditor value={draft} onChange={setDraft} placeholder={t("Edit note…", "编辑备注…")} />
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>
                {t("Cancel", "取消")}
              </Button>
              <Button size="sm" variant="secondary" disabled={!draft.trim() || saving} onClick={save}>
                {saving && <Loader2 className="animate-spin" />}
                {t("Save", "保存")}
              </Button>
            </div>
          </div>
        ) : (
          note &&
          open && (
            <div className="mt-1.5 rounded-lg border bg-muted/40 px-3 py-2">
              <Markdown>{entry.text!}</Markdown>
            </div>
          )
        )}
      </div>

      <ConfirmDeleteDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("Delete note?", "删除备注？")}
        description={t("This permanently removes the note from the activity log.", "这会从动态记录中永久移除此备注。")}
        confirmLabel={t("Delete note", "删除备注")}
        pending={saving}
        onConfirm={onDelete}
      />
    </li>
  );
}

function localizedStatusLabel(status: string, t: Translate): string {
  const normalized = status.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const labels: Record<string, [string, string]> = {
    backlog: ["Backlog", "待办"],
    todo: ["To do", "待办"],
    to_do: ["To do", "待办"],
    in_progress: ["In progress", "进行中"],
    active: ["Active", "进行中"],
    running: ["Running", "运行中"],
    review: ["In review", "审核中"],
    awaiting_review: ["Awaiting review", "等待审核"],
    stalled: ["Stalled", "已停滞"],
    done: ["Done", "已完成"],
    completed: ["Completed", "已完成"],
    finished: ["Finished", "已完成"],
    closed: ["Closed", "已关闭"],
    blocked: ["Blocked", "受阻"],
  };
  const label = labels[normalized];
  return label ? t(...label) : statusLabel(status);
}
