import type { CarbonWorkLog, CarbonWorkLogCreate, CarbonWorkLogVisibility } from "@/lib/carbon-api";

export type WorkLogVisibility = CarbonWorkLogVisibility;

/** The full API record. Immutable audit fields are shown in the UI but are never sent by an editor mutation. */
export type WorkLog = CarbonWorkLog;

export type WorkLogDraft = CarbonWorkLogCreate;
export {
  IDENTITY_DRAFT_TAG,
  isWorkLogCoordinationDraft,
  workLogCoordinationDraft,
  type WorkLogCoordinationDraft,
} from "@/lib/worklog-coordination";

/**
 * A task link carries its durable source scope. `projectId: ""` is reserved for
 * a known cluster-wide task; an omitted project id is intentionally unknown and
 * callers must fail closed instead of borrowing their current project.
 */
export type TaskNavigationTarget = {
  taskId: string;
  clusterId?: string;
  projectId?: string;
};

type TaskNavigationScope = Pick<TaskNavigationTarget, "clusterId" | "projectId">;

function nonEmpty(value?: string): string | undefined {
  const cleaned = value?.trim();
  return cleaned || undefined;
}

/**
 * The Work Log service binds a linked project-owned task back onto `projectId`.
 * Its current DTO always serializes that field, so a present empty value paired
 * with a cluster id is an explicit cluster-wide task rather than a fallback.
 */
export function workLogTaskNavigationTarget(log: Pick<WorkLog, "taskId" | "clusterId" | "projectId">): TaskNavigationTarget | undefined {
  const taskId = nonEmpty(log.taskId);
  if (!taskId) return undefined;

  const projectId = nonEmpty(log.projectId);
  const clusterId = nonEmpty(log.clusterId);
  if (projectId) return { taskId, ...(clusterId ? { clusterId } : {}), projectId };
  if (clusterId && Object.prototype.hasOwnProperty.call(log, "projectId")) {
    return { taskId, clusterId, projectId: "" };
  }
  return { taskId };
}

/**
 * Worker metrics are generated from the queried task set. A concrete report
 * scope is therefore safe only when it is project- or cluster-scoped; a Home
 * aggregate with no task owner stays unresolved for the workspace to reject.
 */
export function recentWorkTaskNavigationTarget(
  item: TaskNavigationTarget,
  sourceScope?: TaskNavigationScope,
): TaskNavigationTarget | undefined {
  const taskId = nonEmpty(item.taskId);
  if (!taskId) return undefined;

  const clusterId = nonEmpty(item.clusterId) ?? nonEmpty(sourceScope?.clusterId);
  if (item.projectId === "") {
    return clusterId ? { taskId, clusterId, projectId: "" } : { taskId };
  }

  const projectId = nonEmpty(item.projectId);
  if (projectId) return { taskId, ...(clusterId ? { clusterId } : {}), projectId };

  const scopedProjectId = nonEmpty(sourceScope?.projectId);
  if (scopedProjectId) return { taskId, ...(clusterId ? { clusterId } : {}), projectId: scopedProjectId };
  if (clusterId) return { taskId, clusterId, projectId: "" };
  return { taskId };
}

export type WorkLogFilters = {
  scope: "all" | "cluster" | "project";
  worker?: string;
  taskId?: string;
  visibility?: WorkLogVisibility;
};

export const WORK_LOG_VISIBILITIES: WorkLogVisibility[] = ["worker_private", "project_public", "global_public"];

export function workLogDraft(log?: WorkLog | null): WorkLogDraft {
  return {
    visibility: log?.visibility ?? "project_public",
    projectId: log?.projectId,
    taskId: log?.taskId,
    title: log?.title ?? "",
    body: log?.body ?? "",
    tags: log?.tags ?? [],
  };
}
