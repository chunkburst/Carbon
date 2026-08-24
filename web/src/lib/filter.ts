import type { Filter } from "@/components/AppSidebar";
import type { Status, Task } from "@/lib/api";
import { translate } from "@/lib/i18n";
export { effectiveRank } from "@/lib/task-order";

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
