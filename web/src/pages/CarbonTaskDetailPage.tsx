import { useLayoutEffect, useMemo, useState, type MouseEvent, type ReactElement, type ReactNode } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import {
  AlertTriangle,
  ArrowLeft,
  Bot,
  ClipboardCopy,
  ClipboardCheck,
  Check as CheckIcon,
  ChevronDown,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleX,
  ClockAlert,
  CornerLeftUp,
  ExternalLink,
  FileText,
  GitBranch,
  GitCommit,
  Link2,
  Loader2,
  MoreHorizontal,
  Pencil,
  Play,
  Plus,
  Trash2,
  UserPlus,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { Assignee } from "@/components/Assignee";
import { ActivityHealthBadge, UnknownActivityHealthBadge } from "@/components/ActivityHealthBadge";
import { LogView } from "@/components/LogView";
import { Markdown } from "@/components/Markdown";
import { MarkdownEditor } from "@/components/MarkdownEditor";
import { PriorityIcon, PRIORITIES, priorityLabel } from "@/components/PriorityIcon";
import { SessionStatus } from "@/components/SessionStatus";
import { StatusIcon } from "@/components/StatusIcon";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import type { AgentSession, Check, GitCommit as GitCommitData, Provenance, Run, SessionGitContext, Task, TaskEvidence } from "@/lib/api";
import type { CarbonHomeCluster, CarbonHomeProject, CarbonReviewCreate, CarbonScope } from "@/lib/carbon-api";
import { agentPromptForTask, carbonTaskDeepLink } from "@/lib/connect";
import { useIdentity } from "@/lib/identity";
import { useI18n } from "@/lib/i18n";
import {
  useApproveCarbonLease,
  useAddCarbonTaskNote,
  useAttestCarbonTask,
  useBulkCarbonMove,
  useCarbonTask,
  useCarbonConfig,
  useCarbonTaskGitContext,
  useCarbonTaskRuns,
  useCarbonTaskSessions,
  useCarbonTaskTypes,
  useCarbonTasks,
  useCarbonWorkerIdentities,
  useClaimCarbonLease,
  useCreateCarbonReview,
  useDeleteCarbonTaskNote,
  useEditCarbonTaskNote,
  usePatchCarbonTask,
  useReassignCarbonLease,
  useReleaseCarbonLease,
  useRunCarbonTaskChecks,
  useRenewCarbonLease,
  useTrashCarbonTasks,
  useTransitionCarbonTask,
} from "@/lib/queries";
import { carbonStorageKey } from "@/lib/storage-identity";
import { carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";
import { activityAction, activityBadgeClass, isActivityNote } from "@/lib/activity";
import { cn, timeAgo } from "@/lib/utils";
import { useWorkerAliasFormatter } from "@/lib/worker-aliases";

type CarbonTaskDetailPageProps = {
  home: string;
  homeId?: string;
  cluster?: CarbonHomeCluster;
  project: CarbonHomeProject;
  taskId: string;
  taskScope?: "cluster";
  taskProjectId?: string;
  suggestedActor?: string;
  onBack: () => void;
  onOpenTask: (clusterId: string | undefined, workspaceProjectId: string, taskId: string, taskProjectId?: string) => void;
  onOpenWorker?: (actor: string) => void;
};

function normalizeChecks(checks: Check[]): Check[] {
  return checks
    .map((check) => ({
      ...check,
      desc: check.desc.trim(),
      cmd: (check.cmd ?? "").trim(),
      type: (check.cmd ?? "").trim() ? "" : "manual",
      result: check.result || "pending",
    }))
    .filter((check) => check.desc);
}

function normalizeEvidence(evidence: TaskEvidence[]): TaskEvidence[] {
  return evidence
    .map((item) => {
      const kind = item.kind.trim();
      const value = item.value.trim();
      const label = item.label?.trim();
      const url = item.url?.trim();
      return {
        ...(item.id ? { id: item.id } : {}),
        kind,
        value,
        ...(label ? { label } : {}),
        ...(url ? { url } : {}),
        ...(item.id && item.createdAt ? { createdAt: item.createdAt } : {}),
        ...(item.id && item.createdBy ? { createdBy: item.createdBy } : {}),
      };
    })
    .filter((item) => item.kind && item.value);
}

function sameEvidence(left: TaskEvidence[], right: TaskEvidence[]): boolean {
  return JSON.stringify(normalizeEvidence(left)) === JSON.stringify(normalizeEvidence(right));
}

function detailStatusOptions(current?: string): string[] {
  return [...new Set(["backlog", "ready", "in_progress", "review", "done", current].filter(Boolean) as string[])];
}

function preserveNativeTextContextMenu(event: MouseEvent<HTMLElement>) {
  const target = event.target;
  if (target instanceof Element && target.closest("input, textarea, [contenteditable='true']")) {
    event.stopPropagation();
  }
}

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
 * Carbon intentionally keeps the 0.2 task-information hierarchy: a narrow task header,
 * a calm reading column, and a resizable properties rail. The data calls remain entirely
 * Carbon-scoped; this is a presentation restoration rather than a legacy transport path.
 */
export function CarbonTaskDetailPage({
  home,
  homeId,
  cluster,
  project,
  taskId,
  taskScope,
  taskProjectId,
  suggestedActor,
  onBack,
  onOpenTask,
  onOpenWorker,
}: CarbonTaskDetailPageProps) {
  const { t } = useI18n();
  const { actor } = useIdentity(suggestedActor);
  const formatWorker = useWorkerAliasFormatter();
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "carbon-task-detail-layout",
    storage: localStorage,
    panelIds: ["carbon-task-detail-main", "carbon-task-detail-props"],
  });
  const detailProjectId = taskScope === "cluster" ? undefined : taskProjectId?.trim() || project.id;
  const scope = useMemo<CarbonScope>(
    () => ({ home, clusterId: cluster?.id, ...(detailProjectId ? { projectId: detailProjectId } : {}) }),
    [cluster?.id, detailProjectId, home],
  );
  const storageKey = carbonStorageKey({
    home,
    homeId,
    clusterId: cluster?.id,
    projectId: detailProjectId ?? "__cluster__",
  });
  const detailQuery = useCarbonTask(scope, taskId);
  const configQuery = useCarbonConfig(scope);
  const task = detailQuery.data?.available ? detailQuery.data.data : undefined;
  const tasksQuery = useCarbonTasks(scope);
  const runsQuery = useCarbonTaskRuns(scope, taskId);
  const sessionsQuery = useCarbonTaskSessions(scope, taskId);
  const gitContextQuery = useCarbonTaskGitContext(scope, taskId);
  const typeScope = useMemo<CarbonScope>(() => cluster ? { home, clusterId: cluster.id } : scope, [cluster, home, scope]);
  const taskTypes = useCarbonTaskTypes(typeScope);
  const patch = usePatchCarbonTask(storageKey, scope);
  const transitionTask = useTransitionCarbonTask(storageKey, scope);
  const projectMove = useBulkCarbonMove(storageKey, scope);
  const trashTask = useTrashCarbonTasks(storageKey, scope);
  const runChecks = useRunCarbonTaskChecks(storageKey, scope);
  const attestCheck = useAttestCarbonTask(storageKey, scope);
  const addNote = useAddCarbonTaskNote(storageKey, scope);
  const editNote = useEditCarbonTaskNote(storageKey, scope);
  const deleteNote = useDeleteCarbonTaskNote(storageKey, scope);
  const claimLease = useClaimCarbonLease(storageKey, scope);
  const renewLease = useRenewCarbonLease(storageKey, scope);
  const releaseLease = useReleaseCarbonLease(storageKey, scope);
  const reassignLease = useReassignCarbonLease(storageKey, scope);
  const approveLease = useApproveCarbonLease(storageKey, scope);
  const identitiesQuery = useCarbonWorkerIdentities(scope);
  const createReview = useCreateCarbonReview(scope);

  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [blockerReason, setBlockerReason] = useState("");
  const [evidence, setEvidence] = useState<TaskEvidence[]>([]);
  const [moveProjectId, setMoveProjectId] = useState("");
  const [moveForce, setMoveForce] = useState(false);
  const [moveReason, setMoveReason] = useState("");
  const [assignee, setAssignee] = useState("");
  const [type, setType] = useState("");
  const [importance, setImportance] = useState("");
  const [forceReassign, setForceReassign] = useState(false);
  const [leaseReason, setLeaseReason] = useState("");
  const [editingTitle, setEditingTitle] = useState(false);
  const [editingBody, setEditingBody] = useState(false);
  const [note, setNote] = useState("");
  const [carbonExtensionOpen, setCarbonExtensionOpen] = useState(false);
  const [reviewOpen, setReviewOpen] = useState(false);

  const detailTypeOptions = useMemo(() => {
    const registered = taskTypes.data?.available ? taskTypes.data.data.types ?? [] : [];
    return [...new Set([
      "foundation",
      "library",
      "patch",
      "extension",
      "plugin",
      ...registered,
      task?.type ?? "",
    ].map((value) => value.trim().toLowerCase()).filter(Boolean))].sort();
  }, [task?.type, taskTypes.data]);

  const allTasks = useMemo(
    () => tasksQuery.data?.available ? tasksQuery.data.data.tasks ?? [] : [],
    [tasksQuery.data],
  );
  const subtasks = useMemo(
    () => allTasks.filter((candidate) => candidate.parent === task?.id),
    [allTasks, task?.id],
  );
  const openRelatedTask = (relatedTask: Task | undefined, relatedTaskId: string) => {
    const relatedProjectId = relatedTask?.projectId !== undefined
      ? relatedTask.projectId
      : scope.projectId ?? "";
    onOpenTask(cluster?.id, project.id, relatedTaskId, relatedProjectId);
  };
  const taskRuns = runsQuery.data?.available ? runsQuery.data.data : [];
  const taskSessions = sessionsQuery.data?.available ? sessionsQuery.data.data : [];
  const gitContexts = gitContextQuery.data?.available ? gitContextQuery.data.data : [];

  // Sync the draft before paint whenever the selected task or its server version changes.
  // This keeps route-to-route navigation from showing a previous task's draft for a frame.
  useLayoutEffect(() => {
    setTitle(task?.title ?? "");
    setBody(task?.body ?? "");
    setBlockerReason(task?.blockerReason ?? "");
    setEvidence((task?.evidence ?? []).map((item) => ({ ...item })));
    setMoveProjectId(task?.projectId ?? "__cluster_shared__");
    setMoveForce(false);
    setMoveReason("");
    setAssignee(task?.assignee ?? "");
    setType(task?.type ?? "foundation");
    setImportance(task?.importance ?? "normal");
    setLeaseReason("");
    setForceReassign(false);
    setEditingTitle(false);
    setEditingBody(false);
    setNote("");
    setCarbonExtensionOpen(false);
    setReviewOpen(false);
  }, [task]);

  const taskHeader = (
    <header className="flex h-11 shrink-0 items-center gap-2 border-b bg-background px-3">
      <Button variant="ghost" size="icon-xs" aria-label={t("Back to tasks", "返回任务")} onClick={onBack}>
        <ArrowLeft />
      </Button>
      <div className="flex min-w-0 items-center gap-1.5 text-sm">
        <button type="button" onClick={onBack} className="min-w-0 truncate font-medium underline-offset-4 hover:underline">
          {project.name}
        </button>
        <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate font-mono text-xs text-muted-foreground">{taskId}</span>
      </div>
      {task && (
        <div className="ml-auto flex items-center gap-1">
          {task.assignee && <Assignee actor={task.assignee} onOpenWorker={onOpenWorker} />}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label={t("Task actions", "任务操作")}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-44">
              <DropdownMenuItem
                onSelect={() => {
                  navigator.clipboard.writeText(carbonTaskDeepLink({
                    homeId,
                    clusterId: cluster?.id,
                    workspaceProjectId: project.id,
                    taskProjectId: detailProjectId,
                    taskScope,
                  }, task.id));
                  toast.success(t("Link copied", "链接已复制"));
                }}
              >
                <Link2 /> {t("Copy link", "复制链接")}
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => {
                  navigator.clipboard.writeText(
                    agentPromptForTask(task, project.source?.path ?? home, actor, window.location.origin),
                  );
                  toast.success(t("Agent prompt copied", "智能体提示词已复制"));
                }}
              >
                <Bot /> {t("Copy as agent prompt", "复制为智能体提示词")}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setReviewOpen(true)}>
                <ClipboardCheck /> {t("Request review", "发起审核")}
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                disabled={!task.version || trashTask.isPending}
                onSelect={() => {
                  if (!task.version) return;
                  trashTask.mutate(
                    {
                      ids: [task.id],
                      reason: "moved from task detail",
                      expectedVersions: { [task.id]: task.version },
                    },
                    { onSuccess: (result) => result.available && onBack() },
                  );
                }}
              >
                <Trash2 /> {t("Move to trash", "移入回收站")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}
    </header>
  );

  if (detailQuery.isLoading) {
    return (
      <div className="flex h-full min-w-0 flex-col bg-background">
        {taskHeader}
        <div className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-4 px-8 py-8">
          <Skeleton className="h-5 w-28" />
          <Skeleton className="h-9 w-3/4" />
          <Skeleton className="h-32 w-full" />
        </div>
      </div>
    );
  }

  if (!task) {
    return (
      <div className="flex h-full min-w-0 flex-col bg-background">
        {taskHeader}
        <div className="flex min-h-0 flex-1 items-center justify-center p-6">
          <Alert className="max-w-lg">
            <AlertTitle>{detailQuery.isError ? t("Task could not be loaded", "无法加载任务") : t("Task not found", "未找到任务")}</AlertTitle>
            <AlertDescription>
              {detailQuery.isError
                ? t("Check the current project scope and try again.", "请检查当前项目范围后重试。")
                : t("It may have been moved, deleted, or belongs to a different project.", "它可能已被移动、删除，或属于其他项目。")}
            </AlertDescription>
          </Alert>
        </div>
      </div>
    );
  }

  const missingVersion = !task.version;
  const leaseId = task.lease?.id;
  const leaseHeldByCurrentActor = Boolean(actor && task.lease?.holder === actor);
  const leasePending = claimLease.isPending || renewLease.isPending || releaseLease.isPending || reassignLease.isPending || approveLease.isPending;
  const requiresForce = Boolean(task.lease || task.assignee);
  const normalizedEvidence = normalizeEvidence(evidence);
  const normalizedType = type.trim().toLowerCase();
  const carbonPropertyFields = {
    ...(blockerReason !== (task.blockerReason ?? "") ? { blockerReason } : {}),
    ...(!sameEvidence(evidence, task.evidence ?? []) ? { evidence: normalizedEvidence } : {}),
    ...(normalizedType !== (task.type ?? "foundation") ? { type: normalizedType || "foundation" } : {}),
    ...(importance !== (task.importance ?? "normal") ? { importance } : {}),
  };
  const hasCarbonPropertyChanges = Object.keys(carbonPropertyFields).length > 0;
  const destinationProjectId = moveProjectId === "__cluster_shared__" ? "" : moveProjectId;
  const projectChanged = destinationProjectId !== (task.projectId ?? "");
  const statusOptions = detailStatusOptions(task.status);
  const statusInitial = statusOptions.includes("backlog") ? "backlog" : statusOptions[0];
  const statusClosed = statusOptions.filter((value) => ["done", "complete", "closed", "cancelled", "canceled"].includes(value));
  const scopeProjects = cluster?.projects ?? [project];
  const currentProject = task.projectId ? scopeProjects.find((candidate) => candidate.id === task.projectId) : undefined;
  const taskLinkFor = (candidate: Task) => {
    const candidateProjectId = candidate.projectId === undefined ? detailProjectId : candidate.projectId || undefined;
    return carbonTaskDeepLink({
      homeId,
      clusterId: cluster?.id,
      workspaceProjectId: project.id,
      taskProjectId: candidateProjectId,
      taskScope: candidateProjectId ? undefined : taskScope,
    }, candidate.id);
  };
  const taskPromptFor = (candidate: Task) => agentPromptForTask(
    candidate,
    project.source?.path ?? home,
    actor,
    window.location.origin,
  );
  const trashCurrentTask = () => {
    if (!task.version) return;
    trashTask.mutate(
      {
        ids: [task.id],
        reason: "moved from task detail",
        expectedVersions: { [task.id]: task.version },
      },
      { onSuccess: (result) => result.available && onBack() },
    );
  };

  const saveTitle = () => {
    if (missingVersion || patch.isPending || !title.trim() || title.trim() === task.title) {
      setEditingTitle(false);
      return;
    }
    patch.mutate(
      { id: task.id, fields: { title: title.trim(), expectedVersion: task.version } },
      { onSuccess: () => setEditingTitle(false) },
    );
  };
  const saveBody = () => {
    if (missingVersion || patch.isPending || body === (task.body ?? "")) {
      setEditingBody(false);
      return;
    }
    patch.mutate(
      { id: task.id, fields: { body, expectedVersion: task.version } },
      { onSuccess: () => setEditingBody(false) },
    );
  };
  const saveCarbonFields = () => {
    if (missingVersion || patch.isPending || !hasCarbonPropertyChanges) return;
    patch.mutate(
      { id: task.id, fields: { ...carbonPropertyFields, expectedVersion: task.version } },
      { onSuccess: () => setCarbonExtensionOpen(false) },
    );
  };
  const resetCarbonFields = () => {
    setBlockerReason(task.blockerReason ?? "");
    setEvidence((task.evidence ?? []).map((item) => ({ ...item })));
    setType(task.type ?? "foundation");
    setImportance(task.importance ?? "normal");
    setCarbonExtensionOpen(false);
  };
  const moveTask = () => {
    if (!task.version || !projectChanged || !moveForce || !moveReason.trim()) return;
    projectMove.mutate(
      {
        ids: [task.id],
        projectId: destinationProjectId,
        clusterWide: destinationProjectId === "",
        expectedVersions: { [task.id]: task.version },
        force: true,
        reason: moveReason.trim(),
      },
    );
  };
  const updateEvidence = (index: number, next: Partial<TaskEvidence>) => {
    setEvidence((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, ...next } : item));
  };
  const reassign = () => {
    if (missingVersion || !assignee.trim() || !leaseReason.trim() || (requiresForce && !forceReassign)) return;
    reassignLease.mutate({
      id: task.id,
      assignee: assignee.trim(),
      force: requiresForce,
      reason: leaseReason.trim(),
      expectedVersion: task.version,
    });
  };
  const notePending = addNote.isPending || editNote.isPending || deleteNote.isPending;
  const addActivityNote = () => {
    const text = note.trim();
    if (!text || notePending) return;
    addNote.mutate({ id: task.id, text }, { onSuccess: () => setNote("") });
  };

  return (
    <div className="flex h-full min-w-0 flex-col bg-background" onContextMenuCapture={preserveNativeTextContextMenu}>
      {taskHeader}
      {missingVersion && (
        <Alert className="m-3 shrink-0">
          <AlertTitle>{t("Refresh required", "需要刷新")}</AlertTitle>
          <AlertDescription>{t("This task has changed since it was loaded. Refresh it before editing.", "任务状态已发生变化，请刷新后再编辑。")}</AlertDescription>
        </Alert>
      )}
      <ResizablePanelGroup
        orientation="horizontal"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
        className="min-h-0 flex-1"
      >
        <ResizablePanel id="carbon-task-detail-main" defaultSize="68%" className="min-w-0">
          <main className="h-full overflow-y-auto overscroll-contain">
            <div className="mx-auto max-w-2xl px-6 py-8 sm:px-8">
              {task.parent?.trim() && (
                <Button
                  variant="ghost"
                  size="xs"
                  className="-ml-2 mb-3 max-w-full justify-start text-muted-foreground"
                  onClick={() => {
                    const parentId = task.parent!.trim();
                    openRelatedTask(allTasks.find((candidate) => candidate.id === parentId), parentId);
                  }}
                >
                  <CornerLeftUp data-icon="inline-start" />
                  <span className="truncate font-mono">{task.parent.trim()}</span>
                </Button>
              )}
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <TaskStatusPill status={task.status} label={statusLabel(task.status, t)} />
                <span className="font-mono text-xs text-muted-foreground">{task.id}</span>
              </div>

              {editingTitle ? (
                <div className="flex flex-col gap-3">
                  <Input
                    aria-label={t("Task title", "任务标题")}
                    autoFocus
                    className="h-auto px-0 py-1 text-[1.75rem] font-semibold leading-[1.22] tracking-[-0.025em] shadow-none sm:text-3xl"
                    value={title}
                    onChange={(event) => setTitle(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter" && !event.shiftKey) {
                        event.preventDefault();
                        saveTitle();
                      }
                      if (event.key === "Escape") {
                        setTitle(task.title);
                        setEditingTitle(false);
                      }
                    }}
                  />
                  <div className="flex justify-end gap-2">
                    <Button variant="ghost" size="sm" disabled={patch.isPending} onClick={() => {
                      setTitle(task.title);
                      setEditingTitle(false);
                    }}>
                      {t("Cancel", "取消")}
                    </Button>
                    <Button size="sm" disabled={missingVersion || patch.isPending || !title.trim()} onClick={saveTitle}>
                      {patch.isPending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <CheckIcon data-icon="inline-start" />}
                      {t("Save", "保存")}
                    </Button>
                  </div>
                </div>
              ) : (
                <TaskEntityContextMenu
                  task={task}
                  getHref={() => taskLinkFor(task)}
                  getAgentPrompt={() => taskPromptFor(task)}
                  statusLabel={(value) => statusLabel(value, t)}
                  transitioning={transitionTask.isPending}
                  onOpenTask={() => openRelatedTask(task, task.id)}
                  onTransition={(to) => transitionTask.mutate({ id: task.id, to })}
                  onTrash={trashCurrentTask}
                >
                  <div
                    tabIndex={0}
                    data-carbon-context-surface
                    data-carbon-task-surface
                    className="group relative -mx-3 rounded-lg px-3 py-1.5 transition-colors hover:bg-muted/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    aria-label={t("Task {id} actions", "任务 {id} 操作", { id: task.id })}
                  >
                    <button type="button" className="block w-full pr-8 text-left" onClick={() => setEditingTitle(true)}>
                      <h1 className="text-[1.75rem] font-semibold leading-[1.22] tracking-[-0.025em] sm:text-3xl">{title || task.title}</h1>
                    </button>
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      className="absolute top-1.5 right-1.5 opacity-0 transition-opacity group-hover:opacity-100 focus:opacity-100"
                      aria-label={t("Edit title", "编辑标题")}
                      onClick={() => setEditingTitle(true)}
                    >
                      <Pencil />
                    </Button>
                  </div>
                </TaskEntityContextMenu>
              )}

              {task.activityHealth === "stagnant" && (
                <Alert className="mt-5 border-warning/35 bg-warning/5">
                  <ClockAlert className="text-warning" />
                  <AlertTitle>{t("This task has gone quiet", "这项任务已经停滞")}</AlertTitle>
                  <AlertDescription className="leading-6">
                    {t(
                      "There has been no meaningful action for {period}. The task is still {status}; Carbon has not changed or closed it.",
                      "已经超过 {period} 没有有效动作。任务仍处于“{status}”，Carbon 没有替你改状态或关闭任务。",
                      {
                        period: formatStagnationPeriod(configQuery.data?.available ? configQuery.data.data.taskStagnationAfterSeconds : undefined, t),
                        status: statusLabel(task.status, t),
                      },
                    )}
                    {task.lastMeaningfulAt && (
                      <span className="mt-1 block text-xs text-muted-foreground">
                        {t("Last meaningful action: {time}", "上次有效动作：{time}", { time: formatBeijingDateTime(task.lastMeaningfulAt) })}
                      </span>
                    )}
                  </AlertDescription>
                </Alert>
              )}

              {subtasks.length > 0 && (
                <TaskSubtasks
                  tasks={subtasks}
                  title={t("Sub-tasks", "子任务")}
                  onOpenTask={(subtask) => openRelatedTask(subtask, subtask.id)}
                  taskHref={taskLinkFor}
                  taskPrompt={taskPromptFor}
                  statusLabel={(value) => statusLabel(value, t)}
                />
              )}

              <section className="mt-7">
                {editingBody ? (
                  <div className="flex flex-col gap-3">
                    <MarkdownEditor
                      value={body}
                      onChange={setBody}
                      placeholder={t("Describe the task…", "描述此任务…")}
                      minHeight="12rem"
                    />
                    <div className="flex justify-end gap-2">
                      <Button variant="ghost" size="sm" disabled={patch.isPending} onClick={() => {
                        setBody(task.body ?? "");
                        setEditingBody(false);
                      }}>
                        {t("Cancel", "取消")}
                      </Button>
                      <Button size="sm" disabled={missingVersion || patch.isPending} onClick={saveBody}>
                        {patch.isPending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <CheckIcon data-icon="inline-start" />}
                        {t("Save", "保存")}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <div
                    className="group relative -mx-3 min-h-24 cursor-text rounded-lg px-3 py-2.5 transition-colors hover:bg-muted/35"
                    onClick={(event) => {
                      if ((event.target as HTMLElement).closest("a, button")) return;
                      setEditingBody(true);
                    }}
                  >
                    {body ? <Markdown className="text-[0.9375rem] leading-7">{body}</Markdown> : <p className="pt-1 text-sm text-muted-foreground">{t("Add a description", "添加任务描述")}</p>}
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      className="absolute top-2 right-1 opacity-0 transition-opacity group-hover:opacity-100 focus:opacity-100"
                      aria-label={t("Edit description", "编辑描述")}
                      onClick={() => setEditingBody(true)}
                    >
                      <Pencil />
                    </Button>
                  </div>
                )}
              </section>

              <TaskSessionTimeline
                sessions={taskSessions}
                executionState={task.executionState}
                loading={sessionsQuery.isLoading}
                title={t("Agent sessions", "智能体会话")}
              />

              <TaskCodeContext
                contexts={gitContexts}
                loading={gitContextQuery.isLoading}
                title={t("Code context", "代码上下文")}
              />

              {(task.blockerReason ?? "").trim() && (
                <section className="mt-9 border-t pt-5">
                  <div className="flex items-start gap-3 border-l-2 border-foreground/25 pl-3">
                    <AlertTriangle className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center justify-between gap-3">
                        <h2 className="text-xs font-medium text-muted-foreground">{t("Blocker", "阻塞原因")}</h2>
                        <Button variant="ghost" size="xs" onClick={() => setCarbonExtensionOpen(true)}>{t("Edit", "编辑")}</Button>
                      </div>
                      <p className="mt-1.5 whitespace-pre-wrap text-sm leading-6 text-foreground/80">{task.blockerReason}</p>
                    </div>
                  </div>
                </section>
              )}

              {(task.evidence ?? []).length > 0 && (
                <TaskEvidenceTimeline
                  evidence={task.evidence ?? []}
                  title={t("Delivery records", "交付凭据")}
                  onEdit={() => setCarbonExtensionOpen(true)}
                  editLabel={t("Edit", "编辑")}
                  formatWorker={formatWorker}
                />
              )}

              <section className="mt-10 border-t pt-5">
                <h2 className="text-sm font-medium text-muted-foreground">{t("Activity", "动态")}</h2>
                <TaskActivityTimeline
                  entries={task.provenance ?? []}
                  onOpenWorker={onOpenWorker}
                  saving={notePending}
                  onEdit={(index, text, noteId) => editNote.mutate({ id: task.id, text, note: noteId, index })}
                  onDelete={(index, noteId) => deleteNote.mutate({ id: task.id, note: noteId, index })}
                  emptyLabel={t("No activity yet.", "暂无动态。")}
                />
                <div className="mt-5 border-t border-dashed pt-4">
                  <MarkdownEditor value={note} onChange={setNote} placeholder={t("Leave a note…", "留下备注…")} />
                  <div className="mt-2 flex justify-end">
                    <Button size="sm" variant="secondary" disabled={!note.trim() || notePending} onClick={addActivityNote}>
                      {addNote.isPending && <Loader2 data-icon="inline-start" className="animate-spin" />}
                      {t("Add note", "添加备注")}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
          </main>
        </ResizablePanel>

        <ResizableHandle />

        <ResizablePanel
          id="carbon-task-detail-props"
          defaultSize="32%"
          minSize="24%"
          maxSize="50%"
          className="min-w-[260px]"
        >
          <aside className="h-full space-y-5 overflow-y-auto p-5">
            <Prop label={t("Status", "状态")}>
              <Select
                value={task.status}
                disabled={missingVersion || transitionTask.isPending}
                onValueChange={(to) => to !== task.status && transitionTask.mutate({ id: task.id, to })}
              >
                <SelectTrigger className="h-8 w-full">
                  {transitionTask.isPending ? (
                    <span className="flex items-center gap-2 text-sm">
                      <Loader2 className="size-3 animate-spin" />
                      {transitionTask.variables &&
                      (statusClosed.includes(transitionTask.variables.to) || transitionTask.variables.to === "review")
                        ? t("Running checks…", "正在运行检查…")
                        : t("Updating…", "正在更新…")}
                    </span>
                  ) : (
                    <SelectValue />
                  )}
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {statusOptions.map((value) => (
                      <SelectItem key={value} value={value}>
                        <span className="flex items-center gap-2">
                          <StatusIcon status={value} closed={statusClosed} initial={statusInitial} />
                          {statusLabel(value, t)}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Prop>

            <Prop label={t("Assignee", "负责人")}>
              {task.assignee ? (
                <div className="flex items-center gap-2 text-sm">
                  <Assignee actor={task.assignee} onOpenWorker={onOpenWorker} />
                  {formatWorker(task.assignee)}
                </div>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  className="w-full justify-start"
                  disabled={missingVersion}
                  onClick={() => {
                    setCarbonExtensionOpen(true);
                    requestAnimationFrame(() => {
                      document.getElementById(`carbon-task-lease-reason-${task.id}`)?.focus();
                    });
                  }}
                >
                  <UserPlus />
                  {t("Claim", "认领")}
                </Button>
              )}
            </Prop>

            {task.executionState && (
              <Prop label={t("Execution", "执行状态")}>
                <SessionStatus state={task.executionState} />
              </Prop>
            )}

            <Prop label={t("Activity health", "活动健康")}>
              <div className="grid gap-1.5">
                {task.activityHealth === "fresh" ? (
                  <Badge variant="outline" className="text-success">{t("Active recently", "近期有进展")}</Badge>
                ) : (
                  <>
                    <ActivityHealthBadge task={task} thresholdSeconds={configQuery.data?.available ? configQuery.data.data.taskStagnationAfterSeconds : undefined} />
                    <UnknownActivityHealthBadge task={task} />
                  </>
                )}
                {task.lastMeaningfulAt && <span className="text-[11px] leading-4 text-muted-foreground">{t("Last meaningful action {time}", "上次有效动作在 {time}", { time: timeAgo(task.lastMeaningfulAt) })}</span>}
              </div>
            </Prop>

            <Prop label={t("Ready", "就绪")}>
              {task.ready ? (
                <Badge className="bg-brand text-brand-foreground">{t("Ready", "已就绪")}</Badge>
              ) : (
                <Badge variant="outline">{t("Waiting for prerequisite tasks", "等待前置任务")}</Badge>
              )}
            </Prop>

            <Prop label={t("Priority", "优先级")}>
              <Select
                value={task.priority || "none"}
                disabled={missingVersion || patch.isPending}
                onValueChange={(value) =>
                  patch.mutate({
                    id: task.id,
                    fields: { priority: value === "none" ? "" : value, expectedVersion: task.version },
                  })
                }
              >
                <SelectTrigger className="h-8 w-full">
                  <span className="flex items-center gap-2">
                    <PriorityIcon priority={task.priority} />
                    {priorityLabel(task.priority)}
                  </span>
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="none">
                      <span className="flex items-center gap-2">
                        <PriorityIcon /> {t("No priority", "无优先级")}
                      </span>
                    </SelectItem>
                    {PRIORITIES.map((value) => (
                      <SelectItem key={value} value={value}>
                        <span className="flex items-center gap-2">
                          <PriorityIcon priority={value} /> {priorityLabel(value)}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Prop>

            <Prop label={t("Labels", "标签")}>
              <LabelsEditor
                labels={task.labels ?? []}
                onChange={(labels) => {
                  if (missingVersion) return;
                  patch.mutate({ id: task.id, fields: { labels, expectedVersion: task.version } });
                }}
              />
            </Prop>

            <Prop label={t("Parent", "父任务")}>
              <Select
                value={task.parent || "none"}
                disabled={missingVersion || patch.isPending}
                onValueChange={(value) =>
                  patch.mutate({
                    id: task.id,
                    fields: { parent: value === "none" ? "" : value, expectedVersion: task.version },
                  })
                }
              >
                <SelectTrigger className="h-8 w-full">
                  <SelectValue placeholder={t("No parent", "无父任务")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="none">{t("No parent", "无父任务")}</SelectItem>
                    {allTasks
                      .filter((candidate) => candidate.id !== task.id)
                      .map((candidate) => (
                        <SelectItem key={candidate.id} value={candidate.id}>
                          <span className="font-mono text-xs">{candidate.id}</span> {candidate.title}
                        </SelectItem>
                      ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Prop>

            {task.deps && task.deps.length > 0 && (
              <Prop label={t("Depends on", "依赖于")}>
                <div className="flex flex-wrap gap-1.5">
                  {task.deps.map((dependency) => (
                    <Badge key={dependency} variant="outline" className="font-mono text-xs">
                      {dependency}
                    </Badge>
                  ))}
                </div>
              </Prop>
            )}

            <ChecksSection
              checks={task.checks ?? []}
              runs={taskRuns}
              running={runChecks.isPending}
              saving={patch.isPending}
              onRun={() => runChecks.mutate({ id: task.id })}
              onAttest={(index, pass) => attestCheck.mutate({ id: task.id, index, pass })}
              attesting={attestCheck.isPending}
              onSave={(next) => {
                if (missingVersion) return;
                patch.mutate({
                  id: task.id,
                  fields: { checks: normalizeChecks(next), expectedVersion: task.version },
                });
              }}
            />

            <CarbonExtension open={carbonExtensionOpen} onOpenChange={setCarbonExtensionOpen}>
              <FieldGroup className="gap-3">
                <Field>
                  <FieldLabel>{t("Type", "类型")}</FieldLabel>
                  <Select value={type} onValueChange={setType} disabled={missingVersion || patch.isPending}>
                    <SelectTrigger className="h-8 w-full">
                      <SelectValue placeholder={t("Choose type", "选择类型")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {detailTypeOptions.map((value) => (
                          <SelectItem key={value} value={value}>{carbonTaskTypeLabel(value, t)}</SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </Field>
                <Field>
                  <FieldLabel>{t("Importance", "重要性")}</FieldLabel>
                  <Select value={importance} onValueChange={setImportance} disabled={missingVersion || patch.isPending}>
                    <SelectTrigger className="h-8 w-full">
                      <SelectValue placeholder={t("Choose importance", "选择重要性")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {["core", "important", "normal", "optional", "experimental"].map((value) => (
                          <SelectItem key={value} value={value}>{carbonImportanceLabel(value, t)}</SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </Field>
                <Field>
                  <FieldLabel htmlFor={`carbon-task-blocker-${task.id}`}>{t("Blocker reason", "阻塞原因")}</FieldLabel>
                  <Textarea
                    id={`carbon-task-blocker-${task.id}`}
                    value={blockerReason}
                    maxLength={4096}
                    disabled={missingVersion || patch.isPending}
                    onChange={(event) => setBlockerReason(event.target.value)}
                    placeholder={t("Why is this task blocked?", "此任务为什么被阻塞？")}
                  />
                </Field>
              </FieldGroup>

              <EvidenceEditor evidence={evidence} onChange={setEvidence} onUpdate={updateEvidence} />

              <div className="mt-4 flex justify-end gap-2">
                <Button variant="ghost" size="sm" disabled={patch.isPending} onClick={resetCarbonFields}>
                  {t("Cancel", "取消")}
                </Button>
                <Button size="sm" disabled={missingVersion || patch.isPending || !hasCarbonPropertyChanges} onClick={saveCarbonFields}>
                  {patch.isPending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <CheckIcon data-icon="inline-start" />}
                  {t("Save changes", "保存更改")}
                </Button>
              </div>

              <section className="mt-5 border-t pt-4">
                <h3 className="text-sm font-medium">{t("Project", "项目")}</h3>
                {cluster ? (
                  <>
                    <FieldGroup className="mt-3 gap-3">
                      <Field>
                        <FieldLabel>{t("Move to", "移动到")}</FieldLabel>
                        <Select value={moveProjectId} onValueChange={setMoveProjectId}>
                          <SelectTrigger className="h-8 w-full">
                            <SelectValue placeholder={t("Choose project", "选择项目")} />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectGroup>
                              <SelectItem value="__cluster_shared__">{t("Cluster shared pool", "集群共享任务池")}</SelectItem>
                              {scopeProjects.map((candidate) => (
                                <SelectItem key={candidate.id} value={candidate.id}>{candidate.name}</SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                      </Field>
                      {projectChanged && (
                        <Field>
                          <FieldLabel htmlFor={`carbon-task-move-reason-${task.id}`}>{t("Reason", "原因")}</FieldLabel>
                          <Input
                            id={`carbon-task-move-reason-${task.id}`}
                            value={moveReason}
                            onChange={(event) => setMoveReason(event.target.value)}
                            placeholder={t("Reason for project move", "说明项目移动原因（必填）")}
                          />
                        </Field>
                      )}
                      <Field orientation="horizontal">
                        <Checkbox
                          id={`carbon-task-move-force-${task.id}`}
                          checked={moveForce}
                          onCheckedChange={(checked) => setMoveForce(checked === true)}
                        />
                        <FieldLabel htmlFor={`carbon-task-move-force-${task.id}`}>
                          {t("Confirm forced project move", "确认强制移动项目")}
                        </FieldLabel>
                      </Field>
                    </FieldGroup>
                    <div className="mt-4 flex justify-end">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={missingVersion || projectMove.isPending || !projectChanged || !moveForce || !moveReason.trim()}
                        onClick={moveTask}
                      >
                        {projectMove.isPending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : null}
                        {t("Move task", "移动任务")}
                      </Button>
                    </div>
                  </>
                ) : (
                  <p className="mt-1 text-sm text-muted-foreground">
                    {currentProject?.name ?? (task.projectId === "" ? t("Cluster shared", "集群共享") : project.name)}
                  </p>
                )}
              </section>

              <section className="mt-5 border-t pt-4">
                <h3 className="text-sm font-medium">{t("Handoff and owner", "接手与负责人")}</h3>
                <div className="mt-3">
                  <LeaseEditor
                    taskId={task.id}
                    version={task.version}
                    assignee={assignee}
                    onAssigneeChange={setAssignee}
                    lease={task.lease}
                    pendingClaims={task.pendingClaims ?? []}
                    actor={actor}
                    leaseReason={leaseReason}
                    onLeaseReasonChange={setLeaseReason}
                    forceReassign={forceReassign}
                    onForceReassignChange={setForceReassign}
                    pending={leasePending}
                    requiresForce={requiresForce}
                    formatWorker={formatWorker}
                    onOpenWorker={onOpenWorker}
                    onClaim={() => claimLease.mutate({ id: task.id, reason: leaseReason.trim(), expectedVersion: task.version })}
                    onRenew={() => leaseId && renewLease.mutate({ id: task.id, leaseId, expectedVersion: task.version })}
                    onRelease={() => leaseId && releaseLease.mutate({ id: task.id, leaseId, reason: leaseReason.trim(), expectedVersion: task.version })}
                    onReassign={reassign}
                    onApprove={(requestId, approve) => approveLease.mutate({
                      id: task.id,
                      requestId,
                      approve,
                      reason: leaseReason.trim(),
                      expectedVersion: task.version,
                    })}
                    heldByCurrentActor={leaseHeldByCurrentActor}
                  />
                </div>
              </section>

              {task.conflict && task.conflict.state !== "resolved" && (
                <Alert variant="destructive" className="mt-5">
                  <AlertTitle>{t("Task conflict", "任务冲突")}</AlertTitle>
                  <AlertDescription>
                    {task.conflict.message || t("This task needs conflict resolution before it can be changed safely.", "此任务需要先解决冲突，才能安全更改。")}
                  </AlertDescription>
                </Alert>
              )}
            </CarbonExtension>

          </aside>
        </ResizablePanel>
      </ResizablePanelGroup>
      <RequestReviewDialog
        open={reviewOpen}
        onOpenChange={setReviewOpen}
        task={task}
        identities={identitiesQuery.data?.available ? identitiesQuery.data.data.records ?? [] : []}
        identitiesAvailable={identitiesQuery.data?.available === true}
        pending={createReview.isPending}
        onSubmit={(input) => createReview.mutate(input, { onSuccess: (result) => result.available && setReviewOpen(false) })}
      />
    </div>
  );
}

function RequestReviewDialog({
  open,
  onOpenChange,
  task,
  identities,
  identitiesAvailable,
  pending,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: Task;
  identities: Array<{ actor: string; roles: string[]; role: string }>;
  identitiesAvailable: boolean;
  pending: boolean;
  onSubmit: (input: CarbonReviewCreate) => void;
}) {
  const { t } = useI18n();
  const [target, setTarget] = useState("plan");
  const [reviewer, setReviewer] = useState("");
  const manualChecks = useMemo(
    () => (task.checks ?? []).map((check, index) => ({ check, index })).filter(({ check }) => !check.cmd?.trim()),
    [task.checks],
  );
  const reviewers = useMemo(
    () => identities.filter((identity) => identity.roles?.includes("reviewer") || identity.role === "reviewer" || identity.role === "审核者"),
    [identities],
  );
  const canSubmit = Boolean(reviewer && (target === "plan" || target.startsWith("manual:")) && !pending);

  const reset = () => {
    setTarget("plan");
    setReviewer("");
  };
  const submit = () => {
    if (!canSubmit) return;
    if (target === "plan") {
      onSubmit({ targetKind: "plan", targetId: task.id, taskId: task.id, reviewerActor: reviewer });
      return;
    }
    const index = Number(target.slice("manual:".length));
    if (!Number.isInteger(index)) return;
    onSubmit({ targetKind: "manual_check", targetId: `${task.id}#check:${index}`, taskId: task.id, checkId: String(index), reviewerActor: reviewer });
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) reset(); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("Request a review", "发起审核")}</DialogTitle>
          <DialogDescription>{t("Choose the exact thing to check and a Worker whose identity includes Reviewer. This is separate from task ownership and lease approval.", "选择要检查的具体对象，并指定带有“审核者”身份的 Worker；这不会改变任务负责人，也不是认领审批。")}</DialogDescription>
        </DialogHeader>
        <FieldGroup className="gap-3">
          <Field>
            <FieldLabel>{t("What should be reviewed?", "审核什么？")}</FieldLabel>
            <Select value={target} onValueChange={setTarget}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="plan">{t("Task plan and description", "任务计划与说明")}</SelectItem>
                {manualChecks.map(({ check, index }) => <SelectItem key={index} value={`manual:${index}`}>{t("Manual check", "人工检查")}：{check.desc}</SelectItem>)}
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel>{t("Reviewer", "审核者")}</FieldLabel>
            <Select value={reviewer} onValueChange={setReviewer} disabled={!reviewers.length}>
              <SelectTrigger><SelectValue placeholder={t("Choose a reviewer", "选择审核者")} /></SelectTrigger>
              <SelectContent>{reviewers.map((identity) => <SelectItem key={identity.actor} value={identity.actor}>{identity.actor}</SelectItem>)}</SelectContent>
            </Select>
            {!identitiesAvailable ? <p className="text-xs text-muted-foreground">{t("Worker identities are unavailable in this Carbon version.", "当前 Carbon 版本暂时无法读取 Worker 身份。")}</p> : reviewers.length === 0 ? <p className="text-xs text-muted-foreground">{t("No Worker in this project has the Reviewer identity yet. Add it from Agent team → Worker actions.", "这个项目还没有带“审核者”身份的 Worker；请从“智能体团队 → Worker 操作”添加。")}</p> : null}
          </Field>
        </FieldGroup>
        <DialogFooter><Button variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>{t("Cancel", "取消")}</Button><Button disabled={!canSubmit} onClick={submit}><ClipboardCheck />{t("Send for review", "提交审核")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TaskSubtasks({
  tasks,
  title,
  onOpenTask,
  taskHref,
  taskPrompt,
  statusLabel,
}: {
  tasks: Task[];
  title: string;
  onOpenTask: (task: Task) => void;
  taskHref: (task: Task) => string;
  taskPrompt: (task: Task) => string;
  statusLabel: (status: string) => string;
}) {
  return (
    <section className="mt-7 border-t pt-5">
      <h2 className="text-xs font-medium text-muted-foreground">{title}</h2>
      <ul className="mt-2.5 divide-y divide-border/70">
        {tasks.map((subtask) => (
          <TaskEntityContextMenu
            key={subtask.id}
            task={subtask}
            getHref={() => taskHref(subtask)}
            getAgentPrompt={() => taskPrompt(subtask)}
            statusLabel={statusLabel}
            onOpenTask={() => onOpenTask(subtask)}
          >
            <li
              tabIndex={0}
              data-carbon-context-surface
              data-carbon-task-surface
              className="focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label={`${subtask.title} (${subtask.id})`}
            >
              <button
                type="button"
                className="group flex w-full min-w-0 items-center gap-2 py-2.5 text-left first:pt-0 last:pb-0"
                onClick={() => onOpenTask(subtask)}
              >
                <TaskStatusPill status={subtask.status} label={statusLabel(subtask.status)} />
                <span className="min-w-0 flex-1 truncate text-sm group-hover:underline group-hover:underline-offset-4">{subtask.title}</span>
                <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{subtask.id}</span>
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              </button>
            </li>
          </TaskEntityContextMenu>
        ))}
      </ul>
    </section>
  );
}

type TaskEntityContextMenuProps = {
  children: ReactElement;
  task: Task;
  getHref: () => string;
  getAgentPrompt: () => string;
  statusLabel: (status: string) => string;
  transitioning?: boolean;
  onOpenTask: () => void;
  onTransition?: (to: string) => void;
  onTrash?: () => void;
};

/**
 * Detail surfaces retain native text menus while editing. This wrapper is used
 * only for the read-only task title and subtask rows, where a task-level menu
 * is more useful than the workspace background menu.
 */
function TaskEntityContextMenu({
  children,
  task,
  getHref,
  getAgentPrompt,
  statusLabel,
  transitioning = false,
  onOpenTask,
  onTransition,
  onTrash,
}: TaskEntityContextMenuProps) {
  const { t } = useI18n();
  const states = detailStatusOptions(task.status);
  const initial = states.includes("backlog") ? "backlog" : states[0];
  const closed = states.filter((value) => ["done", "complete", "closed", "cancelled", "canceled"].includes(value));
  const copy = async (value: string, success: string) => {
    try {
      await copyText(value);
      toast.success(success);
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
      <ContextMenuContent className="min-w-56">
        <ContextMenuLabel className="max-w-72 truncate font-mono text-[10px]">{task.id}</ContextMenuLabel>
        <ContextMenuGroup>
          <ContextMenuItem onSelect={onOpenTask}>
            <ExternalLink />
            {t("Open task details", "打开任务详情")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => void copy(task.id, t("Task ID copied", "任务 ID 已复制"))}>
            <ClipboardCopy />
            {t("Copy task ID", "复制任务 ID")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => void copy(task.title, t("Task title copied", "任务标题已复制"))}>
            <FileText />
            {t("Copy task title", "复制任务标题")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => void copy(getHref(), t("Task link copied", "任务链接已复制"))}>
            <Link2 />
            {t("Copy task link", "复制任务链接")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => void copy(getAgentPrompt(), t("Agent prompt copied", "智能体提示词已复制"))}>
            <Bot />
            {t("Copy Agent prompt", "复制智能体提示词")}
          </ContextMenuItem>
        </ContextMenuGroup>
        {onTransition && <ContextMenuSeparator />}
        {onTransition && (
          <ContextMenuSub>
            <ContextMenuSubTrigger>{t("Change status", "修改状态")}</ContextMenuSubTrigger>
            <ContextMenuSubContent className="min-w-40">
              <ContextMenuGroup>
                {states.map((state) => (
                  <ContextMenuItem
                    key={state}
                    disabled={transitioning || !task.version || state === task.status}
                    onSelect={() => onTransition(state)}
                  >
                    <StatusIcon status={state} closed={closed} initial={initial} />
                    {statusLabel(state)}
                  </ContextMenuItem>
                ))}
              </ContextMenuGroup>
            </ContextMenuSubContent>
          </ContextMenuSub>
        )}
        {onTrash && <ContextMenuSeparator />}
        {onTrash && (
          <ContextMenuItem variant="destructive" disabled={transitioning || !task.version} onSelect={onTrash}>
            <Trash2 />
            {t("Move to trash", "移入回收站")}
          </ContextMenuItem>
        )}
      </ContextMenuContent>
    </ContextMenu>
  );
}

function TaskStatusPill({ status, label }: { status: string; label: string }) {
  const normalized = status.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const Icon = normalized === "done" || normalized === "complete" || normalized === "closed"
    ? CircleCheck
    : normalized === "blocked" || normalized === "stalled"
      ? CircleX
      : Circle;

  return (
    <span className="inline-flex h-5 items-center gap-1 rounded-full border border-border px-1.5 text-[10px] font-medium text-foreground">
      <Icon className="size-3 text-muted-foreground" />
      {label}
    </span>
  );
}

function TaskSessionTimeline({
  sessions,
  executionState,
  loading,
  title,
}: {
  sessions: AgentSession[];
  executionState?: Task["executionState"];
  loading: boolean;
  title: string;
}) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();

  if (loading) {
    return (
      <section className="mt-9 border-t pt-5">
        <Skeleton className="h-3 w-24" />
        <Skeleton className="mt-3 h-16 w-full" />
      </section>
    );
  }
  if (sessions.length === 0) return null;

  return (
    <section className="mt-9 border-t pt-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-xs font-medium text-muted-foreground">{title}</h2>
        {executionState && <span className="text-[11px] text-muted-foreground">{executionStateLabel(executionState, t)}</span>}
      </div>
      <ol className="mt-2.5 divide-y divide-border/70">
        {sessions.map((session) => {
          const detail = session.live?.progress || session.summary || session.cancelReason;
          return (
            <li key={session.id} className="flex min-w-0 items-start gap-3 py-3 first:pt-0 last:pb-0">
              <span className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-md bg-muted/70 text-muted-foreground">
                <Bot className="size-3.5" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="font-medium text-sm">{formatWorker(session.actor)}</span>
                  <span className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">{session.health}</span>
                  <span className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground">{timeAgo(session.live?.heartbeatAt ?? session.endedAt ?? session.startedAt)}</span>
                </div>
                {(session.client || session.model) && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{[session.client, session.model].filter(Boolean).join(" · ")}</p>}
                {detail && <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-foreground/80">{detail}</p>}
                {(session.branch || session.live?.worktree) && (
                  <p className="mt-2 flex min-w-0 items-center gap-1.5 text-[11px] text-muted-foreground">
                    <GitBranch className="size-3 shrink-0" />
                    <span className="truncate">{session.branch || session.live?.worktree}</span>
                  </p>
                )}
              </div>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

function TaskCodeContext({
  contexts,
  loading,
  title,
}: {
  contexts: SessionGitContext[];
  loading: boolean;
  title: string;
}) {
  const { t } = useI18n();
  if (loading) {
    return (
      <section className="mt-9 border-t pt-5">
        <Skeleton className="h-3 w-24" />
        <Skeleton className="mt-3 h-20 w-full" />
      </section>
    );
  }
  if (contexts.length === 0) return null;

  return (
    <section className="mt-9 border-t pt-5">
      <h2 className="text-xs font-medium text-muted-foreground">{title}</h2>
      <ol className="mt-2.5 divide-y divide-border/70">
        {contexts.map(({ session, context }) => (
          <li key={session.id} className="py-3 first:pt-0 last:pb-0">
            <div className="flex min-w-0 items-center gap-2">
              <GitBranch className="size-3.5 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate text-sm font-medium">{context.branch || session.branch || t("Detached HEAD", "分离的 HEAD")}</span>
              <span className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">{session.status}</span>
            </div>
            {(context.headStarted || context.headFinished || context.currentHead) && (
              <p className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                {context.headStarted && <span>{t("Started at", "开始提交")} <span className="font-mono text-foreground">{shortSha(context.headStarted)}</span></span>}
                {context.headFinished && <span>{t("Finished at", "完成提交")} <span className="font-mono text-foreground">{shortSha(context.headFinished)}</span></span>}
                {!context.headFinished && context.currentHead && <span>{t("Current", "当前提交")} <span className="font-mono text-foreground">{shortSha(context.currentHead)}</span></span>}
              </p>
            )}
            {!context.available ? (
              <p className="mt-2 text-xs text-muted-foreground">{context.error || t("Git context is unavailable.", "Git 上下文不可用。")}</p>
            ) : (
              <div className="mt-2.5 space-y-2 text-xs">
                {(context.warnings ?? []).map((warning) => (
                  <p key={`${warning.kind}-${warning.message}`} className="flex gap-1.5 text-muted-foreground"><AlertTriangle className="mt-0.5 size-3 shrink-0" />{warning.message}</p>
                ))}
                {(context.filesChanged ?? []).length > 0 && (
                  <ContextList icon={<FileText />} title={t("Files changed", "修改文件")} values={(context.filesChanged ?? []).map((file) => `${file.status} ${file.oldPath ? `${file.oldPath} → ` : ""}${file.path}`)} />
                )}
                {(context.commits ?? []).length > 0 && (
                  <ContextList icon={<GitCommit />} title={t("Commits", "提交")} values={(context.commits ?? []).map((commit: GitCommitData) => `${shortSha(commit.hash)} ${commit.subject}`)} />
                )}
                {(context.uncommitted ?? []).length > 0 && (
                  <ContextList icon={<FileText />} title={t("Uncommitted", "未提交")} values={(context.uncommitted ?? []).map((file) => `${file.status} ${file.oldPath ? `${file.oldPath} → ` : ""}${file.path}`)} />
                )}
              </div>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

function ContextList({ icon, title, values }: { icon: ReactNode; title: string; values: string[] }) {
  const visible = values.slice(0, 5);
  return (
    <div>
      <p className="flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">{icon}{title}</p>
      <div className="mt-1 space-y-0.5 pl-4.5 font-mono text-[11px] leading-5 text-muted-foreground">
        {visible.map((value) => <p key={value} className="truncate">{value}</p>)}
        {values.length > visible.length && <p>+{values.length - visible.length}</p>}
      </div>
    </div>
  );
}

function TaskActivityTimeline({
  entries,
  onOpenWorker,
  saving,
  onEdit,
  onDelete,
  emptyLabel,
}: {
  entries: Provenance[];
  onOpenWorker?: (actor: string) => void;
  saving: boolean;
  onEdit: (index: number, text: string, noteId?: string) => void;
  onDelete: (index: number, noteId?: string) => void;
  emptyLabel: string;
}) {
  if (entries.length === 0) return <p className="mt-3 text-sm text-muted-foreground">{emptyLabel}</p>;
  return (
    <ol className="relative mt-4 ml-1 border-l border-border">
      {entries.map((entry, index) => (
        <TaskActivityEntry
          key={entry.id ?? `${entry.at}-${index}`}
          entry={entry}
          index={index}
          onOpenWorker={onOpenWorker}
          saving={saving}
          onEdit={onEdit}
          onDelete={onDelete}
        />
      ))}
    </ol>
  );
}

function TaskActivityEntry({
  entry,
  index,
  onOpenWorker,
  saving,
  onEdit,
  onDelete,
}: {
  entry: Provenance;
  index: number;
  onOpenWorker?: (actor: string) => void;
  saving: boolean;
  onEdit: (index: number, text: string, noteId?: string) => void;
  onDelete: (index: number, noteId?: string) => void;
}) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const text = entry.text?.trim();
  const action = activityAction(entry.did, t);
  const isNote = isActivityNote(entry.did);
  const preview = text?.split("\n").find((line) => line.trim()) ?? "";

  const startEditing = () => {
    setDraft(entry.text ?? "");
    setEditing(true);
    setOpen(true);
  };
  const save = () => {
    const next = draft.trim();
    if (next) onEdit(index, next, entry.id);
    setEditing(false);
  };

  return (
    <li className="group relative pb-5 pl-5 text-sm last:pb-0">
      <span className="absolute -left-[5px] top-1.5 size-2 rounded-full border border-border bg-background" aria-hidden="true" />
      <div className="flex min-w-0 items-center gap-2">
        <Assignee actor={entry.who} onOpenWorker={onOpenWorker} />
        <button type="button" className="min-w-0 truncate font-medium text-left hover:underline hover:underline-offset-4" onClick={() => onOpenWorker?.(entry.who)}>{formatWorker(entry.who)}</button>
        <Badge
          variant="outline"
          title={action.label}
          className={cn("h-6 max-w-56 truncate px-2 text-xs font-medium", activityBadgeClass(action.tone))}
        >
          {action.label}
        </Badge>
        {entry.editedAt && <span className="text-xs text-muted-foreground">{t("edited", "已编辑")}</span>}
        <span className="ml-auto shrink-0 text-xs tabular-nums text-muted-foreground">{timeAgo(entry.at)}</span>
      </div>
      {text && !editing && (
        <div className="mt-1 flex min-w-0 items-start gap-1">
          <button type="button" className="min-w-0 flex-1 truncate text-left text-sm leading-6 text-muted-foreground hover:text-foreground" onClick={() => setOpen((value) => !value)}>
            {open ? t("Hide note", "收起备注") : preview}
          </button>
          {isNote && (
            <span className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
              <Button variant="ghost" size="icon-xs" aria-label={t("Edit note", "编辑备注")} disabled={saving} onClick={startEditing}><Pencil /></Button>
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-destructive"
                aria-label={t("Delete note", "删除备注")}
                disabled={saving}
                onClick={() => {
                  if (window.confirm(t("Delete this note?", "删除此备注？"))) onDelete(index, entry.id);
                }}
              >
                <Trash2 />
              </Button>
            </span>
          )}
        </div>
      )}
      {editing ? (
        <div className="mt-2.5 space-y-2">
          <MarkdownEditor value={draft} onChange={setDraft} placeholder={t("Edit note…", "编辑备注…")} />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" disabled={saving} onClick={() => setEditing(false)}>{t("Cancel", "取消")}</Button>
            <Button size="sm" variant="secondary" disabled={!draft.trim() || saving} onClick={save}>
              {saving && <Loader2 data-icon="inline-start" className="animate-spin" />}
              {t("Save", "保存")}
            </Button>
          </div>
        </div>
      ) : text && open ? (
        <div className="mt-2 border-l border-border pl-3 text-sm leading-6 text-foreground/85"><Markdown>{entry.text!}</Markdown></div>
      ) : null}
    </li>
  );
}

function TaskEvidenceTimeline({
  evidence,
  title,
  onEdit,
  editLabel,
  formatWorker,
}: {
  evidence: TaskEvidence[];
  title: string;
  onEdit: () => void;
  editLabel: string;
  formatWorker: (value?: string) => string;
}) {
  return (
    <section className="mt-9 border-t pt-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-xs font-medium text-muted-foreground">{title}</h2>
        <Button variant="ghost" size="xs" onClick={onEdit}>{editLabel}</Button>
      </div>
      <ol className="mt-2.5 divide-y divide-border/70">
        {evidence.map((item, index) => {
          const label = item.label?.trim() || item.value;
          return (
            <li key={item.id ?? `${item.kind}-${item.value}-${index}`} className="flex min-w-0 items-start gap-3 py-3 first:pt-0 last:pb-0">
              <span className="mt-1.5 size-1.5 shrink-0 rounded-full border border-border bg-background" aria-hidden="true" />
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
                  <span className="font-mono text-[11px] text-muted-foreground">{item.kind}</span>
                  {item.url ? (
                    <a href={item.url} target="_blank" rel="noreferrer" className="min-w-0 break-all text-sm underline decoration-border underline-offset-4 hover:decoration-foreground">
                      {label}
                    </a>
                  ) : (
                    <span className="min-w-0 break-all text-sm">{label}</span>
                  )}
                </div>
                {(item.createdBy || item.createdAt || (item.label && item.label !== item.value)) && (
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    {item.label && item.label !== item.value && <>{item.value}</>}
                    {item.label && item.label !== item.value && (item.createdBy || item.createdAt) && " · "}
                    {item.createdBy && formatWorker(item.createdBy)}
                    {item.createdBy && item.createdAt && " · "}
                    {item.createdAt && timeAgo(item.createdAt)}
                  </p>
                )}
              </div>
              {item.url && <ExternalLink className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />}
            </li>
          );
        })}
      </ol>
    </section>
  );
}

function Prop({
  label,
  action,
  children,
}: {
  label: string;
  action?: ReactNode;
  children: ReactNode;
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

function CarbonExtension({
  open,
  onOpenChange,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <Collapsible open={open} onOpenChange={onOpenChange} className="border-t pt-3">
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="-ml-2 w-[calc(100%+1rem)] justify-between text-muted-foreground">
          <span className="text-xs font-medium">{t("Carbon details", "Carbon 详情")}</span>
          <ChevronDown data-icon="inline-end" className="transition-transform data-[state=open]:rotate-180" />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="pt-3">
        {children}
      </CollapsibleContent>
    </Collapsible>
  );
}

function checkStatus(result: string | undefined, running: boolean) {
  if (running) {
    return { Icon: Loader2, label: "running", icon: "animate-spin text-muted-foreground", pill: "bg-muted text-muted-foreground" };
  }
  switch (result) {
    case "pass":
      return { Icon: CircleCheck, label: "pass", icon: "text-success", pill: "bg-success/10 text-success" };
    case "fail":
      return { Icon: CircleX, label: "fail", icon: "text-destructive", pill: "bg-destructive/10 text-destructive" };
    default:
      return { Icon: Circle, label: "pending", icon: "text-muted-foreground/50", pill: "bg-muted text-muted-foreground" };
  }
}

function StatusPill({ className, children }: { className: string; children: ReactNode }) {
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
  const meta = checkStatus(check.result, running && !isManual);
  const expandable = !isManual && !!run;

  const lead = (
    <>
      <meta.Icon className={cn("size-4 shrink-0", meta.icon)} />
      <span className="min-w-0 flex-1 truncate text-sm">{check.desc}</span>
    </>
  );

  if (isManual && pending) {
    return (
      <div className="flex items-center gap-2 px-3 py-2">
        {lead}
        <span className="flex shrink-0 items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon"
            className="size-6 text-success"
            aria-label={t("Mark check as passed", "标记检查通过")}
            disabled={attesting}
            onClick={() => onAttest(true)}
          >
            {attesting ? <Loader2 className="animate-spin" /> : <CheckIcon className="size-3.5" />}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="size-6 text-destructive"
            aria-label={t("Mark check as failed", "标记检查失败")}
            disabled={attesting}
            onClick={() => onAttest(false)}
          >
            <X className="size-3.5" />
          </Button>
        </span>
      </div>
    );
  }

  if (expandable) {
    return (
      <Collapsible>
        <CollapsibleTrigger className="group flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-foreground/[0.03]">
          {lead}
          <StatusPill className={meta.pill}>{t(
            meta.label === "running" ? "Running" : meta.label === "pass" ? "Passed" : meta.label === "fail" ? "Failed" : "Pending",
            meta.label === "running" ? "检查中" : meta.label === "pass" ? "已通过" : meta.label === "fail" ? "未通过" : "待检查",
          )}</StatusPill>
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

  return (
    <div className="flex items-center gap-2 px-3 py-2">
      {lead}
      <StatusPill className={meta.pill}>{t(
        meta.label === "running" ? "Running" : meta.label === "pass" ? "Passed" : meta.label === "fail" ? "Failed" : "Pending",
        meta.label === "running" ? "检查中" : meta.label === "pass" ? "已通过" : meta.label === "fail" ? "未通过" : "待检查",
      )}</StatusPill>
    </div>
  );
}

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
          {t("Checks", "检查项")}
          {checks.length > 0 && (
            <span className={cn("tabular-nums", checks.every((check) => check.result === "pass") && "text-success")}>
              {checks.filter((check) => check.result === "pass").length}/{checks.length}
            </span>
          )}
        </h3>
        <div className="flex items-center gap-1">
          {checks.some((check) => check.cmd) && (
            <Button
              variant="outline"
              size="sm"
              className="h-6 gap-1 px-2 text-xs"
              disabled={running}
              onClick={onRun}
            >
              {running ? <Loader2 className="size-3 animate-spin" /> : <Play className="size-3" />}
              {t("Run checks", "运行检查")}
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
          {checks.map((check, index) => (
            <CheckRow
              key={index}
              check={check}
              run={check.cmd ? runs.find((run) => run.cmd === check.cmd) : undefined}
              running={running}
              onAttest={(pass) => onAttest(index, pass)}
              attesting={attesting}
            />
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("No checks yet.", "还没有检查项。")}</p>
      )}
    </div>
  );
}

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
  const [draft, setDraft] = useState<Check[]>(checks.map((check) => ({ ...check })));

  const setRow = (index: number, next: Partial<Check>) =>
    setDraft((current) => current.map((check, currentIndex) => currentIndex === index ? { ...check, ...next } : check));
  const removeRow = (index: number) => setDraft((current) => current.filter((_, currentIndex) => currentIndex !== index));
  const addRow = () => setDraft((current) => [...current, { desc: "", cmd: "" }]);
  const save = () => {
    const cleaned = draft
      .map((check) => ({ ...check, desc: check.desc.trim(), cmd: (check.cmd ?? "").trim() }))
      .filter((check) => check.desc)
      .map((check) => ({ ...check, type: check.cmd ? "" : "manual" }));
    onSave(cleaned);
  };

  return (
    <div className="space-y-2">
      <h3 className="text-xs font-medium text-muted-foreground">{t("Checks", "检查项")}</h3>
      <div className="space-y-2 rounded-lg border p-2">
        {draft.length === 0 && (
          <p className="px-1 py-2 text-xs text-muted-foreground">{t("No checks yet. Add one below.", "还没有检查项，可以在下方添加。")}</p>
        )}
        {draft.map((check, index) => (
          <div key={index} className="space-y-1.5 rounded-md border bg-muted/30 p-2">
            <div className="flex items-center gap-1.5">
              <Input
                value={check.desc}
                placeholder={t("What should be checked…", "检查内容…")}
                onChange={(event) => setRow(index, { desc: event.target.value })}
                className="h-7 text-xs"
              />
              <Button
                variant="ghost"
                size="icon"
                className="size-7 shrink-0 text-destructive"
                aria-label={t("Remove check", "移除检查项")}
                onClick={() => removeRow(index)}
              >
                <X className="size-3.5" />
              </Button>
            </div>
            <Input
              value={check.cmd ?? ""}
              placeholder={t("Command (leave blank for a manual check)", "命令（留空则手动检查）")}
              onChange={(event) => setRow(index, { cmd: event.target.value })}
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
    const value = input.trim();
    if (value && !labels.includes(value)) onChange([...labels, value]);
    setInput("");
  };

  return (
    <div className="space-y-1.5">
      {labels.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {labels.map((label) => (
            <Badge key={label} variant="secondary" className="gap-1 pr-1 text-xs font-normal">
              {label}
              <button
                aria-label={t("Remove {label}", "移除标签 {label}", { label })}
                onClick={() => onChange(labels.filter((value) => value !== label))}
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
        onChange={(event) => setInput(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            add();
          }
        }}
        placeholder={t("Add label…", "添加标签…")}
        className="h-7 text-xs"
      />
    </div>
  );
}



function EvidenceEditor({
  evidence,
  onChange,
  onUpdate,
}: {
  evidence: TaskEvidence[];
  onChange: (evidence: TaskEvidence[]) => void;
  onUpdate: (index: number, next: Partial<TaskEvidence>) => void;
}) {
  const { t } = useI18n();
  const formatWorker = useWorkerAliasFormatter();
  return (
    <section className="mt-5 border-t pt-4">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-medium">{t("Delivery records", "交付凭据")}</h3>
          <p className="text-xs text-muted-foreground">{t("Keep links, screenshots, or verification results here for later reference.", "把链接、截图或验证结果记在这里，方便回看。")}</p>
        </div>
        <Button type="button" variant="outline" size="xs" onClick={() => onChange([...evidence, { kind: "", value: "" }])}>
          <Plus data-icon="inline-start" />{t("Add", "添加")}
        </Button>
      </div>
      <div className="mt-3 flex flex-col gap-3">
        {evidence.map((item, index) => (
          <div key={item.id ?? `new-${index}`} className="flex flex-col gap-2 border-b pb-3">
            <div className="grid grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)_auto] gap-2">
              <Input value={item.kind} onChange={(event) => onUpdate(index, { kind: event.target.value })} placeholder={t("Kind", "类型")} />
              <Input value={item.value} onChange={(event) => onUpdate(index, { value: event.target.value })} placeholder={t("Record details", "凭据内容")} />
              <Button type="button" variant="ghost" size="icon-xs" className="text-destructive" aria-label={t("Remove delivery record", "移除交付凭据")} onClick={() => onChange(evidence.filter((_, itemIndex) => itemIndex !== index))}>
                <X />
              </Button>
            </div>
            <Input value={item.label ?? ""} onChange={(event) => onUpdate(index, { label: event.target.value })} placeholder={t("Label (optional)", "标签（可选）")} />
            <Input value={item.url ?? ""} onChange={(event) => onUpdate(index, { url: event.target.value })} placeholder={t("URL (optional)", "链接（可选）")} />
            {(item.id || item.createdAt || item.createdBy) && (
              <p className="font-mono text-xs text-muted-foreground">
                {item.id && `#${item.id}`}{item.createdBy && `${item.id ? " · " : ""}${formatWorker(item.createdBy)}`}{item.createdAt && `${item.id || item.createdBy ? " · " : ""}${timeAgo(item.createdAt)}`}
              </p>
            )}
          </div>
        ))}
        {evidence.length === 0 && <p className="text-xs text-muted-foreground">{t("No delivery records yet.", "还没有添加交付凭据。")}</p>}
      </div>
    </section>
  );
}


function LeaseEditor({
  taskId,
  version,
  assignee,
  onAssigneeChange,
  lease,
  pendingClaims,
  actor,
  leaseReason,
  onLeaseReasonChange,
  forceReassign,
  onForceReassignChange,
  pending,
  requiresForce,
  formatWorker,
  onOpenWorker,
  onClaim,
  onRenew,
  onRelease,
  onReassign,
  onApprove,
  heldByCurrentActor,
}: {
  taskId: string;
  version?: string;
  assignee: string;
  onAssigneeChange: (value: string) => void;
  lease?: { id?: string; holder?: string };
  pendingClaims: Array<{ requestId?: string; actor?: string; assignee?: string; reason?: string; requestedAt?: string; leaseTtlSeconds?: number }>;
  actor: string;
  leaseReason: string;
  onLeaseReasonChange: (value: string) => void;
  forceReassign: boolean;
  onForceReassignChange: (value: boolean) => void;
  pending: boolean;
  requiresForce: boolean;
  formatWorker: (value?: string) => string;
  onOpenWorker?: (actor: string) => void;
  onClaim: () => void;
  onRenew: () => void;
  onRelease: () => void;
  onReassign: () => void;
  onApprove: (requestId: string, approve: boolean) => void;
  heldByCurrentActor: boolean;
}) {
  const { t } = useI18n();
  const missingVersion = !version;
  const hasLease = Boolean(lease?.id);
  return (
    <div className="flex flex-col gap-3">
      {lease?.holder && (
        <div className="flex items-center gap-2 text-sm">
          <Assignee actor={lease.holder} onOpenWorker={onOpenWorker} />
          <button type="button" className="truncate text-left hover:underline" onClick={() => onOpenWorker?.(lease.holder!)}>{formatWorker(lease.holder)}</button>
        </div>
      )}
      <FieldGroup className="gap-3">
        <Field>
          <FieldLabel htmlFor={`carbon-task-assignee-${taskId}`}>{t("Assignee", "负责人")}</FieldLabel>
          <Input id={`carbon-task-assignee-${taskId}`} value={assignee} onChange={(event) => onAssigneeChange(event.target.value)} placeholder="human:you or agent:codex" />
        </Field>
        <Field>
          <FieldLabel htmlFor={`carbon-task-lease-reason-${taskId}`}>{t("Handoff note", "接手/转交说明")}</FieldLabel>
          <Input id={`carbon-task-lease-reason-${taskId}`} value={leaseReason} onChange={(event) => onLeaseReasonChange(event.target.value)} placeholder={t("Explain conflicts, handoffs, releases, or approvals", "遇到冲突、转交、释放或审批时，请说明原因")} />
        </Field>
      </FieldGroup>
      {!hasLease ? (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" disabled={missingVersion || pending || !leaseReason.trim()} onClick={onClaim}>{t("Ask to take over", "申请接手")}</Button>
          <Button size="sm" variant="outline" disabled={missingVersion || pending || !assignee.trim() || !leaseReason.trim() || (requiresForce && !forceReassign)} onClick={onReassign}>{t("Assign", "分配")}</Button>
        </div>
      ) : heldByCurrentActor ? (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" disabled={missingVersion || pending} onClick={onRenew}>{t("Renew", "续期")}</Button>
          <Button size="sm" variant="outline" disabled={missingVersion || pending || !leaseReason.trim()} onClick={onRelease}>{t("Release", "释放")}</Button>
        </div>
      ) : null}
      {requiresForce && !heldByCurrentActor && (
        <div className="flex flex-col gap-2 border-t pt-3">
          <Field orientation="horizontal">
            <Checkbox id={`carbon-task-force-lease-${taskId}`} checked={forceReassign} onCheckedChange={(checked) => onForceReassignChange(checked === true)} />
            <FieldLabel htmlFor={`carbon-task-force-lease-${taskId}`}>{t("Force reassignment", "强制重新分配")}</FieldLabel>
          </Field>
          <Button size="sm" variant="outline" className="self-start" disabled={missingVersion || !assignee.trim() || !leaseReason.trim() || !forceReassign || pending} onClick={onReassign}>{t("Hand off task", "转交任务")}</Button>
        </div>
      )}
      {pendingClaims.length > 0 && (
        <div className="flex flex-col gap-2 border-t pt-3">
          <p className="text-xs font-medium">{t("Handoff requests to review", "待确认的接手申请")}</p>
          {pendingClaims.map((request) => request.requestId && (
            <div key={request.requestId} className="flex flex-wrap items-center gap-2 border-b pb-2 text-xs">
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{formatWorker(request.actor ?? request.assignee) || request.requestId}</span>
                {request.reason && <span className="mt-0.5 block text-muted-foreground">{request.reason}</span>}
                <span className="mt-0.5 block text-muted-foreground">{request.requestedAt ? timeAgo(request.requestedAt) : t("time unavailable", "时间未知")}{request.leaseTtlSeconds ? ` · ${t("valid for {seconds} seconds", "有效期 {seconds} 秒", { seconds: request.leaseTtlSeconds })}` : ""}</span>
              </span>
              <Button size="xs" variant="outline" disabled={missingVersion || pending || !leaseReason.trim()} onClick={() => onApprove(request.requestId!, true)}>{t("Approve", "批准")}</Button>
              <Button size="xs" variant="ghost" disabled={missingVersion || pending || !leaseReason.trim()} onClick={() => onApprove(request.requestId!, false)}>{t("Reject", "拒绝")}</Button>
            </div>
          ))}
        </div>
      )}
      {actor && <p className="sr-only">{actor}</p>}
    </div>
  );
}

function formatStagnationPeriod(
  seconds: number | undefined,
  t: (english: string, chinese: string, vars?: Record<string, string | number>) => string,
): string {
  if (!seconds || seconds <= 0) return t("the configured period", "项目设定周期");
  if (seconds % 86_400 === 0) return t("{count} days", "{count} 天", { count: seconds / 86_400 });
  if (seconds % 3_600 === 0) return t("{count} hours", "{count} 小时", { count: seconds / 3_600 });
  return t("{count} minutes", "{count} 分钟", { count: Math.max(1, Math.round(seconds / 60)) });
}

function formatBeijingDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

function statusLabel(value: string, t: (english: string, chinese: string) => string): string {
  const normalized = value.trim().toLowerCase().replace(/[\s-]+/g, "_");
  const labels: Record<string, [string, string]> = {
    backlog: ["Backlog", "待办"],
    ready: ["Ready", "就绪"],
    in_progress: ["In progress", "进行中"],
    review: ["In review", "审核中"],
    done: ["Done", "已完成"],
  };
  const label = labels[normalized];
  return label ? t(...label) : value;
}

function shortSha(value: string): string {
  return value.length > 7 ? value.slice(0, 7) : value;
}

function executionStateLabel(value: string, t: (english: string, chinese: string) => string): string {
  const labels: Record<string, [string, string]> = {
    active: ["Active", "执行中"],
    stalled: ["Stalled", "已停滞"],
    awaiting_review: ["Awaiting review", "等待审核"],
  };
  const label = labels[value];
  return label ? t(...label) : value;
}
