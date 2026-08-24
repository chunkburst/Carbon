// Carbon stable v2 extension contract.
//
// The base Carbon API deliberately remains small and file-compatible. Carbon-only
// capabilities live behind `/api/carbon/*` so a newer web bundle can keep working
// with an older sidecar: a 404 means "not installed", never fabricated data.
import { currentActor } from "@/lib/identity";
import {
  normalizeTask,
  type AgentSession,
  type Check,
  type Run,
  type SessionGitContext,
  type Task,
  type TaskEvidence,
} from "@/lib/api";

export type CarbonFeature =
  | "task_fields"
  | "bulk"
  | "workers"
  | "search"
  | "trash"
  | "leases"
  | "views"
  | "templates"
  | "work_logs"
  | "worker_registry"
  | "catalog_icons"
  | "task_evidence";

export type CarbonCapabilities = {
  version: string;
  features: string[];
};

// A Carbon scope never overloads legacy `path`: a new home/cluster/project request
// gets explicit query keys, while a compatibility request carries only `path`.
export type CarbonScope = {
  home?: string;
  clusterId?: string;
  projectId?: string;
  legacyPath?: string;
};

export type CarbonScopeInput = CarbonScope | string;

export function legacyCarbonScope(path: string): CarbonScope {
  return { legacyPath: path };
}

function normalizeScope(input: CarbonScopeInput): CarbonScope {
  return typeof input === "string" ? legacyCarbonScope(input) : input;
}

export function carbonScopeKey(input: CarbonScopeInput): string {
  const scope = normalizeScope(input);
  return [scope.home ?? "", scope.clusterId ?? "", scope.projectId ?? "", scope.legacyPath ?? ""].join("|");
}

function scopedURL(endpoint: string, input: CarbonScopeInput, extra?: Record<string, string | undefined>): string {
  const scope = normalizeScope(input);
  const params = new URLSearchParams();
  if (scope.home) params.set("home", scope.home);
  if (scope.clusterId) params.set("cluster", scope.clusterId);
  if (scope.projectId) params.set("project", scope.projectId);
  // Never append path alongside home/cluster/project; server treats it as legacy root.
  if (!scope.home && !scope.clusterId && !scope.projectId && scope.legacyPath) params.set("path", scope.legacyPath);
  for (const [key, value] of Object.entries(extra ?? {})) if (value !== undefined) params.set(key, value);
  const suffix = params.toString();
  return suffix ? `${endpoint}?${suffix}` : endpoint;
}

function backupURL(endpoint: string, scope: CarbonHomeScope): string {
  // Do not pass through a broader Carbon scope. The backup API rejects it and a
  // home-only request makes the boundary explicit at every call site.
  return scopedURL(endpoint, { home: scope.home });
}

// The server advertises a string list so it can introduce capabilities without a
// coordinated web deploy. Keep the aliases here, at the HTTP boundary.
export function hasCarbonFeature(capabilities: CarbonCapabilities | undefined, feature: CarbonFeature): boolean {
  if (!capabilities) return false;
  const aliases: Record<CarbonFeature, string[]> = {
    task_fields: ["task_fields", "taskFields", "tasks", "carbon-0.4"],
    bulk: ["bulk"],
    workers: ["workers", "worker_stats", "worker-stats"],
    search: ["search"],
    trash: ["trash"],
    leases: ["leases", "lease"],
    views: ["views"],
    templates: ["templates"],
    work_logs: ["work-logs", "work_logs", "workLogs"],
    worker_registry: ["worker-registry", "worker_registry", "workerRegistry", "worker-analytics"],
    catalog_icons: ["catalog-icons", "catalog_icons", "catalogIcons"],
    task_evidence: ["task-evidence", "task_evidence", "taskEvidence", "task-blocker"],
  };
  return aliases[feature].some((name) => capabilities.features.includes(name));
}

export function carbonEventsURL(scope: CarbonScopeInput): string {
  return scopedURL("/api/events", scope);
}

// Home metadata has its own stream. The resolved Home root identifies the watcher,
// while callers may still keep their Home query under the launch-time default key.
export function carbonHomeEventsURL(home: string): string {
  return `/api/home/events?home=${enc(home)}`;
}

// The built-in vocabularies are deliberately narrow. The `(string & {})` tail keeps
// server-configured custom types valid without weakening autocomplete for the defaults.
export type CarbonTaskType = "foundation" | "library" | "patch" | "extension" | "plugin" | (string & {});
export type CarbonImportance = "core" | "important" | "normal" | "optional" | "experimental" | (string & {});

export type CarbonLease = {
  id?: string;
  holder?: string;
  acquiredAt?: string;
  expiresAt?: string;
  renewedAt?: string;
  renewable?: boolean;
};

export type CarbonConflict = {
  state?: "open" | "resolved" | (string & {});
  message?: string;
  fields?: string[];
  detectedAt?: string;
};

export type CarbonPendingClaim = {
  requestId?: string;
  actor?: string;
  assignee?: string;
  requestedAt?: string;
  leaseTtlSeconds?: number;
  reason?: string;
};

export type CarbonLeaseClaimResponse = {
  task?: Task;
  pending: boolean;
  request?: CarbonPendingClaim;
};

// These keys follow the Carbon HTTP DTO. The on-disk/frontmatter spelling may be
// `project_id`, but the server boundary is camelCase. `type` is a task
// classification, not a JavaScript type tag.
export type CarbonTaskFields = {
  projectId?: string;
  type?: CarbonTaskType;
  importance?: CarbonImportance;
  blockerReason?: string;
  evidence?: TaskEvidence[];
  lease?: CarbonLease;
  conflict?: CarbonConflict;
  pendingClaims?: CarbonPendingClaim[];
  version?: string;
};

// Single-task edits preserve the server's normal task update contract. Carbon
// callers deliberately do not use `projectId` or `assignee` here: project moves
// require the audited bulk-move primitive and ownership uses a lease. `projectId`
// remains in the type solely for legacy compatibility with older callers.
export type CarbonTaskPatch = Pick<CarbonTaskFields, "projectId" | "type" | "importance"> & {
  title?: string;
  body?: string;
  priority?: string;
  labels?: string[];
  parent?: string;
  deps?: string[];
  checks?: Check[];
  blockerReason?: string;
  evidence?: TaskEvidence[];
  expectedVersion?: string;
};

// A named Carbon alias makes the proof DTO discoverable next to the Carbon task API,
// while retaining the identical base-task shape for legacy reads.
export type CarbonTaskEvidence = TaskEvidence;

// Proof and blocker edits stay task-specific: each proof has server-owned audit
// metadata, so bulk mutation would be ambiguous and is intentionally not exposed.
export type CarbonBulkPatch = Omit<CarbonTaskPatch, "blockerReason" | "evidence"> & {
  ids: string[];
  expectedVersions?: Record<string, string>;
  status?: string;
  priority?: string;
  labels?: string[];
};

export type CarbonBulkMove = {
  ids: string[];
  projectId: string;
  clusterWide?: boolean;
  expectedVersions?: Record<string, string>;
  parent?: string;
  force?: boolean;
  reason?: string;
};

export type CarbonWorkerMetric = {
  actor: string;
  active: number;
  completed: number;
  completed_by_priority?: Partial<Record<string, number>>;
  completedByPriority?: Partial<Record<string, number>>;
  average_cycle_seconds?: number;
  averageCycleSeconds?: number;
  reopen_rate?: number;
  reopenRate?: number;
  cycle_samples?: number;
  cycleSamples?: number;
  last_activity?: string;
  lastActivity?: string;
  recent_work?: CarbonWorkerRecentWork[];
  recentWork?: CarbonWorkerRecentWork[];
};

export type CarbonWorkersResponse = {
  scope?: {
    mode?: string;
    home?: string;
    clusterId?: string;
    projectId?: string;
    root?: string;
    sourceOffline?: boolean;
    legacy?: boolean;
  };
  workers: CarbonWorkerMetric[];
  aggregate?: CarbonWorkerAggregate;
};

export type CarbonWorkerRecentWork = {
  taskId: string;
  title: string;
  status: string;
  clusterId?: string;
  projectId?: string;
  activity: string;
  at: string;
};

// Aggregate values describe the selected Worker view (all, one cluster, or one
// project), not a second Worker. This is where project/cluster completion-cycle data
// is exposed for the Worker screen.
export type CarbonWorkerAggregate = {
  taskCount: number;
  active: number;
  completed: number;
  open: number;
  averageCycleSeconds: number;
  cycleSamples: number;
  reopened: number;
  reopenRate: number;
};

// Registry records are home-global lifecycle metadata. Reset/delete actions update
// only this registry; they never edit task files, assignments, or provenance.
export type CarbonWorkerRegistryRecord = {
  actor: string;
  createdAt: string;
  updatedAt: string;
  resetAt?: string;
  deletedAt?: string;
};

export type CarbonWorkerRegistryMutation = {
  worker: CarbonWorkerRegistryRecord;
};

export type CarbonWorkerAliasesResponse = {
  aliases: Record<string, string>;
};

// Project-store identities are intentionally separate from the home-global Worker
// lifecycle registry. A Worker may cover several task types while each task keeps
// its single scalar `type` field for backwards compatibility.
export type CarbonWorkerIdentity = {
  actor: string;
  role: string;
  types: string[];
  claimedAt: string;
  updatedAt: string;
  changedBy: string;
  reason?: string;
};

export type CarbonWorkerIdentityListResponse = {
  modeEnabled: boolean;
  records: CarbonWorkerIdentity[];
};

export type CarbonWorkerIdentityMutationResponse = {
  modeEnabled: boolean;
  record: CarbonWorkerIdentity;
};

export type CarbonWorkerIdentityUpdate = {
  role: string;
  types: string[];
  reason?: string;
};

export type CarbonWorkLogVisibility = "worker_private" | "project_public" | "global_public";

export type CarbonWorkLogCoordination = {
  version: 1;
  recipients?: string[];
  thread?: string;
};

// Work Logs are audit records, not task provenance. Their visibility is enforced by
// the sidecar; the local human UI receives the complete record so it can audit every
// attribute, while Worker-private reads remain isolated for other Worker clients.
export type CarbonWorkLog = {
  id: string;
  worker: string;
  visibility: CarbonWorkLogVisibility;
  clusterId: string;
  projectId?: string;
  taskId?: string;
  title: string;
  body?: string;
  tags?: string[];
  // Server-owned. Generic Work Log create/update requests cannot attach or mutate
  // this envelope; only the append-only identity draft primitive can create it.
  coordination?: CarbonWorkLogCoordination;
  createdAt: string;
  createdBy: string;
  updatedAt: string;
  updatedBy: string;
  // Version is the durable optimistic-concurrency token. HTTP also returns it as
  // an ETag; callers may pass either representation as expectedVersion.
  version?: string;
};

export type CarbonWorkLogListFilter = {
  worker?: string;
  visibility?: CarbonWorkLogVisibility;
  projectId?: string;
  taskId?: string;
  limit?: number;
};

export type CarbonWorkLogCreate = {
  visibility: CarbonWorkLogVisibility;
  projectId?: string;
  taskId?: string;
  title: string;
  body?: string;
  tags?: string[];
};

export type CarbonWorkLogUpdate = Partial<CarbonWorkLogCreate> & {
  // Required for every replace/delete operation. Pass either the response `version`
  // or the quoted ETag returned by the sidecar.
  expectedVersion: string;
};

export type CarbonWorkLogListResponse = {
  worklogs: CarbonWorkLog[];
};

export type CarbonSearchResult = {
  clusterId?: string;
  task: Task;
  score?: number;
  highlights?: { field: string; excerpt: string }[];
};

export type CarbonAgentStatus = {
  id: string;
  name: string;
  mode: "auto" | "manual";
  installed: boolean;
  connected: boolean;
  targetPath?: string;
  docsURL?: string;
};

export type CarbonAgentGuide = {
  path?: string;
  lang: string;
  config: string;
};

export type CarbonIntegrations = {
  agents: CarbonAgentStatus[];
  manual?: boolean;
  reason?: string;
};

export type CarbonConnectResult = {
  connected: boolean;
  path?: string;
  manual?: boolean;
  guide?: CarbonAgentGuide;
  reason?: string;
};

export type CarbonViewQuery = {
  text?: string;
  project_id?: string;
  cluster_id?: string;
  type?: string;
  importance?: string;
  status?: string;
  assignee?: string;
  labels?: string[];
};

export type CarbonSavedView = {
  id: string;
  name: string;
  query: CarbonViewQuery;
  created_at?: string;
  updated_at?: string;
  version?: string;
};

export type CarbonTaskTemplate = {
  id: string;
  name: string;
  title: string;
  body?: string;
  project_id?: string;
  cluster_wide?: boolean;
  type: CarbonTaskType;
  importance: CarbonImportance;
  priority?: string;
  labels?: string[];
  deps?: string[];
  checks?: Check[];
  parent?: string;
  version?: string;
};

export type CarbonTaskTypeDefinition = {
  key: string;
  display_name?: string;
  labels?: string[];
  created_at?: string;
  created_by?: string;
};

export type CarbonTrashItem = {
  id: string;
  title: string;
  project_id?: string;
  type?: CarbonTaskType;
  importance?: CarbonImportance;
  assignee?: string;
  labels?: string[];
  trash?: {
    trashed_at?: string;
    trashed_by?: string;
    reason?: string;
    original_project_id?: string;
  };
  version?: string;
  etag?: string;
};

// Backup is deliberately home-scoped: it snapshots the complete `.carbon` management
// plane and must never inherit a cluster, project, or legacy path scope.
export type CarbonHomeScope = {
  home: string;
};

// This is the entire public backup configuration DTO. References are opaque; no
// plaintext credentials or server-owned upload metadata are represented here.
export type CarbonBackupProfile = {
  backend: "s3" | "cos" | (string & {});
  enabled: boolean;
  continuousAuthorization?: boolean;
  bucket?: string;
  prefix?: string;
  region?: string;
  endpoint?: string;
  usePathStyle?: boolean;
  allowInsecureEndpoint?: boolean;
  credentialRef?: string;
  encryption: boolean;
  encryptionKeyRef?: string;
};

export type CarbonBackupLocalSchedule = {
  enabled: boolean;
  intervalHours: number;
  onStart: boolean;
  keepLast: number;
  keepDays: number;
};

export type CarbonBackupConfig = {
  profile: CarbonBackupProfile;
  local: CarbonBackupLocalSchedule;
};

export type CarbonBackupStatus = {
  sourceId: string;
  source: string;
  local: {
    configured: boolean;
    enabled: boolean;
    operational: boolean;
    intervalHours?: number;
    onStart?: boolean;
    keepLast?: number;
    keepDays?: number;
    lastRunAt?: string;
    lastSuccessAt?: string;
    lastSnapshotId?: string;
    lastSnapshotAt?: string;
    lastPruneAt?: string;
    lastPruned?: number;
  };
  remote: {
    configured: boolean;
    enabled: boolean;
    continuousAuthorization?: boolean;
    operational: boolean;
    lastUpload?: string;
    lastRemoteSnapshotId?: string;
    lastRemoteSnapshotAt?: string;
    lastRemoteFailureAt?: string;
    lastRemoteError?: string;
  };
};

export type CarbonConfig = {
  scope?: { home?: string; clusterId?: string; projectId?: string };
  checkShell?: string;
  trashRetentionDays: number;
  identityMode: boolean;
};

export type CarbonConfigUpdate = {
  trashRetentionDays?: number;
  identityMode?: boolean;
};

export type CarbonBackupSnapshot = {
  id: string;
  manifest_key: string;
};

export type CarbonBackupManifest = {
  created_at?: string;
  source_id?: string;
  files?: Array<{ path: string; sha256: string; size: number; mode: number }>;
};

export type CarbonBackupSnapshotInfo = {
  snapshot: CarbonBackupSnapshot;
  manifest: CarbonBackupManifest;
};

export type CarbonBackupVerification = {
  snapshot: CarbonBackupSnapshot;
  manifest: CarbonBackupManifest;
  verified: boolean;
};

export type CarbonBackupUpload = {
  snapshot: CarbonBackupSnapshot;
  uploaded: boolean;
  verified: boolean;
  lastUpload?: string;
};

export type CarbonBackupRun = {
  snapshot: CarbonBackupSnapshot;
  created: boolean;
  skipped: boolean;
  prune: { retained: number; pruned: number; objectsPruned?: number };
};

export type CarbonBackupPrune = {
  retained: number;
  pruned: number;
  objectsPruned?: number;
};

export type CarbonRestorePlan = {
  snapshot: CarbonBackupSnapshot;
  verified: boolean;
  files?: number;
  restore?: string;
};

export type CarbonProjectSource = {
  path?: string;
  aliases?: string[];
  fingerprint?: string;
  lastSeen?: string;
};

export type CarbonHomeProject = {
  id: string;
  name: string;
  slug?: string;
  description?: string;
  slugAliases?: string[];
  kind?: string;
  source?: CarbonProjectSource;
  createdAt?: string;
};

export type CarbonHomeProjectDelete = {
  projectId: string;
  projectName: string;
  clusterId?: string;
  standalone: boolean;
  deleteData: boolean;
  tasksDeleted?: number;
  trashDeleted?: number;
  sessionsDeleted?: number;
  liveDeleted?: number;
  runsDeleted?: number;
  receiptId?: string;
  clearedAt?: string;
};

export type CarbonHomeCluster = {
  id: string;
  name: string;
  slug?: string;
  description?: string;
  slugAliases?: string[];
  dataPath?: string;
  createdAt?: string;
  projects: CarbonHomeProject[];
};

export type CarbonHome = {
  root: string;
  initialized: boolean;
  carbonVersion?: string;
  capabilities?: string[];
  // Top-level projects are intentionally first-class.  A cluster is an optional
  // extension for related projects, not a required wrapper around every project.
  manifest?: {
    version?: number;
    id?: string;
    createdAt?: string;
    projects?: CarbonHomeProject[];
    clusters?: CarbonHomeCluster[];
  };
};

export type CarbonCatalogIconKind = "builtin" | "emoji";

// Icons are finite presentation tokens, never paths, URLs, or user-provided SVG.
// The union stays deliberately narrow so a client cannot accidentally turn catalog
// metadata into an untrusted asset-loading surface.
export type CarbonCatalogIcon = {
  kind: CarbonCatalogIconKind;
  key: string;
};

export type CarbonCatalogPresentation = {
  version: number;
  clusters: Record<string, CarbonCatalogIcon>;
  projects: Record<string, CarbonCatalogIcon>;
};

export type CarbonCatalogPresentationTarget = "cluster" | "project";

// Short aliases keep presentation-only component props independent from the broader
// Carbon API naming without duplicating their wire contract.
export type CatalogPresentation = CarbonCatalogPresentation;
export type CatalogIconToken = CarbonCatalogIcon;

export type CarbonMigrationPreflight = {
  targetHome: string;
  legacyRoot: string;
  legacyPath: string;
  legacyDigest: string;
  name: string;
  projects?: CarbonMigrationProject[];
  homeExists?: boolean;
  existingHomeId?: string;
  existingHomeDigest?: string;
};

export type CarbonMigrationProject = {
  legacyId?: string;
  targetId?: string;
  name?: string;
  sourcePath?: string;
  offline?: boolean;
  fingerprint?: string;
  /** Legacy wire alias from pre-Carbon sidecars. Current UI never emits this field. */
  cairnPath?: string;
  snapshotDigest?: string;
};

export type CarbonMigrationPlan = {
  version?: number;
  id: string;
  clusterId: string;
  targetHome: string;
  legacyRoot: string;
  legacyPath: string;
  legacyDigest: string;
  reviewDigest: string;
  projects?: CarbonMigrationProject[];
  tasks?: unknown[];
  sessions?: unknown[];
  configs?: unknown[];
  runs?: unknown[];
  configConflicts?: Array<{ projectId?: string; field?: string; detail?: string }>;
  configPolicy?: string;
  warnings?: string[];
};

export type CarbonMigrationApplyResult = {
  result: {
    plan: CarbonMigrationPlan;
    applied: boolean;
    backupPath?: string;
    receiptPath?: string;
  };
  snapshot?: { id?: string; manifest_key?: string };
  snapshotId?: string;
  snapshotTiming?: "pre-import" | "post-import";
};

export type CarbonMigrationReceiptMeta = {
  id: string;
  status: string;
  appliedAt?: string;
  clusterId?: string;
  legacyRoot?: string;
};

export type CarbonMigrationReceipts = {
  receipts: CarbonMigrationReceiptMeta[];
  latest?: CarbonMigrationReceiptMeta | null;
};

export type CarbonDoctorIssue = {
  code: string;
  clusterId?: string;
  projectId?: string;
  detail: string;
};

export type CarbonDoctorRepair = CarbonDoctorIssue;

export type CarbonDoctorReport = {
  main: string;
  homeId: string;
  changed: boolean;
  applied: boolean;
  issues: CarbonDoctorIssue[];
  repairs: CarbonDoctorRepair[];
};

export class CarbonAPIError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly currentVersion?: string;
  readonly currentEtag?: string;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(
    message: string,
    status: number,
    details?: Readonly<Record<string, unknown>>,
  ) {
    super(message);
    this.name = "CarbonAPIError";
    this.status = status;
    this.details = details;
    this.code = typeof details?.code === "string" ? details.code : undefined;
    const conflict = details?.conflict && typeof details.conflict === "object"
      ? details.conflict as Record<string, unknown>
      : undefined;
    this.currentVersion = typeof details?.currentVersion === "string"
      ? details.currentVersion
      : typeof conflict?.currentVersion === "string" ? conflict.currentVersion : undefined;
    this.currentEtag = typeof details?.currentEtag === "string"
      ? details.currentEtag
      : typeof conflict?.currentEtag === "string" ? conflict.currentEtag : undefined;
  }
}

export type CarbonOptional<T> =
  | { available: true; data: T }
  | { available: false; data: undefined };

const enc = encodeURIComponent;

type CarbonRequestOptions = {
  headers?: Record<string, string | undefined>;
};

async function request<T>(method: string, url: string, body?: unknown, options?: CarbonRequestOptions): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  for (const [name, value] of Object.entries(options?.headers ?? {})) {
    if (value !== undefined) headers[name] = value;
  }
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const actor = currentActor();
  if (actor) headers["X-Carbon-Actor"] = enc(actor);

  const response = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    // An intermediary can return non-JSON. Keep the useful HTTP status below.
  }
  if (!response.ok) {
    const details = data && typeof data === "object" ? data as Record<string, unknown> : undefined;
    const message =
      details && typeof details.error === "string"
        ? details.error
        : `${response.status} ${response.statusText}`;
    throw new CarbonAPIError(message, response.status, details);
  }
  return data as T;
}

async function optional<T>(method: string, url: string, body?: unknown, options?: CarbonRequestOptions): Promise<CarbonOptional<T>> {
  try {
    return { available: true, data: await request<T>(method, url, body, options) };
  } catch (error) {
    if (error instanceof CarbonAPIError && error.status === 404) return { available: false, data: undefined };
    throw error;
  }
}

// Catalog image assets are intentionally not JSON.  Keep their narrow binary
// transport separate from `request` so a caller cannot accidentally submit a
// filename, path, URL, or data URI instead of the locally selected bytes.
async function binaryAssetRequest(method: "PUT" | "DELETE", url: string, file?: Blob): Promise<void> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (file) headers["Content-Type"] = file.type;
  const actor = currentActor();
  if (actor) headers["X-Carbon-Actor"] = enc(actor);

  const response = await fetch(url, { method, headers, body: file });
  if (response.ok) return;
  const text = await response.text();
  let details: Record<string, unknown> | undefined;
  try {
    const parsed: unknown = text ? JSON.parse(text) : undefined;
    if (parsed && typeof parsed === "object") details = parsed as Record<string, unknown>;
  } catch {
    // The status remains actionable even if a proxy replaced the JSON error body.
  }
  throw new CarbonAPIError(
    typeof details?.error === "string" ? details.error : `${response.status} ${response.statusText}`,
    response.status,
    details,
  );
}

function ifMatchHeader(expectedVersion?: string): CarbonRequestOptions | undefined {
  const value = expectedVersion?.trim();
  if (!value) return undefined;
  // The work-log service accepts either its raw version or a quoted ETag in the
  // request body. HTTP's If-Match syntax, however, must use the quoted form.
  const etag = value.startsWith('"') && value.endsWith('"') ? value : `"${value}"`;
  return { headers: { "If-Match": etag } };
}

export async function getCarbonCapabilities(scope: CarbonScopeInput): Promise<CarbonOptional<CarbonCapabilities>> {
  // Status is intentionally the discovery endpoint: legacy servers return their normal
  // status shape, which we treat as no Carbon capability rather than an error.
  const status = await request<{ carbonVersion?: string; capabilities?: unknown }>(
    "GET",
    scopedURL("/api/status", scope),
  );
  const features = Array.isArray(status.capabilities)
    ? status.capabilities.filter((value): value is string => typeof value === "string")
    : [];
  if (!status.carbonVersion) return { available: false, data: undefined };
  return { available: true, data: { version: status.carbonVersion, features } };
}

export const createCarbonTask = (scope: CarbonScopeInput, input: CarbonTaskFields & { title: string; body?: string; deps?: string[]; labels?: string[]; priority?: string; parent?: string }) =>
  optional<Task>("POST", scopedURL("/api/tasks", scope), input).then(normalizeOptionalTask);

export const listCarbonTasks = (scope: CarbonScopeInput, includeCluster = false, marketHistory = false) =>
  optional<{ tasks?: Task[] }>("GET", scopedURL("/api/tasks", scope, {
    include_cluster: includeCluster ? "true" : undefined,
    market_history: marketHistory ? "true" : undefined,
  })).then((result) =>
    result.available ? { available: true as const, data: { ...result.data, tasks: (result.data.tasks ?? []).map(normalizeTask) } } : result,
  );

export const getCarbonTask = (scope: CarbonScopeInput, id: string, includeCluster = false) =>
  optional<Task>("GET", scopedURL(`/api/tasks/${enc(id)}`, scope, { include_cluster: includeCluster ? "true" : undefined })).then(normalizeOptionalTask);

// Detail data deliberately uses the same scoped task routes as the legacy client.
// Carbon only changes how the storage boundary is carried (home/cluster/project),
// never the task DTOs returned by the server.
export const getCarbonTaskRuns = (scope: CarbonScopeInput, id: string) =>
  optional<{ runs?: Run[] }>("GET", scopedURL(`/api/tasks/${enc(id)}/runs`, scope)).then((result) =>
    result.available ? { available: true as const, data: result.data.runs ?? [] } : result,
  );

export const getCarbonTaskGitContext = (scope: CarbonScopeInput, id: string) =>
  optional<{ sessions?: SessionGitContext[] }>("GET", scopedURL(`/api/tasks/${enc(id)}/git_context`, scope)).then((result) =>
    result.available ? { available: true as const, data: result.data.sessions ?? [] } : result,
  );

export const listCarbonTaskSessions = (scope: CarbonScopeInput, id: string) =>
  optional<{ sessions?: AgentSession[] }>("GET", scopedURL(`/api/tasks/${enc(id)}/sessions`, scope)).then((result) =>
    result.available ? { available: true as const, data: result.data.sessions ?? [] } : result,
  );

export const listCarbonSessions = (scope: CarbonScopeInput) =>
  optional<{ sessions?: AgentSession[] }>("GET", scopedURL("/api/sessions", scope)).then((result) =>
    result.available ? { available: true as const, data: result.data.sessions ?? [] } : result,
  );

export const patchCarbonTask = (scope: CarbonScopeInput, id: string, fields: CarbonTaskPatch) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/update`, scope), fields).then(normalizeOptionalTask);

export const transitionCarbonTask = (scope: CarbonScopeInput, id: string, to: string) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/transition`, scope), { to }).then(normalizeOptionalTask);

export const runCarbonTaskChecks = (scope: CarbonScopeInput, id: string, only?: number[]) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/run_checks`, scope), { only }).then(normalizeOptionalTask);

export const attestCarbonTask = (scope: CarbonScopeInput, id: string, index: number, pass: boolean) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/attest`, scope), { index, pass }).then(normalizeOptionalTask);

export const addCarbonTaskNote = (scope: CarbonScopeInput, id: string, text: string) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/note`, scope), { text }).then(normalizeOptionalTask);

function carbonTaskNoteURL(scope: CarbonScopeInput, id: string, note?: string, index?: number): string {
  const segment = note ? enc(note) : "-";
  return scopedURL(
    `/api/tasks/${enc(id)}/notes/${segment}`,
    scope,
    note ? undefined : { index: String(index ?? -1) },
  );
}

export const editCarbonTaskNote = (scope: CarbonScopeInput, id: string, text: string, note?: string, index?: number) =>
  optional<Task>("PATCH", carbonTaskNoteURL(scope, id, note, index), { text }).then(normalizeOptionalTask);

export const deleteCarbonTaskNote = (scope: CarbonScopeInput, id: string, note?: string, index?: number) =>
  optional<Task>("DELETE", carbonTaskNoteURL(scope, id, note, index)).then(normalizeOptionalTask);

export const reorderCarbonTask = (scope: CarbonScopeInput, id: string, rank: number) =>
  optional<Task>("POST", scopedURL(`/api/tasks/${enc(id)}/reorder`, scope), { rank }).then(normalizeOptionalTask);

export const bulkPatchCarbonTasks = (scope: CarbonScopeInput, patch: CarbonBulkPatch) =>
  optional<{ tasks?: Task[]; etags?: Record<string, string> }>("POST", scopedURL("/api/tasks/bulk/update", scope), patch);

export const bulkMoveCarbonTasks = (scope: CarbonScopeInput, input: CarbonBulkMove) =>
  optional<{ tasks?: Task[]; etags?: Record<string, string> }>("POST", scopedURL("/api/tasks/bulk/move", scope), input);

export const claimLease = (scope: CarbonScopeInput, id: string, input?: { ttlSeconds?: number; reason?: string; expectedVersion?: string }) =>
  optional<CarbonLeaseClaimResponse>("POST", scopedURL(`/api/tasks/${enc(id)}/lease/claim`, scope), input ?? {}).then((result) => {
    if (!result.available || !result.data.task) return result;
    return { available: true as const, data: { ...result.data, task: normalizeTask(result.data.task) } };
  });

export const renewLease = (scope: CarbonScopeInput, id: string, input: { leaseId: string; ttlSeconds?: number; expectedVersion?: string }) =>
  optional<unknown>("POST", scopedURL(`/api/tasks/${enc(id)}/lease/renew`, scope), input);

export const releaseLease = (scope: CarbonScopeInput, id: string, input: { leaseId: string; reason: string; expectedVersion?: string; keepAssignee?: boolean }) =>
  optional<unknown>("POST", scopedURL(`/api/tasks/${enc(id)}/lease/release`, scope), input);

export const reassignCarbonTask = (scope: CarbonScopeInput, id: string, input: { assignee: string; force?: boolean; reason?: string; expectedVersion?: string }) =>
  optional<unknown>("POST", scopedURL(`/api/tasks/${enc(id)}/lease/reassign`, scope), input);

export const approveLeaseClaim = (scope: CarbonScopeInput, id: string, input: { requestId: string; approve: boolean; reason?: string; expectedVersion?: string }) =>
  optional<unknown>("POST", scopedURL(`/api/tasks/${enc(id)}/approval`, scope), input);

function normalizeWorkerMetric(worker: CarbonWorkerMetric): CarbonWorkerMetric {
  const recentWork = worker.recentWork ?? worker.recent_work;
  return {
    ...worker,
    completedByPriority: worker.completedByPriority ?? worker.completed_by_priority,
    averageCycleSeconds: worker.averageCycleSeconds ?? worker.average_cycle_seconds,
    reopenRate: worker.reopenRate ?? worker.reopen_rate,
    cycleSamples: worker.cycleSamples ?? worker.cycle_samples,
    lastActivity: worker.lastActivity ?? worker.last_activity,
    recentWork,
  };
}

function normalizeOptionalWorkers(result: CarbonOptional<CarbonWorkersResponse>): CarbonOptional<CarbonWorkersResponse> {
  return result.available
    ? { available: true, data: { ...result.data, workers: (result.data.workers ?? []).map(normalizeWorkerMetric) } }
    : result;
}

export const getWorkerMetrics = (args: { scope: CarbonScopeInput; viewScope: "all" | "cluster" | "project" }) => {
  const base = typeof args.scope === "string" ? legacyCarbonScope(args.scope) : args.scope;
  const scope: CarbonScope =
    args.viewScope === "all"
      ? { home: base.home }
      : args.viewScope === "cluster"
        ? { home: base.home, clusterId: base.clusterId }
        : base;
  return optional<CarbonWorkersResponse>("GET", scopedURL("/api/stats/workers", scope, {
    include_cluster: args.viewScope === "project" ? undefined : "true",
  })).then(normalizeOptionalWorkers);
};

function normalizeWorkerAliases(raw: unknown): Record<string, string> {
  if (raw && typeof raw === "object" && "actor" in raw && "alias" in raw) {
    const actor = typeof (raw as { actor?: unknown }).actor === "string" ? (raw as { actor: string }).actor.trim() : "";
    const alias = typeof (raw as { alias?: unknown }).alias === "string" ? (raw as { alias: string }).alias.trim() : "";
    return actor && alias ? { [actor]: alias } : {};
  }
  const source = raw && typeof raw === "object" && "aliases" in raw
    ? (raw as { aliases?: unknown }).aliases
    : raw;
  const aliases: Record<string, string> = {};
  if (Array.isArray(source)) {
    for (const item of source) {
      if (!item || typeof item !== "object") continue;
      const actor = typeof (item as { actor?: unknown }).actor === "string" ? (item as { actor: string }).actor.trim() : "";
      const alias = typeof (item as { alias?: unknown }).alias === "string" ? (item as { alias: string }).alias.trim() : "";
      if (actor && alias) aliases[actor] = alias;
    }
    return aliases;
  }
  if (!source || typeof source !== "object") return aliases;
  for (const [actor, alias] of Object.entries(source)) {
    const canonical = actor.trim();
    const display = typeof alias === "string" ? alias.trim() : "";
    if (canonical && display) aliases[canonical] = display;
  }
  return aliases;
}

function normalizeOptionalWorkerAliases(result: CarbonOptional<unknown>): CarbonOptional<CarbonWorkerAliasesResponse> {
  return result.available
    ? { available: true, data: { aliases: normalizeWorkerAliases(result.data) } }
    : result;
}

function homeOnlyURL(endpoint: string, home?: string): string {
  if (!home) return endpoint;
  return `${endpoint}${endpoint.includes("?") ? "&" : "?"}home=${enc(home)}`;
}

function homeWorkerAliasesURL(home?: string): string {
  return homeOnlyURL("/api/home/workers/aliases", home);
}

export const getCarbonWorkerAliases = (home?: string) =>
  optional<unknown>("GET", homeWorkerAliasesURL(home)).then(normalizeOptionalWorkerAliases);

// An empty alias is the explicit remove operation. `home` intentionally stays in
// the query string: the server contract accepts only actor/alias in the body.
export const patchCarbonWorkerAlias = (home: string | undefined, input: { actor: string; alias: string }) =>
  optional<unknown>("PATCH", homeWorkerAliasesURL(home), input).then(normalizeOptionalWorkerAliases);

// Reset and delete are presentation/metric lifecycle actions only. Their server
// endpoints intentionally do not rewrite task data; a later Worker activity can
// re-register the actor without restoring the cleared historical metrics.
export const resetCarbonWorker = (home: string | undefined, actor: string) =>
  optional<CarbonWorkerRegistryMutation>("POST", homeOnlyURL("/api/home/workers/reset", home), { actor });

export const deleteCarbonWorker = (home: string | undefined, actor: string) =>
  optional<CarbonWorkerRegistryMutation>("DELETE", homeOnlyURL(`/api/home/workers/${enc(actor)}`, home));

type CarbonWorkLogWire = CarbonWorkLog & {
  cluster_id?: string;
  project_id?: string;
  task_id?: string;
  created_at?: string;
  created_by?: string;
  updated_at?: string;
  updated_by?: string;
};

function normalizeWorkLog(log: CarbonWorkLogWire): CarbonWorkLog {
  return {
    ...log,
    clusterId: log.clusterId ?? log.cluster_id ?? "",
    projectId: log.projectId ?? log.project_id,
    taskId: log.taskId ?? log.task_id,
    createdAt: log.createdAt ?? log.created_at ?? "",
    createdBy: log.createdBy ?? log.created_by ?? "",
    updatedAt: log.updatedAt ?? log.updated_at ?? "",
    updatedBy: log.updatedBy ?? log.updated_by ?? "",
  };
}

function normalizeOptionalWorkLog(result: CarbonOptional<CarbonWorkLogWire>): CarbonOptional<CarbonWorkLog> {
  return result.available ? { available: true, data: normalizeWorkLog(result.data) } : result;
}

function normalizeOptionalWorkLogs(result: CarbonOptional<{ worklogs?: CarbonWorkLogWire[] }>): CarbonOptional<CarbonWorkLogListResponse> {
  return result.available
    ? { available: true, data: { worklogs: (result.data.worklogs ?? []).map(normalizeWorkLog) } }
    : result;
}

function workLogListURL(scope: CarbonScopeInput, filter: CarbonWorkLogListFilter): string {
  const limit = filter.limit ?? 100;
  return scopedURL("/api/worklogs", scope, {
    worker: filter.worker,
    visibility: filter.visibility,
    projectId: filter.projectId,
    taskId: filter.taskId,
    limit: String(limit),
  });
}

export const listCarbonWorkLogs = (scope: CarbonScopeInput, filter: CarbonWorkLogListFilter = {}) =>
  optional<{ worklogs?: CarbonWorkLogWire[] }>("GET", workLogListURL(scope, filter)).then(normalizeOptionalWorkLogs);

export const getCarbonWorkLog = (scope: CarbonScopeInput, id: string) =>
  optional<CarbonWorkLogWire>("GET", scopedURL(`/api/worklogs/${enc(id)}`, scope)).then(normalizeOptionalWorkLog);

export const createCarbonWorkLog = (scope: CarbonScopeInput, input: CarbonWorkLogCreate) =>
  optional<CarbonWorkLogWire>("POST", scopedURL("/api/worklogs", scope), input).then(normalizeOptionalWorkLog);

export const updateCarbonWorkLog = (scope: CarbonScopeInput, id: string, input: CarbonWorkLogUpdate) =>
  optional<CarbonWorkLogWire>("PUT", scopedURL(`/api/worklogs/${enc(id)}`, scope), input, ifMatchHeader(input.expectedVersion)).then(normalizeOptionalWorkLog);

export const deleteCarbonWorkLog = (scope: CarbonScopeInput, id: string, input: { expectedVersion: string }) =>
  optional<unknown>("DELETE", scopedURL(`/api/worklogs/${enc(id)}`, scope), undefined, ifMatchHeader(input.expectedVersion));

export const searchCarbonTasks = (args: { scope: CarbonScopeInput; query: string; includeCluster?: boolean }) => {
  return optional<{ results?: CarbonSearchResult[] }>("GET", scopedURL("/api/search", args.scope, { q: args.query, include_cluster: args.includeCluster ? "true" : undefined }));
};

export const listTrash = (scope: CarbonScopeInput) => {
  return optional<{ entries?: CarbonTrashItem[] }>("GET", scopedURL("/api/trash", scope));
};

export const trashCarbonTasks = (scope: CarbonScopeInput, input: { ids: string[]; reason?: string; expectedVersions?: Record<string, string> }) =>
  optional<{ entries?: CarbonTrashItem[] }>("POST", scopedURL("/api/trash", scope), input);

export const restoreTrashItem = (scope: CarbonScopeInput, id: string, input: { projectId?: string; expectedVersion?: string } = {}) =>
  optional<Task>("POST", scopedURL(`/api/trash/${enc(id)}/restore`, scope), input).then(normalizeOptionalTask);

export const emptyTrash = (scope: CarbonScopeInput) => optional<unknown>("DELETE", scopedURL("/api/trash", scope, { confirm: "true" }));

export const listCarbonViews = (scope: CarbonScopeInput) =>
  optional<{ views?: CarbonSavedView[] }>("GET", scopedURL("/api/views", scope));

export const createCarbonView = (scope: CarbonScopeInput, input: { name: string; query: CarbonViewQuery; id?: string }) =>
  optional<CarbonSavedView>("POST", scopedURL("/api/views", scope), input);

export const deleteCarbonView = (scope: CarbonScopeInput, id: string) =>
  optional<unknown>("DELETE", scopedURL(`/api/views/${enc(id)}`, scope));

export const listCarbonTemplates = (scope: CarbonScopeInput) =>
  optional<{ templates?: CarbonTaskTemplate[] }>("GET", scopedURL("/api/templates", scope));

export const createCarbonTemplate = (scope: CarbonScopeInput, input: Omit<CarbonTaskTemplate, "id" | "version"> & { id?: string }) =>
  optional<CarbonTaskTemplate>("POST", scopedURL("/api/templates", scope), input);

export const instantiateCarbonTemplate = (
  scope: CarbonScopeInput,
  id: string,
  input: { expectedVersion?: string; projectId?: string; title?: string; body?: string; type?: string; importance?: string; priority?: string; labels?: string[]; deps?: string[]; parent?: string; checks?: Check[] } = {},
) => optional<Task>("POST", scopedURL(`/api/templates/${enc(id)}/instantiate`, scope), input).then(normalizeOptionalTask);

export const listCarbonTaskTypes = (scope: CarbonScopeInput) =>
  optional<{ types?: string[]; custom?: CarbonTaskTypeDefinition[] }>("GET", scopedURL("/api/types", scope));

export const createCarbonTaskType = (scope: CarbonScopeInput, input: { key: string; displayName?: string }) =>
  optional<{ definition?: CarbonTaskTypeDefinition }>("POST", scopedURL("/api/types", scope), input);

function normalizeOptionalTask(result: CarbonOptional<Task>): CarbonOptional<Task> {
  return result.available ? { available: true, data: normalizeTask(result.data) } : result;
}

export const getBackupConfig = (scope: CarbonHomeScope) =>
  optional<CarbonBackupConfig>("GET", backupURL("/api/backup/config", scope));

export const putBackupConfig = (scope: CarbonHomeScope, config: CarbonBackupConfig) =>
  optional<CarbonBackupConfig>("PUT", backupURL("/api/backup/config", scope), config);

export const createBackupSnapshot = (scope: CarbonHomeScope) =>
  optional<CarbonBackupSnapshot>("POST", backupURL("/api/backup/snapshots", scope), {});

export const runBackupNow = (scope: CarbonHomeScope) =>
  optional<CarbonBackupRun>("POST", backupURL("/api/backup/run-now", scope), {});

export const pruneBackupSnapshots = (scope: CarbonHomeScope) =>
  optional<CarbonBackupPrune>("POST", backupURL("/api/backup/prune", scope), {});

export const setBackupContinuousAuthorization = (scope: CarbonHomeScope, enabled: boolean) =>
  optional<CarbonBackupConfig>("POST", backupURL("/api/backup/continuous-authorization", scope), { confirm: true, enabled });

export const listBackupSnapshots = (scope: CarbonHomeScope) =>
  optional<{ snapshots?: CarbonBackupSnapshotInfo[] }>("GET", backupURL("/api/backup/snapshots", scope));

export const uploadBackupSnapshot = (scope: CarbonHomeScope, snapshotId: string) =>
  optional<CarbonBackupUpload>("POST", backupURL(`/api/backup/snapshots/${enc(snapshotId)}/upload`, scope), { confirm: true });

export const verifyBackup = (scope: CarbonHomeScope, snapshotId: string) =>
  optional<CarbonBackupVerification>("POST", backupURL(`/api/backup/snapshots/${enc(snapshotId)}/verify`, scope), {});

export const createRestorePlan = (scope: CarbonHomeScope, snapshotId: string) =>
  optional<CarbonRestorePlan>("POST", backupURL(`/api/backup/snapshots/${enc(snapshotId)}/restore-plan`, scope), {});

export const getBackupStatus = (scope: CarbonHomeScope) =>
  optional<CarbonBackupStatus>("GET", backupURL("/api/backup/status", scope));

export const getCarbonConfig = (scope: CarbonScopeInput) =>
  optional<CarbonConfig>("GET", scopedURL("/api/config", scope));

export const saveCarbonConfig = (scope: CarbonScopeInput, input: CarbonConfigUpdate) =>
  optional<CarbonConfig>("POST", scopedURL("/api/config", scope), input);

export const listCarbonWorkerIdentities = (scope: CarbonScopeInput) =>
  optional<CarbonWorkerIdentityListResponse>("GET", scopedURL("/api/worker-identities", scope));

export const updateCarbonWorkerIdentity = (
  scope: CarbonScopeInput,
  actor: string,
  input: CarbonWorkerIdentityUpdate,
) => optional<CarbonWorkerIdentityMutationResponse>("PUT", scopedURL(`/api/worker-identities/${enc(actor)}`, scope), input);

// --- Carbon-scoped MCP connection -------------------------------------------------

// `configProjectId` selects only the source directory that receives an agent config.
// It never changes the Carbon scope embedded in the generated MCP command.
export type CarbonMCPRouting = "pinned" | "session";

function connectRouting(routing: CarbonMCPRouting | undefined): string | undefined {
  return routing === "session" ? "session" : undefined;
}

export const listCarbonIntegrations = (scope: CarbonScopeInput, configProjectId?: string, routing?: CarbonMCPRouting) =>
  optional<CarbonIntegrations>("GET", scopedURL("/api/connect", scope, { configProjectId, routing: connectRouting(routing) }));

export const connectCarbonAgent = (scope: CarbonScopeInput, input: { agent: string; actor?: string; configProjectId?: string; routing?: CarbonMCPRouting }) =>
  optional<CarbonConnectResult>("POST", scopedURL(`/api/connect/${enc(input.agent)}`, scope, { routing: connectRouting(input.routing) }), {
    actor: input.actor,
    configProjectId: input.configProjectId,
  });

export const disconnectCarbonAgent = (scope: CarbonScopeInput, input: { agent: string; configProjectId?: string; routing?: CarbonMCPRouting }) =>
  optional<CarbonConnectResult>("DELETE", scopedURL(`/api/connect/${enc(input.agent)}`, scope, {
    configProjectId: input.configProjectId,
    routing: connectRouting(input.routing),
  }));

export const getCarbonAgentGuide = (scope: CarbonScopeInput, input: { agent: string; actor?: string; configProjectId?: string; routing?: CarbonMCPRouting }) =>
  optional<CarbonAgentGuide>("GET", scopedURL(`/api/connect/${enc(input.agent)}/manual`, scope, {
    actor: input.actor,
    configProjectId: input.configProjectId,
    routing: connectRouting(input.routing),
  }));

// --- Home / cluster manifest -------------------------------------------------------

export const getCarbonHome = (home?: string) =>
  optional<CarbonHome>("GET", home ? `/api/home?home=${enc(home)}` : "/api/home");

export const getCarbonCatalogPresentation = (home?: string) =>
  optional<CarbonCatalogPresentation>("GET", homeOnlyURL("/api/home/presentation", home));

export function carbonCatalogAssetURL(
  home: string | undefined,
  target: CarbonCatalogPresentationTarget,
  id: string | undefined,
  revision?: string | number,
): string | undefined {
  if (!id?.trim()) return undefined;
  const params = new URLSearchParams();
  if (home) params.set("home", home);
  if (revision !== undefined) params.set("v", String(revision));
  const query = params.toString();
  const endpoint = `/api/home/presentation/${enc(target)}/${enc(id)}/asset`;
  return query ? `${endpoint}?${query}` : endpoint;
}

export async function uploadCarbonCatalogAsset(
  home: string | undefined,
  input: { target: CarbonCatalogPresentationTarget; id: string; file: Blob },
): Promise<CarbonOptional<undefined>> {
  try {
    await binaryAssetRequest(
      "PUT",
      homeOnlyURL(`/api/home/presentation/${enc(input.target)}/${enc(input.id)}/asset`, home),
      input.file,
    );
    return { available: true, data: undefined };
  } catch (error) {
    if (error instanceof CarbonAPIError && error.status === 404) return { available: false, data: undefined };
    throw error;
  }
}

export async function deleteCarbonCatalogAsset(
  home: string | undefined,
  input: { target: CarbonCatalogPresentationTarget; id: string },
): Promise<CarbonOptional<undefined>> {
  try {
    await binaryAssetRequest(
      "DELETE",
      homeOnlyURL(`/api/home/presentation/${enc(input.target)}/${enc(input.id)}/asset`, home),
    );
    return { available: true, data: undefined };
  } catch (error) {
    if (error instanceof CarbonAPIError && error.status === 404) return { available: false, data: undefined };
    throw error;
  }
}

// The home is carried only by the normal home-only request scope. The presentation
// handler deliberately rejects it in the body so an icon mutation cannot silently
// target another catalog.
export const setCarbonCatalogIcon = (
  home: string | undefined,
  input: { target: CarbonCatalogPresentationTarget; id: string; icon: CarbonCatalogIcon | null },
) =>
  optional<CarbonCatalogPresentation>(
    "PUT",
    homeOnlyURL(`/api/home/presentation/${enc(input.target)}/${enc(input.id)}/icon`, home),
    { icon: input.icon },
  );

export const ensureCarbonHome = (home?: string) =>
  optional<CarbonHome>("POST", "/api/home", home ? { home } : {});

export const createHomeCluster = (input: { home?: string; name: string; slug?: string; description?: string }) =>
  optional<CarbonHomeCluster>("POST", "/api/home/clusters", input);

export const updateHomeCluster = (input: { home?: string; clusterId: string; name?: string; slug?: string; description?: string }) =>
  optional<CarbonHomeCluster>("PATCH", `/api/home/clusters/${enc(input.clusterId)}`, {
    home: input.home,
    name: input.name,
    slug: input.slug,
    description: input.description,
  });

export type CarbonHomeProjectInput = {
  home?: string;
  // Omit `clusterId` to create/update an independent project.  Clustered routes
  // remain for legacy links and the explicit extension workflow.
  clusterId?: string;
  name: string;
  slug?: string;
  description?: string;
  kind?: string;
  sourcePath: string;
};

export const addHomeProject = (input: CarbonHomeProjectInput) =>
  optional<CarbonHomeProject>(
    "POST",
    input.clusterId
      ? `/api/home/clusters/${enc(input.clusterId)}/projects`
      : "/api/home/projects",
    {
      home: input.home,
      name: input.name,
      slug: input.slug,
      description: input.description,
      kind: input.kind,
      sourcePath: input.sourcePath,
    },
  );

export const addStandaloneHomeProject = (input: Omit<CarbonHomeProjectInput, "clusterId">) =>
  addHomeProject(input);

export const relinkHomeProject = (input: { home?: string; clusterId?: string; projectId: string; sourcePath: string }) =>
  optional<CarbonHomeProject>(
    "POST",
    input.clusterId
      ? `/api/home/clusters/${enc(input.clusterId)}/projects/${enc(input.projectId)}/relink`
      : `/api/home/projects/${enc(input.projectId)}/relink`,
    { home: input.home, sourcePath: input.sourcePath },
  );

export const relinkStandaloneHomeProject = (input: { home?: string; projectId: string; sourcePath: string }) =>
  relinkHomeProject(input);

export const updateHomeProject = (input: { home?: string; clusterId?: string; projectId: string; name?: string; slug?: string; description?: string; kind?: string }) =>
  optional<CarbonHomeProject>(
    "PATCH",
    input.clusterId
      ? `/api/home/clusters/${enc(input.clusterId)}/projects/${enc(input.projectId)}`
      : `/api/home/projects/${enc(input.projectId)}`,
    {
      home: input.home,
      name: input.name,
      slug: input.slug,
      description: input.description,
      kind: input.kind,
    },
  );

export const updateStandaloneHomeProject = (input: { home?: string; projectId: string; name?: string; slug?: string; description?: string; kind?: string }) =>
  updateHomeProject(input);

// Task-data removal is deliberately a Home-only route. A project can be either
// standalone or grouped, but its stable id is unique within the Home manifest;
// callers must never fall back to a legacy path or a cluster-scoped endpoint.
export const clearHomeProjectTaskData = (input: { home?: string; projectId: string; name: string }) =>
  optional<void>(
    "POST",
    homeOnlyURL(`/api/home/projects/${enc(input.projectId)}/clear-task-data`, input.home),
    { name: input.name },
  );

// Project deletion is Home-only and always uses the immutable catalog ID. `deleteData`
// is explicit (including false) so a missing checkbox value cannot be interpreted as a
// destructive task-data clear by a newer sidecar.
export const deleteHomeProject = (input: { home?: string; projectId: string; name: string; deleteData: boolean }) =>
  optional<CarbonHomeProjectDelete>(
    "POST",
    homeOnlyURL(`/api/home/projects/${enc(input.projectId)}/delete`, input.home),
    { name: input.name, deleteData: input.deleteData },
  );

export const getLegacyMigrationPreflight = (home: string | undefined, legacyCluster: string) => {
  const query = new URLSearchParams({ legacyCluster });
  if (home) query.set("home", home);
  return optional<CarbonMigrationPreflight>("GET", `/api/home/migrations/legacy/preflight?${query.toString()}`);
};

export const previewLegacyMigration = (home: string | undefined, legacyCluster: string) =>
  optional<CarbonMigrationPlan>("POST", "/api/home/migrations/legacy/preview", { ...(home ? { home } : {}), legacyCluster });

export const applyLegacyMigration = (input: { home?: string; legacyCluster: string; expectedDigest: string; configPolicy?: string }) =>
  optional<CarbonMigrationApplyResult>("POST", "/api/home/migrations/legacy/apply", input);

export const getLegacyMigrationReceipts = (home?: string) =>
  optional<CarbonMigrationReceipts>("GET", home ? `/api/home/migrations/legacy/receipt?home=${enc(home)}` : "/api/home/migrations/legacy/receipt");

export const getHomeDoctor = (home: string | undefined, repair = false) => {
  const query = new URLSearchParams();
  if (home) query.set("home", home);
  if (repair) query.set("repair", "true");
  const suffix = query.toString();
  return optional<CarbonDoctorReport>("GET", `/api/home/doctor${suffix ? `?${suffix}` : ""}`);
};
