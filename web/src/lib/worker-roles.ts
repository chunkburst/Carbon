export const CARBON_BUILT_IN_ROLES = ["architect", "task_publisher", "reviewer", "frontend", "backend", "researcher", "planner"] as const;

type Translate = (en: string, zh: string, vars?: Record<string, string | number>) => string;

export function workerRoleLabel(role: string, t: Translate): string {
  const labels: Record<string, [string, string]> = {
    architect: ["Architect", "架构师"],
    task_publisher: ["Task publisher", "任务发布者"],
    reviewer: ["Reviewer", "审核者"],
    frontend: ["Frontend", "前端"],
    backend: ["Backend", "后端"],
    researcher: ["Researcher", "研究者"],
    planner: ["Planner", "规划者"],
  };
  const label = labels[role];
  return label ? t(label[0], label[1]) : role;
}
