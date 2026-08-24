import { useRef, useState } from "react";
import { CheckCircle2, GitBranch, MoreHorizontal, RefreshCw, Trash2, UserPlus } from "lucide-react";
import { Assignee } from "@/components/Assignee";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { PriorityIcon } from "@/components/PriorityIcon";
import { SessionStatus } from "@/components/SessionStatus";
import { TaskMetadata } from "@/components/TaskMetadata";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { StatusIcon } from "@/components/StatusIcon";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { hasCarbonFeature } from "@/lib/carbon-api";
import { currentActor } from "@/lib/identity";
import { useCarbonCapabilities, useClaimCarbonLease, useDeleteTask, useTransition } from "@/lib/queries";
import { cn, labelTone, statusLabel, timeAgo } from "@/lib/utils";
import type { Status, Task } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

export function TaskRow({
  path,
  task,
  status,
  onOpen,
  selected,
  bulkSelected,
  onBulkSelect,
}: {
  path: string;
  task: Task;
  status: Status;
  onOpen: (id: string) => void;
  selected?: boolean;
  bulkSelected?: boolean;
  onBulkSelect?: (checked: boolean) => void;
}) {
  const transition = useTransition(path);
  const leaseClaim = useClaimCarbonLease(path);
  const capabilities = useCarbonCapabilities(path);
  const deleteTask = useDeleteTask(path);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmClaim, setConfirmClaim] = useState(false);
  const [claimReason, setClaimReason] = useState("");
  const [claimSubmitting, setClaimSubmitting] = useState(false);
  const [claimAwaitingApproval, setClaimAwaitingApproval] = useState(false);
  const claimSubmissionLock = useRef(false);
  const { t } = useI18n();
  const actor = currentActor() || t("current user", "当前用户");
  const leaseSupported = capabilities.data?.available === true && hasCarbonFeature(capabilities.data.data, "leases");
  const claimPending = leaseClaim.isPending || claimSubmitting;
  const claimChecking = capabilities.isLoading;
  const missingLeaseVersion = leaseSupported && !task.version;

  const setClaimDialogOpen = (open: boolean) => {
    setConfirmClaim(open);
    if (open) setClaimAwaitingApproval(false);
    else setClaimReason("");
  };

  const confirmTaskClaim = () => {
    if (
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

  const checks = task.checks ?? [];
  const passed = checks.filter((c) => c.result === "pass").length;
  const allPass = checks.length > 0 && passed === checks.length;
  const stop = (e: React.MouseEvent) => e.stopPropagation();

  return (
    <div
      id={`row-${task.id}`}
      role="button"
      tabIndex={0}
      onClick={() => onOpen(task.id)}
      onKeyDown={(e) => {
        if (e.key === "Enter") onOpen(task.id);
      }}
      className={cn(
        "group flex h-8 w-full cursor-pointer items-center gap-2 px-3 text-left transition-colors hover:bg-foreground/[0.04] focus-visible:bg-foreground/[0.04] focus-visible:outline-none",
        selected && "bg-foreground/[0.06] ring-1 ring-inset ring-brand/40",
      )}
    >
      {onBulkSelect && (
        <Checkbox
          checked={bulkSelected}
          aria-label={t("Select {id}", "选择 {id}", { id: task.id })}
          onClick={stop}
          onCheckedChange={(checked) => onBulkSelect(checked === true)}
          className="shrink-0"
        />
      )}

      {/* priority occupies the leading column (aligns with the section header chevron) */}
      <PriorityIcon priority={task.priority} className="shrink-0" />

      {/* inline status change */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild onClick={stop}>
          <button
            aria-label={t("Change status", "更改状态")}
            className="grid size-4 shrink-0 place-items-center rounded hover:ring-2 hover:ring-foreground/10"
          >
            <StatusIcon
              status={task.status}
              closed={status.closed}
              initial={status.initial}
              className="size-3.5"
            />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" onClick={stop}>
          {(status.states ?? []).map((s) => (
            <DropdownMenuItem
              key={s}
              disabled={s === task.status || transition.isPending}
              onSelect={() => transition.mutate({ id: task.id, to: s })}
            >
              <StatusIcon status={s} closed={status.closed} initial={status.initial} />
              {statusLabel(s)}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>

      <span className="w-20 shrink-0 truncate font-mono text-xs whitespace-nowrap text-muted-foreground">
        {task.id}
      </span>
      <span className="flex-1 truncate text-[13px]">{task.title}</span>

      <TaskMetadata task={task} compact className="hidden shrink-0 xl:flex" />

      <SessionStatus state={task.executionState} compact />

      {task.labels?.slice(0, 2).map((l) => (
        <Badge
          key={l}
          variant="secondary"
          className={cn("carbon-label hidden h-4 shrink-0 px-1.5 text-[10px] font-normal sm:inline-flex", labelTone(l))}
        >
          {l}
        </Badge>
      ))}
      {task.labels && task.labels.length > 2 && (
        <span className="hidden shrink-0 text-[10px] text-muted-foreground sm:inline">
          +{task.labels.length - 2}
        </span>
      )}

      {task.ready && task.status === status.initial && (
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="size-1.5 shrink-0 rounded-full bg-brand" />
          </TooltipTrigger>
          <TooltipContent>{t("Ready to start — dependencies closed", "可以开始——依赖已关闭")}</TooltipContent>
        </Tooltip>
      )}

      {task.deps && task.deps.length > 0 && (
        <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
          <GitBranch className="size-3" />
          {task.deps.length}
        </span>
      )}

      {checks.length > 0 && (
        <span
          className={cn(
            "flex shrink-0 items-center gap-1 text-xs tabular-nums",
            allPass ? "text-success" : "text-muted-foreground",
          )}
        >
          <CheckCircle2 className="size-3" />
          {passed}/{checks.length}
        </span>
      )}

      {task.updatedAt && (
        <span className="hidden shrink-0 text-xs text-muted-foreground tabular-nums sm:block">
          {timeAgo(task.updatedAt)}
        </span>
      )}

      {/* The compact action still appears on hover, but never mutates ownership directly. */}
      {task.assignee ? (
        <Assignee actor={task.assignee} className="shrink-0" />
      ) : (
        <button
          aria-label={t("Take over", "接手")}
          disabled={claimPending || claimChecking || !leaseSupported}
          onClick={(e) => {
            stop(e);
            setClaimDialogOpen(true);
          }}
          title={!claimChecking && !leaseSupported ? t("Taking over a task needs the current Carbon workflow", "接手任务需要当前 Carbon 工作流程") : undefined}
          className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-30 group-hover:opacity-100 focus-visible:opacity-100"
        >
          <UserPlus className="size-3.5" />
        </button>
      )}

      <DropdownMenu>
        <DropdownMenuTrigger asChild onClick={stop}>
          <button
            aria-label={t("Task actions", "任务操作")}
            className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground opacity-0 hover:bg-foreground/10 hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
          >
            <MoreHorizontal className="size-3.5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" onClick={stop}>
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirmDelete(true)}>
            <Trash2 /> {t("Delete", "删除")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <ConfirmDeleteDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t("Delete {task}?", "删除 {task}？", { task: task.id })}
        description={
          <>
            {t("This permanently deletes ", "这将永久删除 ")}
            <span className="font-medium">{task.title}</span>
            {t(
              ". Tasks with sub-tasks or dependents can't be deleted until those are removed.",
              "。包含子任务或依赖项的任务，需先移除这些内容后才能删除。",
            )}
          </>
        }
        confirmLabel={t("Delete task", "删除任务")}
        pending={deleteTask.isPending}
        onConfirm={() => deleteTask.mutate(task.id, { onSuccess: () => setConfirmDelete(false) })}
      />

      <AlertDialog open={confirmClaim} onOpenChange={setClaimDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("Take over this task?", "确认接手此任务？")}</AlertDialogTitle>
            <AlertDialogDescription>
              {claimChecking
                ? t("Checking the task's current state…", "正在检查任务当前状态…")
                : leaseSupported
                  ? t(
                      "This asks to hand the task to {actor}. If someone else is responsible now, they will confirm the handoff first.",
                      "这会请求将任务交给 {actor}。如果目前已有负责人，对方需要先确认交接。",
                      { actor },
                    )
                  : t(
                      "This Carbon project needs an update before the task can be taken over.",
                      "当前 Carbon 项目需要更新后，才能接手此任务。",
                    )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="rounded-lg bg-muted p-3 text-sm">
            <p className="font-medium">{task.title}</p>
            <p className="mt-1 font-mono text-xs text-muted-foreground">{task.id}</p>
            <p className="mt-2 text-muted-foreground">
              {t("Taking over: {actor}", "接手人：{actor}", { actor })}
            </p>
          </div>
          {leaseSupported ? (
            <>
              <FieldGroup className="gap-3">
                <Field>
                  <FieldLabel htmlFor={`claim-reason-${task.id}`}>
                    {t("Why take this task?", "接手说明")}
                  </FieldLabel>
                  <Input
                    id={`claim-reason-${task.id}`}
                    value={claimReason}
                    onChange={(event) => setClaimReason(event.target.value)}
                    placeholder={t("What will you take care of?", "说明接手后会负责什么")}
                  />
                </Field>
              </FieldGroup>
              {missingLeaseVersion && (
                <Alert>
                  <RefreshCw />
                  <AlertDescription>
                    {t(
                      "This task changed or did not load completely. Refresh it before taking it over.",
                      "此任务刚刚变更或未完整加载。请先刷新，再接手。",
                    )}
                  </AlertDescription>
                </Alert>
              )}
            </>
          ) : <Alert><AlertDescription>{t("This task needs the current Carbon handoff flow before it can be taken over.", "此任务需要使用当前 Carbon 交接流程后，才能接手。")}</AlertDescription></Alert>}
          {claimAwaitingApproval && <Alert><AlertDescription>{t("Waiting for the current task lead to confirm the handoff.", "正在等待当前负责人确认交接。")}</AlertDescription></Alert>}
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
                    ? t("Waiting for confirmation", "等待确认")
                  : claimPending
                    ? t("Requesting takeover…", "正在请求接手…")
                    : t("Request takeover", "请求接手")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
