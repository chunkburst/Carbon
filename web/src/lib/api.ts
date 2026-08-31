// Typed client for the Carbon web API. Every call targets a project `path`; the server
// resolves it (falling back to its launch --repo) and reuses mcp.Service, so the web and
// agent front-ends share one rule-set.
import { currentActor } from "@/lib/identity";
import type {
  CarbonConflict,
  CarbonLease,
  CarbonPendingClaim,
  CarbonTaskType,
  CarbonImportance,
} from "@/lib/carbon-api";

export type Check = {
  desc: string;
  cmd?: string;
  type?: string;
  result?: string; // pending | pass | fail
  cwd?: string;
  timeout?: number;
};

// Task evidence is deliberately structured rather than a free-form note. The server
// owns `id`, `createdAt`, and `createdBy` for new entries; clients retain them only
// when editing an existing item. Keeping this DTO in the base task client lets both
// legacy and Carbon task reads expose one stable shape.
export type TaskEvidence = {
  id?: string;
  kind: string;
  value: string;
  label?: string;
  url?: string;
  createdAt?: string;
  createdBy?: string;
};

export type Provenance = {
  id?: string; // stable id, present on note entries only (used to edit/delete)
  who: string;
  at: string;
  did: string;
  text?: string;
  editedAt?: string; // set when a note is edited in place
};

export type ExecutionState = "active" | "stalled" | "awaiting_review";

// Activity health is derived from meaningful task activity and deliberately does
// not replace the workflow status or the live session state.
export type ActivityHealth = "fresh" | "stagnant" | "unknown";

export type SessionLive = {
  session: string;
  heartbeatAt: string;
  progress?: string;
  worktree?: string;
};

export type AgentSession = {
  id: string;
  task: string;
  attempt: string;
  actor: string;
  client?: string;
  model?: string;
  status: "active" | "finished" | "canceled";
  idempotencyKey: string;
  startedAt: string;
  endedAt?: string;
  branch?: string;
  headStarted?: string;
  headFinished?: string;
  summary?: string;
  cancelReason?: string;
  health: "active" | "stalled" | "finished" | "canceled";
  live?: SessionLive;
};

// Run is one parsed check-run log from .carbon/runs (output stays out of the task file).
export type Run = {
  file: string;
  at?: string;
  cmd?: string;
  cwd?: string;
  head?: string;
  exit: number;
  timedout: boolean;
  duration?: string;
  output?: string;
};

export type ChangedFile = {
  status: string;
  path: string;
  oldPath?: string;
};

export type GitCommit = {
  hash: string;
  subject: string;
};

export type GitWarning = {
  kind: string;
  message: string;
};

export type GitContext = {
  available: boolean;
  error?: string;
  branch?: string;
  headStarted?: string;
  headFinished?: string;
  currentHead?: string;
  filesChanged?: ChangedFile[];
  uncommitted?: ChangedFile[];
  commits?: GitCommit[];
  dirty: boolean;
  warnings?: GitWarning[];
};

export type SessionGitContext = {
  session: AgentSession;
  context: GitContext;
};

export type Task = {
  id: string;
  title: string;
  status: string;
  assignee?: string;
  deps?: string[];
  ready: boolean;
  updatedAt?: string; // newest provenance timestamp (list view)
  rank?: number; // manual board ordering (0/absent = unset)
  labels?: string[];
  priority?: string; // "" | low | medium | high | urgent
  parent?: string;
  checks?: Check[];
  blockerReason?: string;
  evidence?: TaskEvidence[];
  provenance?: Provenance[];
  body?: string;
  activeAttempt?: string;
  executionState?: ExecutionState;
  sessionId?: string;
  activityHealth?: ActivityHealth;
  lastMeaningfulAt?: string;
  stagnantAt?: string;
  // Carbon stable v2 is additive. These remain optional while an older local sidecar is
  // in use, allowing the rest of the task UI to keep working unchanged.
  projectId?: string;
  type?: CarbonTaskType;
  importance?: CarbonImportance;
  lease?: CarbonLease;
  conflict?: CarbonConflict;
  pendingClaims?: CarbonPendingClaim[];
  version?: string;
};

export type UpdateFields = {
  priority?: string;
  labels?: string[];
  parent?: string;
  title?: string;
  body?: string;
  checks?: Check[];
  blockerReason?: string;
  evidence?: TaskEvidence[];
};

export type Status = {
  initialized: boolean;
  root: string;
  prefix?: string;
  suggestedPrefix: string;
  states?: string[];
  closed?: string[];
  initial?: string;
  review?: string; // state whose entry runs command checks (alongside closed states)
  checkShell?: string; // shell for cmd checks; empty ⇒ sh (CARBON_SHELL env overrides)
  actor?: string;
  suggestedActor?: string;
  productVersion?: string;
  apiVersion?: string;
  requestedCompatLayer?: string;
  stableCompatLayer?: string;
  carbonVersion?: string;
  taskStagnationAfterSeconds?: number;
  capabilities?: string[];
  scope?: {
    mode?: string;
    home?: string;
    cluster?: string;
    project?: string;
    legacy?: boolean;
  };
};

// A cluster is a lightweight manifest which groups independently-initialized Carbon
// projects. Task APIs stay path-scoped; these types only power project discovery and
// the aggregate switcher UI.
export type ClusterProject = {
  id: string;
  name: string;
  path: string;
  addedAt?: string;
  legacy?: boolean;
  initialized: boolean;
  offline?: boolean;
  prefix?: string;
  tasks: number;
  active: number;
  stalled: number;
  stagnant?: number;
  review: number;
  liveAgents: number;
};

export type Cluster = {
  version: number;
  root: string;
  name: string;
  initialized: boolean;
  legacyAvailable?: boolean;
  projects: ClusterProject[];
};

export type CreateInput = {
  title: string;
  body?: string;
  blockerReason?: string;
  evidence?: TaskEvidence[];
  deps?: string[];
  checks?: Check[];
  labels?: string[];
  priority?: string;
  parent?: string;
};

type WireLease = CarbonLease & { acquired_at?: string; expires_at?: string; renewed_at?: string };
type WirePendingClaim = CarbonPendingClaim & { request_id?: string; requested_at?: string; lease_ttl_seconds?: number };
type WireTaskEvidence = TaskEvidence & { created_at?: string; created_by?: string };

type WireTask = Omit<Task, "lease" | "pendingClaims" | "evidence"> & {
  project_id?: string;
  task_type?: CarbonTaskType;
  blocker_reason?: string;
  evidence?: WireTaskEvidence[];
  lease?: WireLease;
  lease_info?: WireLease;
  conflict_info?: CarbonConflict;
  pendingClaims?: WirePendingClaim[];
  pending_claims?: WirePendingClaim[];
  activity_health?: ActivityHealth;
  last_meaningful_at?: string;
  stagnant_at?: string;
};

// Carbon's persisted frontmatter uses `project_id`; the legacy HTTP DTO uses
// camelCase. Accept both at the boundary so consumers have one stable shape.
function normalizeLease(lease: WireTask["lease"] | undefined): CarbonLease | undefined {
  if (!lease) return undefined;
  return {
    ...lease,
    acquiredAt: lease.acquiredAt ?? lease.acquired_at,
    expiresAt: lease.expiresAt ?? lease.expires_at,
    renewedAt: lease.renewedAt ?? lease.renewed_at,
  };
}

function normalizePendingClaims(claims: WireTask["pendingClaims"] | undefined): CarbonPendingClaim[] | undefined {
  return claims?.map((claim) => ({
    ...claim,
    requestId: claim.requestId ?? claim.request_id,
    requestedAt: claim.requestedAt ?? claim.requested_at,
    leaseTtlSeconds: claim.leaseTtlSeconds ?? claim.lease_ttl_seconds,
  }));
}

function normalizeEvidence(evidence: WireTaskEvidence[] | undefined): TaskEvidence[] | undefined {
  return evidence?.map((item) => ({
    ...item,
    createdAt: item.createdAt ?? item.created_at,
    createdBy: item.createdBy ?? item.created_by,
  }));
}

export function normalizeTask(raw: WireTask): Task {
  return {
    ...raw,
    projectId: raw.projectId ?? raw.project_id,
    type: raw.type ?? raw.task_type,
    blockerReason: raw.blockerReason ?? raw.blocker_reason,
    evidence: normalizeEvidence(raw.evidence),
    lease: normalizeLease(raw.lease ?? raw.lease_info),
    conflict: raw.conflict ?? raw.conflict_info,
    pendingClaims: normalizePendingClaims(raw.pendingClaims ?? raw.pending_claims),
    activityHealth: raw.activityHealth ?? raw.activity_health,
    lastMeaningfulAt: raw.lastMeaningfulAt ?? raw.last_meaningful_at,
    stagnantAt: raw.stagnantAt ?? raw.stagnant_at,
  };
}

const enc = encodeURIComponent;

async function req<T>(method: string, url: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const actor = currentActor();
  if (actor) headers["X-Carbon-Actor"] = enc(actor); // who I am (URL-encoded for non-ASCII)
  const res = await fetch(url, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error(data?.error || `${res.status} ${res.statusText}`);
  return data as T;
}

export const getStatus = (path: string) =>
  req<Status>("GET", `/api/status?path=${enc(path)}`);

export const getCluster = (path: string) =>
  req<Cluster>("GET", `/api/cluster?path=${enc(path)}`);

// POST is deliberately separate from GET: opening a folder upgrades a legacy
// single-project root by creating the cluster manifest without moving `.carbon`.
export const openCluster = (path: string, name?: string) =>
  req<Cluster>("POST", "/api/cluster", { path, name });

export const addClusterProject = (clusterPath: string, path: string, name?: string) =>
  req<Cluster>("POST", "/api/cluster/projects", { clusterPath, path, name });

export const initRepo = (path: string, prefix?: string) =>
  req<Status>("POST", "/api/init", { path, prefix });

export const setCheckShell = (path: string, checkShell: string) =>
  req<Status>("POST", `/api/config?path=${enc(path)}`, { checkShell });

export const listTasks = (path: string) =>
  req<{ tasks: WireTask[] }>("GET", `/api/tasks?path=${enc(path)}`).then((r) =>
    (r.tasks ?? []).map(normalizeTask),
  );

export const getTask = (path: string, id: string) =>
  req<WireTask>("GET", `/api/tasks/${id}?path=${enc(path)}`).then(normalizeTask);

export const createTask = (path: string, input: CreateInput) =>
  req<WireTask>("POST", `/api/tasks?path=${enc(path)}`, input).then(normalizeTask);

export const transitionTask = (path: string, id: string, to: string) =>
  req<WireTask>("POST", `/api/tasks/${id}/transition?path=${enc(path)}`, { to }).then(normalizeTask);

export const claimTask = (path: string, id: string) =>
  req<WireTask>("POST", `/api/tasks/${id}/claim?path=${enc(path)}`).then(normalizeTask);

export const runChecks = (path: string, id: string, only?: number[]) =>
  req<WireTask>("POST", `/api/tasks/${id}/run_checks?path=${enc(path)}`, { only }).then(normalizeTask);

export const addNote = (path: string, id: string, text: string) =>
  req<WireTask>("POST", `/api/tasks/${id}/note?path=${enc(path)}`, { text }).then(normalizeTask);

export const updateTask = (path: string, id: string, fields: UpdateFields) =>
  req<WireTask>("POST", `/api/tasks/${id}/update?path=${enc(path)}`, fields).then(normalizeTask);

export const reorderTask = (path: string, id: string, rank: number) =>
  req<Task>("POST", `/api/tasks/${id}/reorder?path=${enc(path)}`, { rank });

export const getRuns = (path: string, id: string) =>
  req<{ runs: Run[] }>("GET", `/api/tasks/${id}/runs?path=${enc(path)}`).then((r) => r.runs ?? []);

export const getTaskGitContext = (path: string, id: string) =>
  req<{ sessions: SessionGitContext[] }>(
    "GET",
    `/api/tasks/${id}/git_context?path=${enc(path)}`,
  ).then((r) => r.sessions ?? []);

export const listTaskSessions = (path: string, id: string) =>
  req<{ sessions: AgentSession[] }>("GET", `/api/tasks/${id}/sessions?path=${enc(path)}`).then(
    (r) => r.sessions ?? [],
  );

export const listSessions = (path: string) =>
  req<{ sessions: AgentSession[] }>("GET", `/api/sessions?path=${enc(path)}`).then(
    (r) => r.sessions ?? [],
  );

export const attestTask = (path: string, id: string, index: number, pass: boolean) =>
  req<WireTask>("POST", `/api/tasks/${id}/attest?path=${enc(path)}`, { index, pass }).then(normalizeTask);

export const deleteTask = (path: string, id: string) =>
  req<{ id: string; deleted: boolean }>("DELETE", `/api/tasks/${id}?path=${enc(path)}`);

// noteURL addresses a note by its stable id, or by 0-based provenance index for a legacy
// note with no id (server sentinel segment "-" + ?index=).
const noteURL = (path: string, id: string, note?: string, index?: number) => {
  const seg = note ? enc(note) : "-";
  const idx = note ? "" : `&index=${index ?? -1}`;
  return `/api/tasks/${id}/notes/${seg}?path=${enc(path)}${idx}`;
};

export const editNote = (path: string, id: string, text: string, note?: string, index?: number) =>
  req<WireTask>("PATCH", noteURL(path, id, note, index), { text }).then(normalizeTask);

export const deleteNote = (path: string, id: string, note?: string, index?: number) =>
  req<WireTask>("DELETE", noteURL(path, id, note, index)).then(normalizeTask);

// --- Integrations: connect AI agents to this project over MCP ---

export type AgentMode = "auto" | "manual";

// AgentStatus mirrors connect.AgentStatus: an agent in the catalog, whether it looks
// installed on this machine, and whether its config already points at the `carbon` MCP server.
export type AgentStatus = {
  id: string;
  name: string;
  mode: AgentMode;
  installed: boolean;
  connected: boolean;
  targetPath?: string;
  docsURL?: string;
};

// AgentGuide is the manual-setup snippet for one agent: the file content and where it goes.
export type AgentGuide = {
  path?: string;
  lang: string; // "json" | "toml"
  config: string;
};

export const listIntegrations = (path: string) =>
  req<{ agents: AgentStatus[] }>("GET", `/api/connect?path=${enc(path)}`).then((r) => r.agents ?? []);

export const connectAgent = (path: string, agent: string, actor?: string) =>
  req<{ connected: boolean; path: string }>("POST", `/api/connect/${enc(agent)}`, { path, actor });

export const disconnectAgent = (path: string, agent: string) =>
  req<{ connected: boolean; path: string }>("DELETE", `/api/connect/${enc(agent)}?path=${enc(path)}`);

export const agentManual = (path: string, agent: string, actor?: string) =>
  req<AgentGuide>(
    "GET",
    `/api/connect/${enc(agent)}/manual?path=${enc(path)}${actor ? `&actor=${enc(actor)}` : ""}`,
  );
