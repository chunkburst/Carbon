// React Query hooks over the API. Queries are keyed by project path; mutations invalidate
// the task list (and the affected task) and surface success/failure via sonner toasts —
// crucially, gate refusals (failed checks, unclosed deps) show the backend's reason.

import { useCallback, useEffect, useRef, useState } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import * as api from "./api";
import * as carbon from "./carbon-api";
import type { CreateInput } from "./api";
import { translate } from "@/lib/i18n";
import { statusLabel } from "@/lib/utils";

const tasksKey = (path: string) => ["tasks", path] as const;
const integrationsKey = (path: string) => ["integrations", path] as const;
const taskKey = (path: string, id: string) => ["task", path, id] as const;
const runsKey = (path: string, id: string) => ["runs", path, id] as const;
const sessionsKey = (path: string, id?: string) => ["sessions", path, ...(id ? [id] : [])] as const;
const gitContextKey = (path: string, id: string) => ["git-context", path, id] as const;
export const clusterQueryKey = (root: string) => ["cluster", root] as const;

// Settings panes are intentionally mounted on demand. Keep their mostly-static records warm
// across a quick close/reopen, while mutations still invalidate these exact keys immediately.
const SETTINGS_STATIC_STALE_TIME_MS = 5 * 60_000;
const BACKUP_STATUS_STALE_TIME_MS = 15_000;
const BACKUP_SNAPSHOTS_STALE_TIME_MS = 30_000;

// isGatedStatus reports whether entering `to` runs the task's command checks server-side
// (i.e. the move blocks while checks run). Mirrors the backend gate: closed states or the
// review state. Reads the cached workspace status so callers needn't thread it through.
export function isGatedStatus(qc: QueryClient, path: string, to: string): boolean {
  const st = qc.getQueryData<api.Status>(["status", path]);
  if (!st) return false;
  return (st.closed ?? []).includes(to) || st.review === to;
}

export function useStatus(path: string | null) {
  return useQuery({
    queryKey: ["status", path],
    queryFn: () => api.getStatus(path as string),
    enabled: path !== null,
    retry: false,
  });
}

// Cluster summaries poll at a modest cadence. We deliberately keep an SSE connection
// only for the selected project; this aggregate refresh makes the switcher reflect
// work progressing in sibling projects without opening one stream per project.
export function useCluster(root: string | null) {
  return useQuery({
    queryKey: clusterQueryKey(root ?? ""),
    queryFn: () => api.getCluster(root as string),
    enabled: !!root,
    refetchInterval: 7500,
    // A hidden desktop window does not need to keep re-rendering cluster summaries;
    // the active workspace SSE reconnects and the normal focus refetch catches up.
    refetchIntervalInBackground: false,
    retry: false,
  });
}

export function useTasks(path: string) {
  return useQuery({ queryKey: tasksKey(path), queryFn: () => api.listTasks(path) });
}

export function useTask(path: string, id: string | null) {
  return useQuery({
    queryKey: taskKey(path, id ?? ""),
    queryFn: () => api.getTask(path, id as string),
    enabled: !!id,
  });
}

export function useRuns(path: string, id: string | null) {
  return useQuery({
    queryKey: runsKey(path, id ?? ""),
    queryFn: () => api.getRuns(path, id as string),
    enabled: !!id,
  });
}

export function useTaskGitContext(path: string, id: string | null) {
  return useQuery({
    queryKey: gitContextKey(path, id ?? ""),
    queryFn: () => api.getTaskGitContext(path, id as string),
    enabled: !!id,
  });
}

export function useTaskSessions(path: string, id: string | null) {
  return useQuery({
    queryKey: sessionsKey(path, id ?? ""),
    queryFn: () => api.listTaskSessions(path, id as string),
    enabled: !!id,
  });
}

// useSessions lists every session for the project (powers the tray's live agent rows). Keyed
// under sessionsKey(path) so the SSE `session-changed` invalidation keeps it fresh.
export function useSessions(path: string) {
  return useQuery({
    queryKey: sessionsKey(path, "__all__"),
    queryFn: () => api.listSessions(path),
  });
}

// useTaskEvents subscribes to the server's SSE stream for `path` and invalidates the
// affected React Query caches, so the board and open task reflect changes made by ANY
// actor (including MCP agents in another process), not just this UI's own mutations. One
// EventSource per active path; the browser auto-reconnects on drop.
export function useTaskEvents(path: string, clusterRoot?: string | null) {
  const qc = useQueryClient();
  useEffect(() => {
    const es = new EventSource(`/api/events?path=${encodeURIComponent(path)}`);
    es.onmessage = (e) => {
      let msg: { type: string; id?: string; session?: string };
      try {
        msg = JSON.parse(e.data);
      } catch {
        return;
      }
      // Always refresh the list (covers create/delete). For a single-task change also
      // refresh that task and its runs so check output updates live.
      qc.invalidateQueries({ queryKey: tasksKey(path) });
      if (msg.type === "task-changed" && msg.id) {
        qc.invalidateQueries({ queryKey: taskKey(path, msg.id) });
        qc.invalidateQueries({ queryKey: runsKey(path, msg.id) });
        qc.invalidateQueries({ queryKey: sessionsKey(path, msg.id) });
        qc.invalidateQueries({ queryKey: gitContextKey(path, msg.id) });
      }
      if (msg.type === "session-changed") {
        qc.invalidateQueries({ queryKey: sessionsKey(path) });
        qc.invalidateQueries({ queryKey: ["git-context", path] });
        // Session events carry the session id, not the task id. Refresh open task
        // projections so execution state cannot lag behind the session timeline.
        qc.invalidateQueries({ queryKey: ["task", path] });
      }
      if (clusterRoot) qc.invalidateQueries({ queryKey: clusterQueryKey(clusterRoot) });
    };
    return () => es.close();
  }, [path, clusterRoot, qc]);
}

/** Scoped Carbon SSE invalidates only Carbon caches. This keeps Home/cluster/project
 * state fresh when an MCP agent changes a task without falling back to `path`. */
export function useCarbonTaskEvents(scope: carbon.CarbonScopeInput, enabled = true) {
  const qc = useQueryClient();
  const scopeKey = carbon.carbonScopeKey(scope);
  const eventURL = carbon.carbonEventsURL(scope);
  const eventParts = carbonScopeCacheParts(scope);
  const eventHomeKey = eventParts.homeKey;
  const eventClusterID = eventParts.clusterId;
  const eventProjectID = eventParts.projectId;
  useEffect(() => {
    if (!enabled) return;
    const es = new EventSource(eventURL);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let refreshTaskList = false;
    let refreshAllTasks = false;
    let refreshTrash = false;
    let refreshWorkerStats = false;
    let refreshSearch = false;
    let refreshAllDetailData = false;
    let refreshSessions = false;
    const taskIDs = new Set<string>();

    const flush = () => {
      timer = undefined;
      if (refreshTaskList) qc.invalidateQueries({ queryKey: carbonKey("tasks", scopeKey) });
      if (refreshAllTasks) qc.invalidateQueries({ queryKey: carbonKey("task", scopeKey) });
      else for (const id of taskIDs) qc.invalidateQueries({ queryKey: carbonKey("task", scopeKey, id) });
      if (refreshAllDetailData) {
        qc.invalidateQueries({ queryKey: carbonKey("runs", scopeKey) });
        qc.invalidateQueries({ queryKey: carbonKey("git-context", scopeKey) });
      } else {
        for (const id of taskIDs) {
          qc.invalidateQueries({ queryKey: carbonKey("runs", scopeKey, id) });
          qc.invalidateQueries({ queryKey: carbonKey("git-context", scopeKey, id) });
        }
      }
      if (refreshSessions) qc.invalidateQueries({ queryKey: carbonKey("sessions", scopeKey) });
      else for (const id of taskIDs) qc.invalidateQueries({ queryKey: carbonKey("sessions", scopeKey, id) });
      if (refreshTrash) qc.invalidateQueries({ queryKey: carbonKey("trash", scopeKey) });
      if (refreshWorkerStats) invalidateWorkerMetricsForScope(qc, eventHomeKey, eventClusterID, eventProjectID);
      // Search results are recomputed when the dialog/query next changes. Mark
      // active searches stale without forcing a refetch. Session-only events do not
      // change search membership or text, so they deliberately skip this work.
      if (refreshSearch) qc.invalidateQueries({ queryKey: ["carbon", "search", scopeKey], refetchType: "none" });
      refreshTaskList = false;
      refreshAllTasks = false;
      refreshTrash = false;
      refreshWorkerStats = false;
      refreshSearch = false;
      refreshAllDetailData = false;
      refreshSessions = false;
      taskIDs.clear();
    };

    es.onmessage = (event) => {
      let message: { type?: string; id?: string };
      try {
        message = JSON.parse(event.data) as { type?: string; id?: string };
      } catch {
        return;
      }
      if (message.type === "task-changed" && message.id) {
        refreshTaskList = true;
        taskIDs.add(message.id);
        refreshWorkerStats = true;
        refreshSearch = true;
        // A task write can be a check run, note, or an atomic task/session
        // update. Keep only this task's supplemental detail records fresh.
      } else if (message.type === "session-changed") {
        // Session events carry a session id, not a task id. Refresh open detail
        // projections and the list once after the burst so execution state stays
        // current, without invalidating unrelated trash/search/Worker data.
        refreshTaskList = true;
        refreshAllTasks = true;
        refreshSessions = true;
        refreshAllDetailData = true;
      } else if (message.type === "tasks-changed") {
        refreshTaskList = true;
        refreshAllTasks = true;
        refreshTrash = true;
        refreshWorkerStats = true;
        refreshSearch = true;
        refreshSessions = true;
        refreshAllDetailData = true;
      } else {
        return;
      }
      if (timer === undefined) timer = setTimeout(flush, 120);
    };
    return () => {
      if (timer !== undefined) clearTimeout(timer);
      es.close();
    };
  }, [enabled, eventClusterID, eventHomeKey, eventProjectID, eventURL, qc, scopeKey]);
}

function refresh(qc: QueryClient, path: string, id?: string) {
  qc.invalidateQueries({ queryKey: tasksKey(path) });
  if (id) {
    qc.invalidateQueries({ queryKey: taskKey(path, id) });
    qc.invalidateQueries({ queryKey: gitContextKey(path, id) });
  }
}

const fail = (e: unknown) => {
  if (e instanceof carbon.CarbonAPIError && e.status === 409) {
    const current = e.currentVersion ?? e.currentEtag;
    toast.error(current
      ? translate(
        "Edit conflict: the task changed elsewhere (current version {version}). Refresh and retry.",
        "编辑冲突：任务已被其他个体修改（当前版本 {version}）。请刷新后重试。",
        { version: current.slice(0, 12) },
      )
      : translate(
        "Edit conflict: the task changed elsewhere. Refresh and retry.",
        "编辑冲突：任务已被其他个体修改。请刷新后重试。",
      ));
    return;
  }
  toast.error(e instanceof Error ? e.message : String(e));
};

export function useInitRepo(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (prefix?: string) => api.initRepo(path, prefix),
    onSuccess: (st) => {
      qc.setQueryData(["status", path], st);
      toast.success(translate("Initialized {prefix}", "已初始化 {prefix}", { prefix: st.prefix }));
    },
    onError: fail,
  });
}

export function useSetCheckShell(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (checkShell: string) => api.setCheckShell(path, checkShell),
    onSuccess: (st) => {
      qc.setQueryData(["status", path], st);
      toast.success(translate("Check shell saved", "检查 Shell 已保存"));
    },
    onError: fail,
  });
}

export function useCreateTask(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateInput) => api.createTask(path, input),
    onSuccess: (t) => {
      refresh(qc, path);
      toast.success(translate("Created {id}", "已创建 {id}", { id: t.id }));
    },
    onError: fail,
  });
}

export function useClaim(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.claimTask(path, id),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Claimed {id}", "已认领 {id}", { id: t.id }));
    },
    onError: fail,
  });
}

export function useTransition(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, to }: { id: string; to: string }) => api.transitionTask(path, id, to),
    // Reflect the new status immediately for free (non-gated) moves so the board and open
    // detail update with no lag. Gated moves (review/closed) run command checks server-side and
    // may be refused, so we DON'T fake them — the UI shows a "running checks…" state until the
    // server confirms. onError rolls back, onSettled reconciles with the real document.
    onMutate: async ({ id, to }) => {
      if (isGatedStatus(qc, path, to)) return { id };
      await qc.cancelQueries({ queryKey: tasksKey(path) });
      await qc.cancelQueries({ queryKey: taskKey(path, id) });
      const prevList = qc.getQueryData<api.Task[]>(tasksKey(path));
      const prevTask = qc.getQueryData<api.Task>(taskKey(path, id));
      qc.setQueryData<api.Task[]>(tasksKey(path), (old) =>
        old?.map((t) => (t.id === id ? { ...t, status: to } : t)),
      );
      qc.setQueryData<api.Task>(taskKey(path, id), (old) => (old ? { ...old, status: to } : old));
      return { prevList, prevTask, id };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.prevList !== undefined) qc.setQueryData(tasksKey(path), ctx.prevList);
      if (ctx?.prevTask !== undefined) qc.setQueryData(taskKey(path, ctx.id), ctx.prevTask);
      fail(err);
    },
    onSuccess: (t) =>
      toast.success(
        translate("Moved {id} to {status}", "已将 {id} 移至 {status}", {
          id: t.id,
          status: statusLabel(t.status),
        }),
      ),
    onSettled: (_d, _e, vars) => refresh(qc, path, vars.id),
  });
}

export function useRunChecks(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, only }: { id: string; only?: number[] }) => api.runChecks(path, id, only),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      const failed = (t.checks ?? []).filter((c) => c.result === "fail").length;
      if (failed)
        toast.error(translate("{count} check(s) failed", "{count} 项检查失败", { count: failed }));
      else toast.success(translate("Checks passed", "检查已通过"));
    },
    onError: fail,
  });
}

export function useAttest(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, index, pass }: { id: string; index: number; pass: boolean }) =>
      api.attestTask(path, id, index, pass),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Check attested", "检查已确认"));
    },
    onError: fail,
  });
}

export function useReorder(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, rank }: { id: string; rank: number }) => api.reorderTask(path, id, rank),
    // silent: the board is optimistic; just reconcile on settle.
    onSettled: () => qc.invalidateQueries({ queryKey: tasksKey(path) }),
    onError: fail,
  });
}

export function useUpdateTask(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, fields }: { id: string; fields: import("./api").UpdateFields }) =>
      api.updateTask(path, id, fields),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Updated {id}", "已更新 {id}", { id: t.id }));
    },
    onError: fail,
  });
}

export function useAddNote(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, text }: { id: string; text: string }) => api.addNote(path, id, text),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Note added", "已添加备注"));
    },
    onError: fail,
  });
}

export function useDeleteTask(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteTask(path, id),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: tasksKey(path) });
      qc.removeQueries({ queryKey: taskKey(path, r.id) });
      toast.success(translate("Deleted {id}", "已删除 {id}", { id: r.id }));
    },
    onError: fail,
  });
}

export function useEditNote(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, text, note, index }: { id: string; text: string; note?: string; index?: number }) =>
      api.editNote(path, id, text, note, index),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Note updated", "备注已更新"));
    },
    onError: fail,
  });
}

// --- Integrations ---

export function useIntegrations(path: string) {
  return useQuery({ queryKey: integrationsKey(path), queryFn: () => api.listIntegrations(path) });
}

export function useConnectAgent(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ agent, actor }: { agent: string; actor?: string }) =>
      api.connectAgent(path, agent, actor),
    // Re-detect so the just-connected agent flips to "Connected" without a manual refresh.
    onSuccess: (_r, vars) => {
      qc.invalidateQueries({ queryKey: integrationsKey(path) });
      toast.success(translate("Connected {agent}", "已连接 {agent}", { agent: vars.agent }));
    },
    onError: fail,
  });
}

export function useDisconnectAgent(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agent: string) => api.disconnectAgent(path, agent),
    onSuccess: (_r, agent) => {
      qc.invalidateQueries({ queryKey: integrationsKey(path) });
      toast.success(translate("Disconnected {agent}", "已断开 {agent}", { agent }));
    },
    onError: fail,
  });
}

// useAgentManual fetches a manual-setup snippet on demand (enabled when the guide is open).
export function useAgentManual(path: string, agent: string, actor: string, enabled: boolean) {
  return useQuery({
    queryKey: ["agent-manual", path, agent, actor],
    queryFn: () => api.agentManual(path, agent, actor),
    enabled,
  });
}

export function useDeleteNote(path: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, note, index }: { id: string; note?: string; index?: number }) =>
      api.deleteNote(path, id, note, index),
    onSuccess: (t) => {
      refresh(qc, path, t.id);
      toast.success(translate("Note deleted", "备注已删除"));
    },
    onError: fail,
  });
}

// --- Carbon stable v2 optional extensions -----------------------------------------
//
// Every query below resolves `{ available: false }` for a 404. This lets views render
// an honest upgrade affordance against older sidecars instead of manufacturing data.

const carbonKey = (name: string, ...parts: string[]) => ["carbon", name, ...parts] as const;

const carbonTaskRunsQueryKey = (scope: carbon.CarbonScopeInput, id: string) =>
  carbonKey("runs", carbon.carbonScopeKey(scope), id);
const carbonTaskGitContextQueryKey = (scope: carbon.CarbonScopeInput, id: string) =>
  carbonKey("git-context", carbon.carbonScopeKey(scope), id);
const carbonTaskSessionsQueryKey = (scope: carbon.CarbonScopeInput, id: string) =>
  carbonKey("sessions", carbon.carbonScopeKey(scope), id);
const carbonSessionsQueryKey = (scope: carbon.CarbonScopeInput) =>
  carbonKey("sessions", carbon.carbonScopeKey(scope), "__all__");

type CarbonScopeCacheParts = {
  homeKey: string;
  clusterId: string;
  projectId: string;
};

// React Query keys need a stable, structured home boundary. Do not use one global
// `workers` key: a task write in cluster A must not refetch unrelated Worker views in
// another Carbon Home (or a legacy path).
function carbonScopeCacheParts(scope: carbon.CarbonScopeInput): CarbonScopeCacheParts {
  if (typeof scope === "string") {
    return { homeKey: `legacy:${scope}`, clusterId: "", projectId: "" };
  }
  return {
    homeKey: `home:${scope.home ?? "default"}`,
    clusterId: scope.clusterId ?? "",
    projectId: scope.projectId ?? "",
  };
}

function workerMetricsQueryKey(scope: carbon.CarbonScopeInput, viewScope: "all" | "cluster" | "project") {
  const parts = carbonScopeCacheParts(scope);
  const clusterId = viewScope === "all" ? "" : parts.clusterId;
  const projectId = viewScope === "project" ? parts.projectId : "";
  return carbonKey("workers", parts.homeKey, clusterId, projectId, viewScope);
}

function invalidateWorkerMetricsForScope(
  qc: QueryClient,
  homeKey: string,
  clusterId: string,
  projectId: string,
): void {
  qc.invalidateQueries({
    predicate: (query) => {
      const [namespace, name, cachedHome, cachedCluster, cachedProject] = query.queryKey;
      if (namespace !== "carbon" || name !== "workers" || cachedHome !== homeKey) return false;
      if (!clusterId || cachedCluster === "") return true;
      if (cachedCluster !== clusterId) return false;
      return !projectId || cachedProject === "" || cachedProject === projectId;
    },
  });
}

function invalidateWorkerMetricsForHome(qc: QueryClient, home?: string): void {
  const homeKey = `home:${home ?? "default"}`;
  qc.invalidateQueries({ queryKey: carbonKey("workers", homeKey) });
}

// A project clear changes the current project's metrics plus the aggregate views
// that contain it. Do not invalidate every standalone project in the Home: their
// per-project Worker reports are independent and can stay warm.
function invalidateWorkerMetricsForProjectTaskData(
  qc: QueryClient,
  home: string | undefined,
  clusterId: string | undefined,
  projectId: string,
): void {
  const homeKey = `home:${home ?? "default"}`;
  qc.invalidateQueries({
    predicate: (query) => {
      const [namespace, name, cachedHome, cachedCluster, cachedProject] = query.queryKey;
      if (namespace !== "carbon" || name !== "workers" || cachedHome !== homeKey) return false;

      // Home-wide reports have no project id. A standalone project's scoped
      // report carries an empty cluster id and its own project id.
      if (cachedCluster === "") return cachedProject === "" || cachedProject === projectId;
      // A grouped project's removal also changes that cluster aggregate, but
      // never another cluster or another project's detail report.
      return Boolean(clusterId && cachedCluster === clusterId && (cachedProject === "" || cachedProject === projectId));
    },
  });
}

function workLogListQueryKey(scope: carbon.CarbonScopeInput, filter: carbon.CarbonWorkLogListFilter) {
  const parts = carbonScopeCacheParts(scope);
  return carbonKey(
    "worklogs",
    parts.homeKey,
    parts.clusterId,
    parts.projectId,
    filter.worker ?? "",
    filter.visibility ?? "",
    filter.projectId ?? "",
    filter.taskId ?? "",
    String(filter.limit ?? 100),
  );
}

function workLogQueryKey(scope: carbon.CarbonScopeInput, id: string) {
  const parts = carbonScopeCacheParts(scope);
  return carbonKey("worklog", parts.homeKey, parts.clusterId, parts.projectId, id);
}

// Global-public logs may be read from another cluster in the same Home. A mutation
// therefore invalidates this Home's Work Log views (and no other Home), never every
// Work Log query in the application.
function invalidateCarbonWorkLogLists(qc: QueryClient, scope: carbon.CarbonScopeInput): void {
  const parts = carbonScopeCacheParts(scope);
  qc.invalidateQueries({ queryKey: carbonKey("worklogs", parts.homeKey) });
}

function invalidateCarbonWorkLogDetails(qc: QueryClient, scope: carbon.CarbonScopeInput, exceptID?: string): void {
  const parts = carbonScopeCacheParts(scope);
  qc.invalidateQueries({
    predicate: (query) => {
      const [namespace, name, cachedHome, , , cachedID] = query.queryKey;
      return namespace === "carbon" && name === "worklog" && cachedHome === parts.homeKey && cachedID !== exceptID;
    },
  });
}

function removeCarbonWorkLogDetails(qc: QueryClient, scope: carbon.CarbonScopeInput, id: string): void {
  const parts = carbonScopeCacheParts(scope);
  qc.removeQueries({
    predicate: (query) => {
      const [namespace, name, cachedHome, , , cachedID] = query.queryKey;
      return namespace === "carbon" && name === "worklog" && cachedHome === parts.homeKey && cachedID === id;
    },
  });
}

function homeQueryKey(name: string, home?: string) {
  return carbonKey(name, home ?? "default");
}

function refreshCarbonTasks(
  qc: ReturnType<typeof useQueryClient>,
  path: string,
  scope: carbon.CarbonScopeInput,
): void {
  qc.invalidateQueries({ queryKey: carbonKey("tasks", carbon.carbonScopeKey(scope)) });
  qc.invalidateQueries({ queryKey: carbonKey("task", carbon.carbonScopeKey(scope)) });
  // Legacy callers still keep their established task query fresh. A Carbon caller
  // passes an empty UI storage key and therefore never invalidates or fetches `path`.
  if (path) qc.invalidateQueries({ queryKey: tasksKey(path) });
}

type CarbonTaskListCache = carbon.CarbonOptional<{ tasks?: api.Task[] }>;
type CarbonTaskCache = carbon.CarbonOptional<api.Task>;
type CarbonTaskFieldSnapshot<T> = Array<{
  key: readonly unknown[];
  kind: "list" | "detail";
  value: T;
}>;

function carbonTaskFieldSnapshot<T>(
  qc: QueryClient,
  scope: carbon.CarbonScopeInput,
  id: string,
  read: (task: api.Task) => T,
): CarbonTaskFieldSnapshot<T> {
  const scopeKey = carbon.carbonScopeKey(scope);
  const snapshot: CarbonTaskFieldSnapshot<T> = [];
  for (const [key, result] of qc.getQueriesData<CarbonTaskListCache>({ queryKey: carbonKey("tasks", scopeKey) })) {
    if (!result?.available) continue;
    const task = result.data.tasks?.find((candidate) => candidate.id === id);
    if (task) snapshot.push({ key, kind: "list", value: read(task) });
  }
  for (const [key, result] of qc.getQueriesData<CarbonTaskCache>({ queryKey: carbonKey("task", scopeKey) })) {
    if (!result?.available || result.data.id !== id) continue;
    snapshot.push({ key, kind: "detail", value: read(result.data) });
  }
  return snapshot;
}

function restoreCarbonTaskFieldSnapshot<T>(
  qc: QueryClient,
  id: string,
  snapshot: CarbonTaskFieldSnapshot<T>,
  stillOptimistic: (task: api.Task) => boolean,
  restore: (task: api.Task, value: T) => api.Task,
): void {
  for (const entry of snapshot) {
    if (entry.kind === "list") {
      qc.setQueryData<CarbonTaskListCache>(entry.key, (result) => {
        if (!result?.available) return result;
        return {
          available: true,
          data: {
            ...result.data,
            tasks: result.data.tasks?.map((task) => task.id === id && stillOptimistic(task)
              ? restore(task, entry.value)
              : task),
          },
        };
      });
      continue;
    }
    qc.setQueryData<CarbonTaskCache>(entry.key, (result) => {
      if (!result?.available || result.data.id !== id || !stillOptimistic(result.data)) return result;
      return { available: true, data: restore(result.data, entry.value) };
    });
  }
}

function updateCarbonTaskCaches(
  qc: QueryClient,
  scope: carbon.CarbonScopeInput,
  id: string,
  update: (task: api.Task) => api.Task,
): void {
  const scopeKey = carbon.carbonScopeKey(scope);
  for (const [key, result] of qc.getQueriesData<CarbonTaskListCache>({ queryKey: carbonKey("tasks", scopeKey) })) {
    if (!result?.available) continue;
    qc.setQueryData<CarbonTaskListCache>(key, {
      available: true,
      data: { ...result.data, tasks: result.data.tasks?.map((task) => task.id === id ? update(task) : task) },
    });
  }
  for (const [key, result] of qc.getQueriesData<CarbonTaskCache>({ queryKey: carbonKey("task", scopeKey) })) {
    if (!result?.available || result.data.id !== id) continue;
    qc.setQueryData<CarbonTaskCache>(key, { available: true, data: update(result.data) });
  }
}

export function useCarbonCapabilities(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("capabilities", scopeKey),
    queryFn: () => carbon.getCarbonCapabilities(scope),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useCarbonIntegrations(
  scope: carbon.CarbonScopeInput,
  configProjectId?: string,
  routing: carbon.CarbonMCPRouting = "pinned",
) {
  const scopeKey = carbon.carbonScopeKey(scope);
  // A project can be independent of a cluster.  Requiring a cluster here would
  // make Connect silently disappear for the normal project-first workspace.
  // Project-session routing deliberately binds only the Home; configProjectId is
  // still required by one-click setup solely to locate the agent's config file.
  const scoped = typeof scope !== "string" && Boolean(scope.home && (
    routing === "session" || scope.clusterId || scope.projectId
  ));
  return useQuery({
    queryKey: carbonKey("connect", scopeKey, routing, configProjectId ?? "manual"),
    queryFn: () => carbon.listCarbonIntegrations(scope, configProjectId, routing),
    enabled: scoped,
    retry: false,
  });
}

export function useCarbonAgentGuide(
  scope: carbon.CarbonScopeInput,
  input: { agent: string; actor?: string; configProjectId?: string; routing?: carbon.CarbonMCPRouting },
  enabled: boolean,
) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("connect-guide", scopeKey, input.routing ?? "pinned", input.agent, input.actor ?? "", input.configProjectId ?? "manual"),
    queryFn: () => carbon.getCarbonAgentGuide(scope, input),
    enabled: enabled && Boolean(input.agent),
    retry: false,
  });
}

export function useConnectCarbonAgent(scope: carbon.CarbonScopeInput) {
  const qc = useQueryClient();
  const scopeKey = carbon.carbonScopeKey(scope);
  return useMutation({
    mutationFn: (input: { agent: string; actor?: string; configProjectId?: string; routing?: carbon.CarbonMCPRouting }) => carbon.connectCarbonAgent(scope, input),
    onSuccess: (result, input) => {
      qc.invalidateQueries({ queryKey: carbonKey("connect", scopeKey, input.routing ?? "pinned") });
      if (!result.available) {
        toast.message(translate("Carbon Connect needs a newer sidecar", "Carbon Connect 需要更新的 sidecar"));
      } else if (result.data.manual) {
        toast.message(translate("Manual MCP guide is ready", "手动 MCP 指南已生成"));
      } else {
        toast.success(translate("Connected {agent}", "已连接 {agent}", { agent: input.agent }));
      }
    },
    onError: fail,
  });
}

export function useDisconnectCarbonAgent(scope: carbon.CarbonScopeInput) {
  const qc = useQueryClient();
  const scopeKey = carbon.carbonScopeKey(scope);
  return useMutation({
    mutationFn: (input: { agent: string; configProjectId?: string; routing?: carbon.CarbonMCPRouting }) => carbon.disconnectCarbonAgent(scope, input),
    onSuccess: (result, input) => {
      qc.invalidateQueries({ queryKey: carbonKey("connect", scopeKey, input.routing ?? "pinned") });
      if (result.available) toast.success(translate("Disconnected {agent}", "已断开 {agent}", { agent: input.agent }));
    },
    onError: fail,
  });
}

export function useWorkerMetrics(scope: carbon.CarbonScopeInput, viewScope: "all" | "cluster" | "project") {
  return useQuery({
    queryKey: workerMetricsQueryKey(scope, viewScope),
    queryFn: () => carbon.getWorkerMetrics({ scope, viewScope }),
    // Computing delivery cycles walks durable task provenance. Keep the last scoped
    // report warm while users move between Worker/log/task views; Carbon SSE and the
    // lifecycle mutations below still invalidate it immediately when data changes.
    staleTime: 15_000,
    refetchOnWindowFocus: false,
    retry: false,
  });
}

export function useWorkerAliases(home?: string) {
  return useQuery({
    queryKey: carbonKey("worker-aliases", home ?? "default"),
    queryFn: () => carbon.getCarbonWorkerAliases(home),
    enabled: Boolean(home),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function usePatchWorkerAlias(home?: string) {
  const qc = useQueryClient();
  const queryKey = carbonKey("worker-aliases", home ?? "default");
  return useMutation({
    mutationFn: (input: { actor: string; alias: string }) => carbon.patchCarbonWorkerAlias(home, input),
    onSuccess: (result, input) => {
      if (!result.available) {
        toast.message(translate("Worker aliases need a newer Carbon sidecar", "Worker 别名需要更新的 Carbon sidecar"));
        return;
      }
      // The endpoint returns the whole alias map, but preserve a responsive local
      // update even while another client changes a different actor concurrently.
      qc.setQueryData<carbon.CarbonOptional<carbon.CarbonWorkerAliasesResponse>>(queryKey, (old) => {
        const aliases = { ...(old?.available ? old.data.aliases : result.data.aliases) };
        const actor = input.actor.trim();
        const alias = input.alias.trim();
        if (alias) aliases[actor] = alias;
        else delete aliases[actor];
        return { available: true, data: { aliases } };
      });
      void qc.invalidateQueries({ queryKey });
      toast.success(input.alias.trim()
        ? translate("Alias saved", "别名已保存")
        : translate("Alias removed", "别名已移除"));
    },
    onError: fail,
  });
}

// Worker reset/delete only affect home-global metric lifecycle metadata. Neither
// mutation touches task data, so task/list caches intentionally remain untouched.
export function useResetCarbonWorker(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (actor: string) => carbon.resetCarbonWorker(home, actor),
    onSuccess: (result) => {
      if (!result.available) return;
      invalidateWorkerMetricsForHome(qc, home);
    },
    onError: fail,
  });
}

export function useDeleteCarbonWorker(home?: string) {
  const qc = useQueryClient();
  const aliasKey = carbonKey("worker-aliases", home ?? "default");
  return useMutation({
    mutationFn: (actor: string) => carbon.deleteCarbonWorker(home, actor),
    onSuccess: (result) => {
      if (!result.available) return;
      invalidateWorkerMetricsForHome(qc, home);
      // Delete clears its display alias as part of the durable Worker lifecycle.
      qc.invalidateQueries({ queryKey: aliasKey });
    },
    onError: fail,
  });
}

export function useCarbonWorkLogs(
  scope: carbon.CarbonScopeInput,
  filter: carbon.CarbonWorkLogListFilter = {},
  enabled = true,
) {
  return useQuery({
    queryKey: workLogListQueryKey(scope, filter),
    queryFn: () => carbon.listCarbonWorkLogs(scope, filter),
    enabled,
    retry: false,
  });
}

export function useCarbonWorkLog(scope: carbon.CarbonScopeInput, id?: string, enabled = true) {
  return useQuery({
    queryKey: workLogQueryKey(scope, id ?? ""),
    queryFn: () => carbon.getCarbonWorkLog(scope, id as string),
    enabled: enabled && Boolean(id),
    retry: false,
  });
}

export function useCreateCarbonWorkLog(scope: carbon.CarbonScopeInput) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: carbon.CarbonWorkLogCreate) => carbon.createCarbonWorkLog(scope, input),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.setQueryData(workLogQueryKey(scope, result.data.id), result);
      invalidateCarbonWorkLogLists(qc, scope);
    },
    onError: fail,
  });
}

export function useUpdateCarbonWorkLog(scope: carbon.CarbonScopeInput) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: carbon.CarbonWorkLogUpdate }) =>
      carbon.updateCarbonWorkLog(scope, id, input),
    onSuccess: (result, variables) => {
      if (!result.available) return;
      qc.setQueryData(workLogQueryKey(scope, variables.id), result);
      invalidateCarbonWorkLogLists(qc, scope);
      invalidateCarbonWorkLogDetails(qc, scope, variables.id);
    },
    onError: fail,
  });
}

export function useDeleteCarbonWorkLog(scope: carbon.CarbonScopeInput) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, expectedVersion }: { id: string; expectedVersion: string }) =>
      carbon.deleteCarbonWorkLog(scope, id, { expectedVersion }),
    onSuccess: (result, variables) => {
      if (!result.available) return;
      removeCarbonWorkLogDetails(qc, scope, variables.id);
      invalidateCarbonWorkLogLists(qc, scope);
    },
    onError: fail,
  });
}

export function useCarbonSearch(scope: carbon.CarbonScopeInput, query: string, includeCluster = false) {
  const q = query.trim();
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("search", scopeKey, includeCluster ? "cluster" : "project", q),
    queryFn: () => carbon.searchCarbonTasks({ scope, query: q, includeCluster }),
    enabled: q.length > 0,
    retry: false,
  });
}

export function useCarbonTrash(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("trash", scopeKey),
    queryFn: () => carbon.listTrash(scope),
    retry: false,
  });
}

export function useCarbonSavedViews(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("views", scopeKey),
    queryFn: () => carbon.listCarbonViews(scope),
    retry: false,
  });
}

export function useCarbonTemplates(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("templates", scopeKey),
    queryFn: () => carbon.listCarbonTemplates(scope),
    retry: false,
  });
}

export function useCarbonTaskTypes(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("types", scopeKey),
    queryFn: () => carbon.listCarbonTaskTypes(scope),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useCarbonTasks(scope: carbon.CarbonScopeInput, includeCluster = false, enabled = true, marketHistory = false) {
	const scopeKey = carbon.carbonScopeKey(scope);
	return useQuery({
		queryKey: carbonKey("tasks", scopeKey, includeCluster ? "cluster" : "project", ...(marketHistory ? ["market-history"] : [])),
		queryFn: () => carbon.listCarbonTasks(scope, includeCluster, marketHistory),
		enabled,
		retry: false,
	});
}

export function useCarbonTask(scope: carbon.CarbonScopeInput, id?: string, includeCluster = false) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("task", scopeKey, id ?? "", includeCluster ? "cluster" : "project"),
    queryFn: () => carbon.getCarbonTask(scope, id!, includeCluster),
    enabled: Boolean(id),
    retry: false,
  });
}

// Carbon detail records retain the legacy server DTOs, but their cache identity is
// explicitly Home/cluster/project scoped. This prevents a same-named task in a
// sibling project from ever reusing runs, session timelines, or Git context.
export function useCarbonTaskRuns(scope: carbon.CarbonScopeInput, id?: string) {
  return useQuery({
    queryKey: carbonTaskRunsQueryKey(scope, id ?? ""),
    queryFn: () => carbon.getCarbonTaskRuns(scope, id as string),
    enabled: Boolean(id),
    retry: false,
  });
}

export function useCarbonTaskGitContext(scope: carbon.CarbonScopeInput, id?: string) {
  return useQuery({
    queryKey: carbonTaskGitContextQueryKey(scope, id ?? ""),
    queryFn: () => carbon.getCarbonTaskGitContext(scope, id as string),
    enabled: Boolean(id),
    retry: false,
  });
}

export function useCarbonTaskSessions(scope: carbon.CarbonScopeInput, id?: string) {
  return useQuery({
    queryKey: carbonTaskSessionsQueryKey(scope, id ?? ""),
    queryFn: () => carbon.listCarbonTaskSessions(scope, id as string),
    enabled: Boolean(id),
    retry: false,
  });
}

export function useCarbonSessions(scope: carbon.CarbonScopeInput, enabled = true) {
  return useQuery({
    queryKey: carbonSessionsQueryKey(scope),
    queryFn: () => carbon.listCarbonSessions(scope),
    enabled,
    retry: false,
  });
}

function refreshCarbonTaskSupplement(
  qc: QueryClient,
  path: string,
  scope: carbon.CarbonScopeInput,
  id: string,
  input: { runs?: boolean; sessions?: boolean; gitContext?: boolean } = {},
): void {
  refreshCarbonTasks(qc, path, scope);
  if (input.runs) qc.invalidateQueries({ queryKey: carbonTaskRunsQueryKey(scope, id) });
  if (input.sessions) qc.invalidateQueries({ queryKey: carbonTaskSessionsQueryKey(scope, id) });
  if (input.gitContext) qc.invalidateQueries({ queryKey: carbonTaskGitContextQueryKey(scope, id) });
}

export function useRunCarbonTaskChecks(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, only }: { id: string; only?: number[] }) => carbon.runCarbonTaskChecks(scope, id, only),
    onSuccess: (result, variables) => {
      if (!result.available) {
        toast.message(translate("Task checks need a current Carbon sidecar", "任务检查需要当前版本的 Carbon sidecar"));
        return;
      }
      refreshCarbonTaskSupplement(qc, path, scope, variables.id, { runs: true });
      const failed = (result.data.checks ?? []).filter((check) => check.result === "fail").length;
      if (failed) toast.error(translate("{count} check(s) failed", "{count} 项检查失败", { count: failed }));
      else toast.success(translate("Checks passed", "检查已通过"));
    },
    onError: fail,
  });
}

export function useAttestCarbonTask(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, index, pass }: { id: string; index: number; pass: boolean }) =>
      carbon.attestCarbonTask(scope, id, index, pass),
    onSuccess: (result, variables) => {
      if (!result.available) {
        toast.message(translate("Manual checks need a current Carbon sidecar", "手动检查需要当前版本的 Carbon sidecar"));
        return;
      }
      refreshCarbonTaskSupplement(qc, path, scope, variables.id);
      toast.success(translate("Check attested", "检查已确认"));
    },
    onError: fail,
  });
}

export function useAddCarbonTaskNote(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, text }: { id: string; text: string }) => carbon.addCarbonTaskNote(scope, id, text),
    onSuccess: (result, variables) => {
      if (!result.available) {
        toast.message(translate("Notes need a current Carbon sidecar", "备注需要当前版本的 Carbon sidecar"));
        return;
      }
      refreshCarbonTaskSupplement(qc, path, scope, variables.id);
      toast.success(translate("Note added", "已添加备注"));
    },
    onError: fail,
  });
}

export function useEditCarbonTaskNote(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, text, note, index }: { id: string; text: string; note?: string; index?: number }) =>
      carbon.editCarbonTaskNote(scope, id, text, note, index),
    onSuccess: (result, variables) => {
      if (!result.available) {
        toast.message(translate("Notes need a current Carbon sidecar", "备注需要当前版本的 Carbon sidecar"));
        return;
      }
      refreshCarbonTaskSupplement(qc, path, scope, variables.id);
      toast.success(translate("Note updated", "备注已更新"));
    },
    onError: fail,
  });
}

export function useDeleteCarbonTaskNote(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, note, index }: { id: string; note?: string; index?: number }) =>
      carbon.deleteCarbonTaskNote(scope, id, note, index),
    onSuccess: (result, variables) => {
      if (!result.available) {
        toast.message(translate("Notes need a current Carbon sidecar", "备注需要当前版本的 Carbon sidecar"));
        return;
      }
      refreshCarbonTaskSupplement(qc, path, scope, variables.id);
      toast.success(translate("Note deleted", "备注已删除"));
    },
    onError: fail,
  });
}

export function usePatchCarbonTask(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, fields }: { id: string; fields: carbon.CarbonTaskPatch }) =>
      carbon.patchCarbonTask(scope, id, fields),
    onSuccess: (result, vars) => {
      if (!result.available) {
        toast.message(translate("Carbon task fields need a stable v2 sidecar", "Carbon 任务字段需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Updated {id}", "已更新 {id}", { id: vars.id }));
    },
    onError: fail,
  });
}

// The Carbon variants keep scope on every write. Their variable shapes intentionally
// match the legacy Kanban hooks so a board can switch transports without branching.
export function useTransitionCarbonTask(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, to }: { id: string; to: string }) => carbon.transitionCarbonTask(scope, id, to),
    onMutate: async ({ id, to }) => {
      const scopeKey = carbon.carbonScopeKey(scope);
      await qc.cancelQueries({ queryKey: carbonKey("tasks", scopeKey) });
      await qc.cancelQueries({ queryKey: carbonKey("task", scopeKey) });
      const previous = carbonTaskFieldSnapshot(qc, scope, id, (task) => task.status);
      updateCarbonTaskCaches(qc, scope, id, (task) => ({ ...task, status: to }));
      return { previous };
    },
    onError: (error, variables, context) => {
      if (context?.previous) {
        restoreCarbonTaskFieldSnapshot(
          qc,
          variables.id,
          context.previous,
          (task) => task.status === variables.to,
          (task, status) => ({ ...task, status }),
        );
      }
      fail(error);
    },
    onSuccess: (result, variables, context) => {
      if (!result.available) {
        if (context?.previous) {
          restoreCarbonTaskFieldSnapshot(
            qc,
            variables.id,
            context.previous,
            (task) => task.status === variables.to,
            (task, status) => ({ ...task, status }),
          );
        }
        toast.message(translate("Kanban changes need a Carbon sidecar", "看板变更需要 Carbon sidecar"));
        return;
      }
      // Commit only the field owned by this mutation. A concurrent rank or detail
      // edit must not be overwritten by an older full-task response.
      updateCarbonTaskCaches(qc, scope, variables.id, (task) => ({ ...task, status: result.data.status }));
      toast.success(translate("Moved {id} to {status}", "已将 {id} 移至 {status}", {
        id: result.data.id,
        status: statusLabel(result.data.status),
      }));
    },
    onSettled: () => refreshCarbonTasks(qc, path, scope),
  });
}

export function useReorderCarbonTask(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, rank }: { id: string; rank: number }) => carbon.reorderCarbonTask(scope, id, rank),
    onMutate: async ({ id, rank }) => {
      const scopeKey = carbon.carbonScopeKey(scope);
      await qc.cancelQueries({ queryKey: carbonKey("tasks", scopeKey) });
      await qc.cancelQueries({ queryKey: carbonKey("task", scopeKey) });
      const previous = carbonTaskFieldSnapshot(qc, scope, id, (task) => task.rank);
      updateCarbonTaskCaches(qc, scope, id, (task) => ({ ...task, rank }));
      return { previous };
    },
    onError: (error, variables, context) => {
      if (context?.previous) {
        restoreCarbonTaskFieldSnapshot(
          qc,
          variables.id,
          context.previous,
          (task) => task.rank === variables.rank,
          (task, rank) => ({ ...task, rank }),
        );
      }
      fail(error);
    },
    onSuccess: (result, variables, context) => {
      if (!result.available) {
        if (context?.previous) {
          restoreCarbonTaskFieldSnapshot(
            qc,
            variables.id,
            context.previous,
            (task) => task.rank === variables.rank,
            (task, rank) => ({ ...task, rank }),
          );
        }
        toast.message(translate("Kanban changes need a Carbon sidecar", "看板变更需要 Carbon sidecar"));
        return;
      }
      updateCarbonTaskCaches(qc, scope, variables.id, (task) => ({ ...task, rank: result.data.rank }));
    },
    onSettled: () => refreshCarbonTasks(qc, path, scope),
  });
}

function useCarbonLeaseMutation<T>(
  path: string,
  scope: carbon.CarbonScopeInput,
  mutationFn: (input: T) => Promise<carbon.CarbonOptional<unknown>>,
  success: [string, string],
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn,
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Lease actions need a stable v2 sidecar", "租约操作需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate(success[0], success[1]));
    },
    onError: fail,
  });
}

export function useClaimCarbonLease(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ttlSeconds, reason, expectedVersion }: { id: string; ttlSeconds?: number; reason?: string; expectedVersion?: string }) =>
      carbon.claimLease(scope, id, { ttlSeconds, reason, expectedVersion }),
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Lease actions need a stable v2 sidecar", "租约操作需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      if (result.data.pending) {
        toast.message(translate("Lease request is waiting for approval", "租约申请正在等待审批"));
      } else {
        toast.success(translate("Lease claimed", "已认领租约"));
      }
    },
    onError: fail,
  });
}

export function useRenewCarbonLease(path: string, scope: carbon.CarbonScopeInput = path) {
  return useCarbonLeaseMutation(path, scope, ({ id, leaseId, ttlSeconds, expectedVersion }: { id: string; leaseId: string; ttlSeconds?: number; expectedVersion?: string }) =>
    carbon.renewLease(scope, id, { leaseId, ttlSeconds, expectedVersion }), ["Lease renewed", "租约已续期"]);
}

export function useReleaseCarbonLease(path: string, scope: carbon.CarbonScopeInput = path) {
  return useCarbonLeaseMutation(path, scope, ({ id, leaseId, reason, expectedVersion }: { id: string; leaseId: string; reason: string; expectedVersion?: string }) =>
    carbon.releaseLease(scope, id, { leaseId, reason, expectedVersion }), ["Lease released", "租约已释放"]);
}

export function useReassignCarbonLease(path: string, scope: carbon.CarbonScopeInput = path) {
  return useCarbonLeaseMutation(path, scope, ({ id, assignee, force, reason, expectedVersion }: { id: string; assignee: string; force?: boolean; reason?: string; expectedVersion?: string }) =>
    carbon.reassignCarbonTask(scope, id, { assignee, force, reason, expectedVersion }), ["Lease reassigned", "租约已重新分配"]);
}

export function useApproveCarbonLease(path: string, scope: carbon.CarbonScopeInput = path) {
  return useCarbonLeaseMutation(path, scope, ({ id, requestId, approve, reason, expectedVersion }: { id: string; requestId: string; approve: boolean; reason?: string; expectedVersion?: string }) =>
    carbon.approveLeaseClaim(scope, id, { requestId, approve, reason, expectedVersion }), ["Lease claim reviewed", "租约申请已处理"]);
}

export function useCreateCarbonTask(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (
      input: carbon.CarbonTaskFields & {
        title: string;
        body?: string;
        deps?: string[];
        labels?: string[];
        priority?: string;
        parent?: string;
      },
    ) => carbon.createCarbonTask(scope, input),
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Carbon task fields need a stable v2 sidecar", "Carbon 任务字段需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Task created", "任务已创建"));
    },
    onError: fail,
  });
}

export function useBulkCarbonPatch(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: carbon.CarbonBulkPatch) => carbon.bulkPatchCarbonTasks(scope, patch),
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Bulk updates need a stable v2 sidecar", "批量更新需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Tasks updated", "任务已更新"));
    },
    onError: fail,
  });
}

export function useBulkCarbonMove(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: carbon.CarbonBulkMove) =>
      carbon.bulkMoveCarbonTasks(scope, input),
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Bulk moves need a stable v2 sidecar", "批量移动需要 stable v2 sidecar"));
        return;
      }
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Tasks moved", "任务已移动"));
    },
    onError: fail,
  });
}

export function useCreateCarbonView(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; query: carbon.CarbonViewQuery; id?: string }) => carbon.createCarbonView(scope, input),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("views", scopeKey) });
    },
    onError: fail,
  });
}

export function useDeleteCarbonView(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => carbon.deleteCarbonView(scope, id),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("views", scopeKey) });
    },
    onError: fail,
  });
}

export function useCreateCarbonTemplate(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: Omit<carbon.CarbonTaskTemplate, "id" | "version"> & { id?: string }) =>
      carbon.createCarbonTemplate(scope, input),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("templates", scopeKey) });
    },
    onError: fail,
  });
}

export function useCreateCarbonTaskType(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { key: string; displayName?: string }) => carbon.createCarbonTaskType(scope, input),
    onSuccess: (result) => {
      if (result.available) {
        qc.invalidateQueries({ queryKey: carbonKey("types", scopeKey) });
        toast.success(translate("Task type registered", "任务类型已注册"));
      }
    },
    onError: fail,
  });
}

export function useInstantiateCarbonTemplate(path: string, scope: carbon.CarbonScopeInput = path) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input?: Parameters<typeof carbon.instantiateCarbonTemplate>[2] }) =>
      carbon.instantiateCarbonTemplate(scope, id, input),
    onSuccess: (result) => {
      if (result.available) refreshCarbonTasks(qc, path, scope);
    },
    onError: fail,
  });
}

export function useRestoreTrash(path: string, scope: carbon.CarbonScopeInput = path) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, projectId, expectedVersion }: { id: string; projectId?: string; expectedVersion?: string }) =>
      carbon.restoreTrashItem(scope, id, { projectId, expectedVersion }),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("trash", scopeKey) });
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Task restored", "任务已恢复"));
    },
    onError: fail,
  });
}

export function useTrashCarbonTasks(path: string, scope: carbon.CarbonScopeInput = path) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { ids: string[]; reason?: string; expectedVersions?: Record<string, string> }) =>
      carbon.trashCarbonTasks(scope, input),
    onSuccess: (result) => {
      if (!result.available) {
        toast.message(translate("Trash needs a stable v2 sidecar", "回收站需要 stable v2 sidecar"));
        return;
      }
      qc.invalidateQueries({ queryKey: carbonKey("trash", scopeKey) });
      refreshCarbonTasks(qc, path, scope);
      toast.success(translate("Tasks moved to trash", "任务已移入回收站"));
    },
    onError: fail,
  });
}

export function useEmptyTrash(path: string, scope: carbon.CarbonScopeInput = path) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => carbon.emptyTrash(scope),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("trash", scopeKey) });
      toast.success(translate("Trash emptied", "垃圾站已清空"));
    },
    onError: fail,
  });
}

export function useBackupConfig(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  return useQuery({
    queryKey: carbonKey("backup-config", scopeKey),
    queryFn: () => carbon.getBackupConfig(scope),
    staleTime: SETTINGS_STATIC_STALE_TIME_MS,
    retry: false,
  });
}

export function useBackupSnapshots(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  return useQuery({
    queryKey: carbonKey("backup-snapshots", scopeKey),
    queryFn: () => carbon.listBackupSnapshots(scope),
    staleTime: BACKUP_SNAPSHOTS_STALE_TIME_MS,
    retry: false,
  });
}

export function useBackupStatus(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  return useQuery({
    queryKey: carbonKey("backup-status", scopeKey),
    queryFn: () => carbon.getBackupStatus(scope),
    staleTime: BACKUP_STATUS_STALE_TIME_MS,
    retry: false,
  });
}

export function useCarbonConfig(scope: carbon.CarbonScopeInput, enabled = true) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("config", scopeKey),
    queryFn: () => carbon.getCarbonConfig(scope),
    enabled,
    staleTime: SETTINGS_STATIC_STALE_TIME_MS,
    // Identity Mode may be toggled by another Carbon client. Until config changes
    // join the scoped event stream, keep the compatibility boundary live alongside
    // the Worker identity registry rather than leaving this browser stale for 5m.
    refetchInterval: enabled ? 7_500 : false,
    refetchIntervalInBackground: false,
    retry: false,
  });
}

export function useSaveCarbonConfig(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: carbon.CarbonConfigUpdate) => carbon.saveCarbonConfig(scope, input),
    onSuccess: (result, input) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("config", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("worker-identities", scopeKey) });
      toast.success(input.identityMode !== undefined
        ? translate("Identity mode updated", "身份模式已更新")
        : translate("Trash retention saved", "垃圾站保留期限已保存"));
    },
    onError: fail,
  });
}

export function useCarbonWorkerIdentities(scope: carbon.CarbonScopeInput, enabled = true) {
  const scopeKey = carbon.carbonScopeKey(scope);
  return useQuery({
    queryKey: carbonKey("worker-identities", scopeKey),
    queryFn: () => carbon.listCarbonWorkerIdentities(scope),
    enabled,
    staleTime: SETTINGS_STATIC_STALE_TIME_MS,
    // Identity claims may arrive through MCP rather than this browser. Until the
    // scoped SSE contract grows an identity event, keep the roster reasonably live.
    refetchInterval: enabled ? 7_500 : false,
    refetchIntervalInBackground: false,
    retry: false,
  });
}

export function useUpdateCarbonWorkerIdentity(scope: carbon.CarbonScopeInput) {
  const scopeKey = carbon.carbonScopeKey(scope);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ actor, input }: { actor: string; input: carbon.CarbonWorkerIdentityUpdate }) =>
      carbon.updateCarbonWorkerIdentity(scope, actor, input),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("worker-identities", scopeKey) });
      toast.success(translate("Worker identity saved", "Worker 身份已保存"));
    },
    onError: fail,
  });
}

export function useSaveBackupConfig(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (config: carbon.CarbonBackupConfig) => carbon.putBackupConfig(scope, config),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-config", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("backup-status", scopeKey) });
      toast.success(translate("Backup configuration saved", "备份配置已保存"));
    },
    onError: fail,
  });
}

export function useCreateBackupSnapshot(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => carbon.createBackupSnapshot(scope),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-snapshots", scopeKey) });
      toast.success(translate("Snapshot created", "快照已创建"));
    },
    onError: fail,
  });
}

export function useRunBackupNow(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => carbon.runBackupNow(scope),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-snapshots", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("backup-status", scopeKey) });
      toast.success(result.data.created
        ? translate("Local snapshot completed", "本地快照已完成")
        : translate("Local content is unchanged", "本地内容未变化"));
    },
    onError: fail,
  });
}

export function usePruneBackupSnapshots(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => carbon.pruneBackupSnapshots(scope),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-snapshots", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("backup-status", scopeKey) });
      toast.success(translate("Local snapshot retention applied", "已应用本地快照保留策略"));
    },
    onError: fail,
  });
}

export function useSetBackupContinuousAuthorization(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => carbon.setBackupContinuousAuthorization(scope, enabled),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-config", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("backup-status", scopeKey) });
      toast.success(result.data.profile.continuousAuthorization
        ? translate("Continuous remote authorization saved", "已保存持续远程授权")
        : translate("Continuous remote authorization revoked", "已撤销持续远程授权"));
    },
    onError: fail,
  });
}

export function useUploadBackupSnapshot(scope: carbon.CarbonHomeScope) {
  const scopeKey = scope.home;
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (snapshotId: string) => carbon.uploadBackupSnapshot(scope, snapshotId),
    onSuccess: (result) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("backup-snapshots", scopeKey) });
      qc.invalidateQueries({ queryKey: carbonKey("backup-status", scopeKey) });
      toast.success(translate("Encrypted snapshot upload completed", "加密快照上传已完成"));
    },
    onError: fail,
  });
}

export function useVerifyBackup(scope: carbon.CarbonHomeScope) {
  return useMutation({ mutationFn: (snapshotId: string) => carbon.verifyBackup(scope, snapshotId), onError: fail });
}

export function useRestorePlan(scope: carbon.CarbonHomeScope) {
  return useMutation({
    mutationFn: (snapshotId: string) => carbon.createRestorePlan(scope, snapshotId),
    onError: fail,
  });
}

export function useHomeDoctor(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ repair = false }: { repair?: boolean } = {}) => carbon.getHomeDoctor(home, repair),
    onSuccess: (result) => {
      if (result.available && result.data.applied) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useLegacyMigrationReceipts(home?: string) {
  return useQuery({
    queryKey: carbonKey("legacy-migration", "receipts", home ?? "default"),
    queryFn: () => carbon.getLegacyMigrationReceipts(home),
    retry: false,
  });
}

// Home manifest hooks use stable cluster/project ids. They deliberately do not feed
// the legacy cluster cache, so Carbon mode cannot accidentally call /api/cluster.
export function useCarbonHome(home?: string) {
  return useQuery({
    queryKey: carbonKey("home", home ?? "default"),
    queryFn: () => carbon.getCarbonHome(home),
    retry: false,
  });
}

type CarbonHomeCatalogUpdate = {
  pending: boolean;
  apply: () => void;
};

function homeManifestSignature(snapshot: carbon.CarbonOptional<carbon.CarbonHome> | undefined): string {
  if (!snapshot?.available) return "unavailable";
  return JSON.stringify({ initialized: snapshot.data.initialized, manifest: snapshot.data.manifest ?? null });
}

// Stage catalog changes from another Home client. The active Home query is not
// invalidated or replaced until a caller reaches a safe UI boundary and calls apply.
// queryHome owns the original React Query key; resolvedHome is only for SSE routing.
export function useCarbonHomeCatalogUpdates(
  queryHome: string | undefined,
  resolvedHome: string | undefined,
  capabilities: readonly string[] | undefined,
  liveSnapshot: carbon.CarbonOptional<carbon.CarbonHome> | undefined,
): CarbonHomeCatalogUpdate {
  const qc = useQueryClient();
  const [pending, setPending] = useState(false);
  const stagedRef = useRef<carbon.CarbonOptional<carbon.CarbonHome> | undefined>(undefined);
  const liveSignatureRef = useRef(homeManifestSignature(liveSnapshot));
  const fetchingRef = useRef(false);
  const refetchRequestedRef = useRef(false);
  const fetchAndStageRef = useRef<(() => void) | undefined>(undefined);
  const scopeKey = `${queryHome ?? "default"}\u0000${resolvedHome ?? ""}`;
  const scopeKeyRef = useRef(scopeKey);
  const liveSignature = homeManifestSignature(liveSnapshot);

  useEffect(() => {
    scopeKeyRef.current = scopeKey;
  }, [scopeKey]);

  useEffect(() => {
    const previousLiveSignature = liveSignatureRef.current;
    liveSignatureRef.current = liveSignature;
    if (!stagedRef.current) return;
    // A successful live-query update is newer authority than any snapshot staged
    // against its previous value (for example, a local project mutation may finish
    // while an external update is waiting behind the switcher). Never let the older
    // staged value roll that fresh catalog back when the switcher is opened later.
    if (
      homeManifestSignature(stagedRef.current) === liveSignature ||
      liveSignature !== previousLiveSignature
    ) {
      stagedRef.current = undefined;
      setPending(false);
    }
  }, [liveSignature]);

  useEffect(() => {
    stagedRef.current = undefined;
    fetchingRef.current = false;
    refetchRequestedRef.current = false;
    setPending(false);
  }, [queryHome, resolvedHome]);

  const fetchAndStage = useCallback(() => {
    if (fetchingRef.current) {
      refetchRequestedRef.current = true;
      return;
    }
    const requestedScope = scopeKey;
    fetchingRef.current = true;
    void carbon.getCarbonHome(queryHome)
      .then((snapshot) => {
        if (scopeKeyRef.current !== requestedScope || !snapshot.available) return;
        if (homeManifestSignature(snapshot) === liveSignatureRef.current) {
          stagedRef.current = undefined;
          setPending(false);
          return;
        }
        // Keep the rendered query untouched, but replace an older staged catalog so
        // A then B changes made before the user opens the switcher apply as B.
        stagedRef.current = snapshot;
        setPending(true);
      })
      // EventSource is the long-lived retry path. A failed fetch must preserve the
      // currently rendered catalog and must not turn this hook into a polling loop.
      .catch(() => undefined)
      .finally(() => {
        if (scopeKeyRef.current !== requestedScope) return;
        fetchingRef.current = false;
        if (refetchRequestedRef.current) {
          refetchRequestedRef.current = false;
          fetchAndStageRef.current?.();
        }
      });
  }, [queryHome, scopeKey]);

  useEffect(() => {
    fetchAndStageRef.current = fetchAndStage;
    return () => {
      if (fetchAndStageRef.current === fetchAndStage) fetchAndStageRef.current = undefined;
    };
  }, [fetchAndStage]);

  const enabled = Boolean(resolvedHome && capabilities?.includes("home-events"));
  const eventURL = resolvedHome ? carbon.carbonHomeEventsURL(resolvedHome) : "";
  useEffect(() => {
    if (!enabled || !eventURL) return;
    const events = new EventSource(eventURL);
    let timer: ReturnType<typeof setTimeout> | undefined;
    events.onmessage = (event) => {
      let message: { type?: string };
      try {
        message = JSON.parse(event.data) as { type?: string };
      } catch {
        return;
      }
      if (message.type !== "catalog-changed") return;
      if (timer !== undefined) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined;
        if (fetchingRef.current) refetchRequestedRef.current = true;
        else fetchAndStage();
      }, 120);
    };
    return () => {
      if (timer !== undefined) clearTimeout(timer);
      events.close();
    };
  }, [enabled, eventURL, fetchAndStage]);

  const apply = useCallback(() => {
    const staged = stagedRef.current;
    if (!staged) return;
    // Use queryHome, not resolvedHome: an implicit launch remains cached at
    // `carbon/home/default` after the server reveals its canonical root.
    qc.setQueryData(homeQueryKey("home", queryHome), staged);
    stagedRef.current = undefined;
    setPending(false);
  }, [qc, queryHome]);

  return { pending, apply };
}

export function useCarbonCatalogPresentation(home?: string) {
  return useQuery({
    queryKey: homeQueryKey("catalog-presentation", home),
    queryFn: () => carbon.getCarbonCatalogPresentation(home),
    enabled: Boolean(home),
    // Icons are static presentation metadata; mutations replace this exact cache.
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useSetCarbonCatalogIcon(home?: string) {
  const qc = useQueryClient();
  const queryKey = homeQueryKey("catalog-presentation", home);
  return useMutation({
    mutationFn: (input: { target: carbon.CarbonCatalogPresentationTarget; id: string; icon: carbon.CarbonCatalogIcon | null }) =>
      carbon.setCarbonCatalogIcon(home, input),
    onSuccess: (result) => {
      if (!result.available) return;
      // PUT returns the complete presentation map, so update only this home cache
      // instead of broadly invalidating the catalog or task workspace.
      qc.setQueryData(queryKey, result);
    },
    onError: fail,
  });
}

export function useUploadCarbonCatalogAsset(home?: string) {
  const qc = useQueryClient();
  const queryKey = homeQueryKey("catalog-presentation", home);
  return useMutation({
    mutationFn: (input: { target: carbon.CarbonCatalogPresentationTarget; id: string; file: Blob }) =>
      carbon.uploadCarbonCatalogAsset(home, input),
    onSuccess: (result) => {
      if (!result.available) return;
      // Asset bytes are not represented in the token presentation document, but
      // invalidating the exact home key lets future sidecars add asset metadata
      // without leaving a stale catalog behind.
      qc.invalidateQueries({ queryKey });
    },
    onError: fail,
  });
}

export function useDeleteCarbonCatalogAsset(home?: string) {
  const qc = useQueryClient();
  const queryKey = homeQueryKey("catalog-presentation", home);
  return useMutation({
    mutationFn: (input: { target: carbon.CarbonCatalogPresentationTarget; id: string }) =>
      carbon.deleteCarbonCatalogAsset(home, input),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey });
    },
    onError: fail,
  });
}

// Component-facing aliases retain the concise product naming used by the catalog UI.
export function useCatalogPresentation(home?: string) {
  return useCarbonCatalogPresentation(home);
}

export function useSetCatalogPresentationIcon(home?: string) {
  return useSetCarbonCatalogIcon(home);
}

export function useEnsureCarbonHome() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (home?: string) => carbon.ensureCarbonHome(home),
    onSuccess: (result, home) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useCreateHomeCluster(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { name: string; slug?: string; description?: string }) => carbon.createHomeCluster({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useUpdateHomeCluster(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId: string; name?: string; slug?: string; description?: string }) =>
      carbon.updateHomeCluster({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useAddHomeProject(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId?: string; name: string; slug?: string; description?: string; kind?: string; sourcePath: string }) =>
      carbon.addHomeProject({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useRelinkHomeProject(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId?: string; projectId: string; sourcePath: string }) =>
      carbon.relinkHomeProject({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

export function useUpdateHomeProject(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId?: string; projectId: string; name?: string; slug?: string; description?: string; kind?: string }) =>
      carbon.updateHomeProject({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
    },
    onError: fail,
  });
}

// Clearing task data is intentionally narrower than deleting a project. It keeps
// catalog metadata, Worker identity, and Work Logs in place while every cache
// that can project this project's task history is refreshed.
function invalidateClearedHomeProjectTaskData(
  qc: QueryClient,
  home: string | undefined,
  clusterId: string | undefined,
  projectId: string,
): void {
  const scopes: carbon.CarbonScope[] = [
    { home, clusterId, projectId },
    ...(clusterId ? [{ home, clusterId }] : []),
    { home },
  ];
  const scopeKeys = new Set(scopes.map((scope) => carbon.carbonScopeKey(scope)));
  for (const scopeKey of scopeKeys) {
    qc.invalidateQueries({ queryKey: carbonKey("tasks", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("task", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("runs", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("sessions", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("git-context", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("trash", scopeKey) });
    qc.invalidateQueries({ queryKey: carbonKey("search", scopeKey) });
    // Reserved for summary panels that use a dedicated task-statistics cache.
    // Keeping this exact scope key ready avoids a broad cache reset when one is added.
    qc.invalidateQueries({ queryKey: carbonKey("stats", scopeKey) });
  }
  invalidateWorkerMetricsForProjectTaskData(qc, home, clusterId, projectId);
}

export function useClearHomeProjectTaskData(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId?: string; projectId: string; name: string }) =>
      carbon.clearHomeProjectTaskData({ home, projectId: input.projectId, name: input.name }),
    onSuccess: (result, input) => {
      if (!result.available) return;
      invalidateClearedHomeProjectTaskData(qc, home, input.clusterId, input.projectId);
      toast.success(translate("Project task data cleared", "项目任务数据已清空"));
    },
    onError: fail,
  });
}

// Deleting a catalog project changes both project selection and every task projection
// for that stable ID. The Home cache drives the current-project fallback, presentation
// can retain harmless orphan icons, and the scoped task caches must not keep a deleted
// target visible while the catalog query is refetching.
export function useDeleteHomeProject(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clusterId?: string; projectId: string; name: string; deleteData: boolean }) =>
      carbon.deleteHomeProject({
        home,
        projectId: input.projectId,
        name: input.name,
        deleteData: input.deleteData,
      }),
    onSuccess: (result, input) => {
      if (!result.available) return;
      qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
      qc.invalidateQueries({ queryKey: homeQueryKey("catalog-presentation", home) });
      invalidateClearedHomeProjectTaskData(qc, home, input.clusterId, input.projectId);
      toast.success(input.deleteData
        ? translate("Project removed and Carbon task data cleared", "项目已移出目录，并已清除其 Carbon 任务数据")
        : translate("Project removed; task data retained", "项目已移出目录，任务数据保留"));
    },
    onError: fail,
  });
}

export function useLegacyMigrationPreflight(home: string | undefined, legacyCluster: string) {
  return useQuery({
    queryKey: carbonKey("legacy-migration", "preflight", home ?? "default", legacyCluster),
    queryFn: () => carbon.getLegacyMigrationPreflight(home, legacyCluster),
    enabled: Boolean(legacyCluster.trim()),
    retry: false,
  });
}

export function usePreviewLegacyMigration(home?: string) {
  return useMutation({
    mutationFn: (legacyCluster: string) => carbon.previewLegacyMigration(home, legacyCluster),
    onError: fail,
  });
}

export function useApplyLegacyMigration(home?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { legacyCluster: string; expectedDigest: string; configPolicy?: string }) =>
      carbon.applyLegacyMigration({ home, ...input }),
    onSuccess: (result) => {
      if (result.available) {
        qc.invalidateQueries({ queryKey: carbonKey("home", home ?? "default") });
        qc.invalidateQueries({ queryKey: carbonKey("legacy-migration", "preflight", home ?? "default") });
      }
    },
    onError: fail,
  });
}
