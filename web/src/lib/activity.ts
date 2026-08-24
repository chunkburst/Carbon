import type { Translate } from "@/lib/i18n";
import { statusLabel } from "@/lib/utils";

export type ActivityTone = "brand" | "info" | "success" | "warning" | "destructive" | "muted";

export type ActivityAction = {
  label: string;
  tone: ActivityTone;
};

const activityToneClasses: Record<ActivityTone, string> = {
  brand: "border-brand/30 bg-brand/10 text-brand",
  info: "border-info/30 bg-info/10 text-info",
  success: "border-success/30 bg-success/10 text-success",
  warning: "border-warning/30 bg-warning/10 text-warning",
  destructive: "border-destructive/30 bg-destructive/10 text-destructive",
  muted: "border-border bg-muted text-muted-foreground",
};

type ActivityLabel = {
  en: string;
  zh: string;
  tone: ActivityTone;
};

const exactActivityLabels: Record<string, ActivityLabel> = {
  created: { en: "Created", zh: "已创建", tone: "success" },
  create: { en: "Created", zh: "已创建", tone: "success" },
  updated: { en: "Updated", zh: "已更新", tone: "info" },
  update: { en: "Updated", zh: "已更新", tone: "info" },
  note: { en: "Note", zh: "备注", tone: "muted" },
  "ran checks": { en: "Ran checks", zh: "已运行检查", tone: "info" },
  attested: { en: "Attested", zh: "已确认检查", tone: "brand" },
  blocked: { en: "Blocked", zh: "已阻塞", tone: "destructive" },
  unblocked: { en: "Unblocked", zh: "已解除阻塞", tone: "success" },
  "project moved": { en: "Project moved", zh: "项目已移动", tone: "info" },
  "assignee changed": { en: "Assignee changed", zh: "负责人已变更", tone: "info" },
  "assignee reassigned": { en: "Assignee reassigned", zh: "负责人已重新分配", tone: "info" },
  "bulk updated": { en: "Bulk updated", zh: "已批量更新", tone: "info" },
  imported: { en: "Imported", zh: "已导入", tone: "success" },
  trashed: { en: "Trashed", zh: "已移至回收站", tone: "destructive" },
  "restored from trash": { en: "Restored from trash", zh: "已从回收站恢复", tone: "success" },
  claimed: { en: "Taken over", zh: "已接手", tone: "brand" },
  "lease auto-released": { en: "Handoff released automatically", zh: "接手已自动释放", tone: "warning" },
  "lease renewed": { en: "Handoff window renewed", zh: "接手期限已续期", tone: "brand" },
  "lease claimed": { en: "Task taken over", zh: "已接手任务", tone: "brand" },
  "lease released": { en: "Task released", zh: "任务已释放", tone: "muted" },
  "session lease renewed": { en: "Session handoff window renewed", zh: "会话接手期限已续期", tone: "brand" },
  "session lease claimed": { en: "Session taken over", zh: "已接手会话", tone: "brand" },
  "claim approval requested": { en: "Handoff requested", zh: "已提交接手申请", tone: "warning" },
  "claim approval rejected": { en: "Handoff request declined", zh: "接手申请被拒绝", tone: "destructive" },
  "claim approval approved": { en: "Handoff request approved", zh: "接手申请已通过", tone: "success" },
};

function presentLabel(label: ActivityLabel, t: Translate): ActivityAction {
  return { label: t(label.en, label.zh), tone: label.tone };
}

function statusTransitionTone(status: string): ActivityTone {
  switch (status.trim().toLowerCase().replace(/[\s-]+/g, "_")) {
    case "blocked":
      return "destructive";
    case "stalled":
    case "awaiting_review":
    case "in_review":
    case "review":
      return "warning";
    case "done":
    case "complete":
    case "completed":
    case "closed":
      return "success";
    case "canceled":
    case "cancelled":
      return "muted";
    case "active":
    case "in_progress":
      return "brand";
    default:
      return "info";
  }
}

function sessionActionLabel(action: string, sessionID: string, t: Translate): ActivityAction {
  const id = sessionID.trim();
  switch (action.toLowerCase()) {
    case "began":
      return {
        label: id ? t("Began session {id}", "已开始会话 {id}", { id }) : t("Began session", "已开始会话"),
        tone: "brand",
      };
    case "finished":
      return {
        label: id ? t("Finished session {id}", "已完成会话 {id}", { id }) : t("Finished session", "已完成会话"),
        tone: "success",
      };
    default:
      return {
        label: id ? t("Canceled session {id}", "已取消会话 {id}", { id }) : t("Canceled session", "已取消会话"),
        tone: "destructive",
      };
  }
}

/**
 * Resolves the persisted English audit action at the presentation boundary. The raw
 * `did` value remains the durable protocol value; callers must keep using it for
 * behavioral checks such as note editing.
 */
export function activityAction(did: string, t: Translate): ActivityAction {
  const raw = did.trim();
  const normalized = raw.toLowerCase();
  const exact = exactActivityLabels[normalized];
  if (exact) return presentLabel(exact, t);

  const transition = raw.match(/^transitioned to\s+(.+)$/i);
  if (transition) {
    const status = statusLabel(transition[1]);
    return {
      label: t("Transitioned to {status}", "已切换为{status}", { status }),
      tone: statusTransitionTone(transition[1]),
    };
  }

  const bulkTransition = raw.match(/^bulk transitioned to\s+(.+)$/i);
  if (bulkTransition) {
    const status = statusLabel(bulkTransition[1]);
    return {
      label: t("Bulk transitioned to {status}", "已批量切换为{status}", { status }),
      tone: statusTransitionTone(bulkTransition[1]),
    };
  }

  const session = raw.match(/^(began|finished|canceled|cancelled) session(?:\s+(.+))?$/i);
  if (session) return sessionActionLabel(session[1], session[2] ?? "", t);

  return { label: did, tone: "muted" };
}

/** The action tag's stable semantic token class; never derive a random color from text. */
export function activityBadgeClass(tone: ActivityTone): string {
  return activityToneClasses[tone];
}

/** Note behavior is keyed exclusively from the persisted protocol action. */
export function isActivityNote(did: string): boolean {
  return did.trim().toLowerCase() === "note";
}
