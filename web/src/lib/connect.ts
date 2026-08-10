// Shared builders for connecting agents to a project/task: the carbon:// deep link, the MCP
// connect command, and a ready-to-paste agent prompt. Pure string building (no deps), reused by
// the Connect-agent dialog and the task "Copy link" / "Copy as agent prompt" actions.
import type { Task } from "@/lib/api";

const enc = encodeURIComponent;

// taskDeepLink is the shareable carbon:// URL that opens the app to a task.
export function taskDeepLink(path: string, id: string): string {
  return `carbon://task/${id}?repo=${enc(path)}`;
}

export type CarbonTaskDeepLinkScope = {
  homeId?: string;
  clusterId?: string;
  workspaceProjectId: string;
  taskProjectId?: string;
  taskScope?: "cluster";
};

// Stable Carbon links route by catalog IDs instead of an ambiguous source path.
// The legacy repo link above remains available for non-Home workspaces.
export function carbonTaskDeepLink(scope: CarbonTaskDeepLinkScope, id: string): string {
  const query = new URLSearchParams();
  if (scope.homeId?.trim()) query.set("homeId", scope.homeId.trim());
  if (scope.clusterId?.trim()) query.set("cluster", scope.clusterId.trim());
  query.set("project", scope.workspaceProjectId.trim());
  if (scope.taskScope === "cluster" && scope.clusterId?.trim()) {
    query.set("taskScope", "cluster");
  } else if (scope.taskProjectId?.trim() && scope.taskProjectId.trim() !== scope.workspaceProjectId.trim()) {
    query.set("taskProject", scope.taskProjectId.trim());
  }
  return `carbon://task/${enc(id)}?${query.toString()}`;
}

// mcpHttpUrl is the running app's MCP endpoint for a project + actor.
export function mcpHttpUrl(base: string, path: string, actor: string): string {
  return `${base}/mcp?repo=${enc(path)}&actor=${enc(actor)}`;
}

// mcpAddCommand is the copy-paste `claude mcp add` command for the HTTP transport.
export function mcpAddCommand(base: string, path: string, actor: string): string {
  return `claude mcp add --transport http carbon "${mcpHttpUrl(base, path, actor)}"`;
}

// agentPromptForTask renders a task as a prompt an agent can act on: context + how to connect.
export function agentPromptForTask(task: Task, path: string, actor: string, base: string): string {
  const who = actor.startsWith("agent:") ? actor : "agent:claude-1";
  const lines: string[] = [`# ${task.id} — ${task.title}`, "", `Status: ${task.status}`];
  if (task.deps?.length) lines.push(`Depends on: ${task.deps.join(", ")}`);
  if (task.checks?.length) {
    lines.push("", "Checks:");
    for (const c of task.checks) lines.push(`- ${c.desc}${c.cmd ? ` — \`${c.cmd}\`` : " (manual)"}`);
  }
  if (task.body?.trim()) lines.push("", task.body.trim());
  lines.push(
    "",
    "---",
    "Connect to this project over MCP (Carbon must be running; command: `carbon`):",
    "```",
    mcpAddCommand(base, path, who),
    "```",
    `Then work task ${task.id}. Open it: ${taskDeepLink(path, task.id)}`,
  );
  return lines.join("\n");
}
