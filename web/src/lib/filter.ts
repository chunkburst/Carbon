import type { Filter } from "@/components/AppSidebar";
import type { Status, Task } from "@/lib/api";
import { translate } from "@/lib/i18n";

export const FILTER_LABEL: Record<Filter, string> = {
  get all() {
    return translate("All tasks", "所有任务");
  },
  get active() {
    return translate("Active", "进行中");
  },
  get stalled() {
    return translate("Stalled", "已停滞");
  },
  get review() {
    return translate("Awaiting review", "等待审核");
  },
  get backlog() {
    return translate("Backlog", "待办");
  },
  get ready() {
    return translate("Ready", "就绪");
  },
};

// matches applies the base view filter (the sidebar nav) to a task.
export function matches(t: Task, filter: Filter, status: Status): boolean {
  const closed = status.closed ?? [];
  switch (filter) {
    case "active":
      return !(status.closed ?? []).includes(t.status) && t.status !== status.initial;
    case "stalled":
      return t.executionState === "stalled";
    case "review":
      return t.executionState === "awaiting_review";
    case "backlog":
      return t.status === status.initial;
    case "ready":
      return t.ready && !closed.includes(t.status);
    default:
      return true;
  }
}

// Lowercase Crockford base32 alphabet, matching the Go minter (store/id.go). Strictly
// ascending, so decoding preserves chronological order.
const CROCKFORD = "0123456789abcdefghjkmnpqrstvwxyz";

// ID_EPOCH_SEC is 2024-01-01T00:00:00Z in Unix seconds — the base for the current id format
// (mirror of store.idEpoch in internal/store/id.go).
const ID_EPOCH_SEC = 1704067200;

// effectiveRank gives every task a comparable ordering value: its manual rank if set, else a
// creation-order proxy derived from its id so unranked tasks order by creation until first
// dragged. Three id eras, all normalized to one scale:
//   - legacy counter (`PROJ-001`) → its small integer, so it sorts ahead of any time id;
//   - legacy time id (16-char suffix) → 10-char prefix is ms-since-1970, i.e. absolute ms;
//   - current time id (10-char suffix) → 6-char prefix is seconds-since-2024, converted to
//     absolute ms so old and new time ids interleave correctly.
export function effectiveRank(t: Task): number {
  if (t.rank) return t.rank;
  const suffix = t.id.slice(t.id.indexOf("-") + 1);
  if (/^\d+$/.test(suffix)) return Number(suffix); // legacy counter id
  const isCurrent = suffix.length <= 10; // current format is 10 chars; legacy time ids were 16
  const timeChars = isCurrent ? 6 : 10;
  let v = 0;
  for (let i = 0; i < timeChars && i < suffix.length; i++) {
    const d = CROCKFORD.indexOf(suffix[i]);
    if (d < 0) return 0;
    v = v * 32 + d;
  }
  return isCurrent ? (v + ID_EPOCH_SEC) * 1000 : v; // both → absolute ms
}
