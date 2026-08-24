import type { Task } from "./api.ts";

// Lowercase Crockford base32 alphabet, matching the Go minter (store/id.go). Strictly
// ascending, so decoding preserves chronological order.
const CROCKFORD = "0123456789abcdefghjkmnpqrstvwxyz";

// 2024-01-01T00:00:00Z in Unix seconds, matching internal/store/id.go.
const ID_EPOCH_SEC = 1704067200;
const DEFAULT_MANUAL_RANK = 1_024;

export type TaskColumns = Record<string, string[]>;
export type CrossStatusMove = { activeId: string; from: string; to: string; overId: string; after: boolean };
export type TaskDropPreviewMove = CrossStatusMove & { hasCrossedStatus: boolean };

/** Manual rank when present, otherwise a creation-order proxy derived from the task id. */
export function effectiveRank(task: Task): number {
  if (task.rank) return task.rank;
  const suffix = task.id.slice(task.id.indexOf("-") + 1);
  if (/^\d+$/.test(suffix)) return Number(suffix);
  const isCurrent = suffix.length <= 10;
  const timeChars = isCurrent ? 6 : 10;
  let value = 0;
  for (let index = 0; index < timeChars && index < suffix.length; index += 1) {
    const digit = CROCKFORD.indexOf(suffix[index]);
    if (digit < 0) return 0;
    value = value * 32 + digit;
  }
  return isCurrent ? (value + ID_EPOCH_SEC) * 1000 : value;
}

export function compareTaskOrder(left: Task, right: Task): number {
  return effectiveRank(left) - effectiveRank(right) || left.id.localeCompare(right.id);
}

export function moveTaskAcrossStatuses(columns: TaskColumns, move: CrossStatusMove): TaskColumns {
  if (move.from === move.to) return columns;
  return placeTaskAtDropTarget(columns, move);
}

/**
 * Updates the optimistic order while a task that originated in another status keeps moving
 * inside its destination. Native sortable transforms handle ordinary same-status drags, but
 * a cross-status task already lives in the destination preview and needs an explicit reorder.
 */
export function moveTaskForDropPreview(columns: TaskColumns, move: TaskDropPreviewMove): TaskColumns {
  if (move.from === move.to && !move.hasCrossedStatus) return columns;
  return placeTaskAtDropTarget(columns, move);
}

function placeTaskAtDropTarget(columns: TaskColumns, move: CrossStatusMove): TaskColumns {
  const source = columns[move.from] ?? [];
  const sourceIndex = source.indexOf(move.activeId);
  if (sourceIndex < 0 || move.overId === move.activeId) return columns;

  const nextSource = [...source];
  nextSource.splice(sourceIndex, 1);
  const destination = move.from === move.to ? nextSource : [...(columns[move.to] ?? [])];
  const overIndex = destination.indexOf(move.overId);
  const insertionIndex = overIndex >= 0 ? overIndex + (move.after ? 1 : 0) : destination.length;
  destination.splice(insertionIndex, 0, move.activeId);
  if (move.from === move.to) {
    if (destination.every((id, index) => id === source[index])) return columns;
    return { ...columns, [move.to]: destination };
  }
  return { ...columns, [move.from]: nextSource, [move.to]: destination };
}

/**
 * Computes one durable rank from the visible drop order while respecting tasks hidden by
 * filters. An empty-looking filtered column appends after its complete state; a drop between
 * visible tasks uses the first full-list gap after the visible predecessor.
 */
export function rankForTaskDrop(
  activeId: string,
  state: string,
  visibleOrder: readonly string[],
  allTasks: readonly Task[],
): number {
  const index = visibleOrder.indexOf(activeId);
  const previousVisible = index > 0 ? visibleOrder[index - 1] : undefined;
  const nextVisible = index >= 0 && index < visibleOrder.length - 1 ? visibleOrder[index + 1] : undefined;
  const stateTasks = allTasks
    .filter((task) => task.id !== activeId && task.status === state)
    .sort(compareTaskOrder);
  const byId = new Map(stateTasks.map((task) => [task.id, task]));

  let lower = previousVisible ? byId.get(previousVisible) : undefined;
  let upper = nextVisible ? byId.get(nextVisible) : undefined;
  if (lower) {
    const lowerIndex = stateTasks.findIndex((task) => task.id === lower!.id);
    const immediateSuccessor = lowerIndex >= 0 ? stateTasks[lowerIndex + 1] : undefined;
    if (immediateSuccessor) upper = immediateSuccessor;
  } else if (upper) {
    const upperIndex = stateTasks.findIndex((task) => task.id === upper!.id);
    lower = upperIndex > 0 ? stateTasks[upperIndex - 1] : undefined;
  } else {
    lower = stateTasks.at(-1);
  }
  return rankBetween(lower ? effectiveRank(lower) : undefined, upper ? effectiveRank(upper) : undefined);
}

/** Returns a finite, non-zero rank ordered between the supplied boundaries when possible. */
export function rankBetween(lower?: number, upper?: number): number {
  if (lower !== undefined && upper !== undefined && lower < upper) {
    const midpoint = lower + (upper - lower) / 2;
    if (Number.isFinite(midpoint) && midpoint !== 0 && midpoint > lower && midpoint < upper) return midpoint;
  }
  if (lower !== undefined) {
    const step = Math.max(1, Math.abs(lower) * Number.EPSILON * 8);
    const candidate = lower + step;
    if (Number.isFinite(candidate) && candidate !== 0 && (upper === undefined || candidate < upper)) return candidate;
  }
  if (upper !== undefined) {
    if (upper > 0) {
      const candidate = upper / 2;
      if (candidate !== 0 && candidate < upper && (lower === undefined || candidate > lower)) return candidate;
    }
    const step = Math.max(1, Math.abs(upper) * Number.EPSILON * 8);
    const candidate = upper - step;
    if (Number.isFinite(candidate) && candidate !== 0) return candidate;
  }
  return DEFAULT_MANUAL_RANK;
}
