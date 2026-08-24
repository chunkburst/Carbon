// Carbon interface preferences intentionally use the stable Home id only.  A
// portable folder can move without changing a user's project landing place.

export type ProjectManagementPresentation = "dialog" | "page";
export type TaskListPresentation = "row" | "card";
export type WorkspaceTaskSurface = "tasks" | "agent-work" | "board";
/** @deprecated Kept only to read preferences written by Carbon 1.0.0 previews. */
export type BoardPresentation = "row" | "card" | "animation";
export type AnimationBoardStyle = "pixel-agents" | "market-kline";
export type MarketTimeframe = "1m" | "5m" | "30m" | "1h" | "1d";

export type AnimationStyleMetadata = {
  /** Playback rate as a percentage. 100 is the authored cadence. */
  speed: number;
  /** Style-owned energy range. K-line maps this 0–1000 value to live volatility. */
  volatility: number;
};

export const ANIMATION_SPEED_MIN = 25;
export const ANIMATION_SPEED_MAX = 300;
export const ANIMATION_VOLATILITY_MIN = 0;
export const ANIMATION_VOLATILITY_MAX = 1_000;
export const DEFAULT_ANIMATION_STYLE_METADATA: Readonly<AnimationStyleMetadata> = {
  speed: 100,
  volatility: 200,
};

export type LastProjectSelection = {
  /** Absent for a standalone project; older records retain their cluster id. */
  clusterId?: string;
  projectId: string;
};

const PROJECT_MANAGEMENT_PRESENTATION_KEY = "carbon:project-management-presentation";
const BOARD_PRESENTATION_KEY = "carbon:board-presentation";
const TASK_LIST_PRESENTATION_KEY = "carbon:task-list-presentation:v1";
const WORKSPACE_TASK_SURFACE_KEY = "carbon:workspace-task-surface:v1";
const ANIMATION_BOARD_STYLE_KEY = "carbon:animation-board-style";
const BOARD_STATUS_SECTIONS_PREFIX = "carbon:board-status-sections:v1:";
const MARKET_TIMEFRAME_PREFIX = "carbon:market-timeframe:v1:";
const ANIMATION_STYLE_METADATA_PREFIX = "carbon:animation-style-metadata:v1:";
const LAST_PROJECT_PREFIX = "carbon:last-project:v1:";
export const PERSONALIZATION_EVENT = "carbon-personalization-change";

function read(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
    window.dispatchEvent(new Event(PERSONALIZATION_EVENT));
  } catch {
    // Preferences are a convenience. A private browser window may reject writes.
  }
}

function lastProjectKey(homeId?: string): string | null {
  const stableHomeId = homeId?.trim();
  return stableHomeId ? `${LAST_PROJECT_PREFIX}${encodeURIComponent(stableHomeId)}` : null;
}

export function getProjectManagementPresentation(): ProjectManagementPresentation {
  return read(PROJECT_MANAGEMENT_PRESENTATION_KEY) === "page" ? "page" : "dialog";
}

export function setProjectManagementPresentation(value: ProjectManagementPresentation): void {
  write(PROJECT_MANAGEMENT_PRESENTATION_KEY, value);
}

/**
 * The board renderer is a machine preference, while task data and views remain
 * project-scoped. Keep this small enum deliberately global so changing it in one
 * project updates every open Carbon board immediately.
 */
export function getBoardPresentation(): BoardPresentation {
  const value = read(BOARD_PRESENTATION_KEY);
  return value === "card" || value === "animation" ? value : "row";
}

export function setBoardPresentation(value: BoardPresentation): void {
  write(BOARD_PRESENTATION_KEY, value);
}

/**
 * Rows/cards belong to task-oriented surfaces. The dedicated visual board owns
 * its animation style separately, so choosing a K-line can never replace the
 * Tasks or Agent work lists again.
 */
export function getTaskListPresentation(): TaskListPresentation {
  const value = read(TASK_LIST_PRESENTATION_KEY);
  if (value === "row" || value === "card") return value;

  // One-way compatibility with the former shared board preference. An old
  // animation choice intentionally falls back to rows instead of leaking into
  // the functional task views.
  const legacy = read(BOARD_PRESENTATION_KEY);
  return legacy === "card" ? "card" : "row";
}

export function setTaskListPresentation(value: TaskListPresentation): void {
  write(TASK_LIST_PRESENTATION_KEY, value);
}

export function getWorkspaceTaskSurface(): WorkspaceTaskSurface {
  const value = read(WORKSPACE_TASK_SURFACE_KEY);
  return value === "agent-work" || value === "board" ? value : "tasks";
}

export function setWorkspaceTaskSurface(value: WorkspaceTaskSurface): void {
  write(WORKSPACE_TASK_SURFACE_KEY, value);
}

/**
 * Animation styles are presentation-only and deliberately independent of the
 * board mode. Switching away from animation preserves the last scene a person
 * chose, so returning to it does not reset their workspace.
 */
export function isAnimationBoardStyle(value: unknown): value is AnimationBoardStyle {
  return value === "pixel-agents" || value === "market-kline";
}

export function getAnimationBoardStyle(): AnimationBoardStyle {
  const value = read(ANIMATION_BOARD_STYLE_KEY);
  return isAnimationBoardStyle(value) ? value : "pixel-agents";
}

export function setAnimationBoardStyle(value: AnimationBoardStyle): void {
  write(ANIMATION_BOARD_STYLE_KEY, value);
}

export function isMarketTimeframe(value: unknown): value is MarketTimeframe {
  return value === "1m" || value === "5m" || value === "30m" || value === "1h" || value === "1d";
}

function marketTimeframeKey(projectKey: string): string | null {
  const stableKey = projectKey.trim();
  return stableKey ? `${MARKET_TIMEFRAME_PREFIX}${encodeURIComponent(stableKey)}` : null;
}

/**
 * A K-line period belongs to one project market. Switching projects restores that
 * project's last candle scale without changing the global animation-board style.
 */
export function getMarketTimeframe(projectKey: string): MarketTimeframe {
  const key = marketTimeframeKey(projectKey);
  if (!key) return "1h";
  const value = read(key);
  return isMarketTimeframe(value) ? value : "1h";
}

export function setMarketTimeframe(projectKey: string, value: MarketTimeframe): void {
  const key = marketTimeframeKey(projectKey);
  if (!key) return;
  write(key, value);
}

function animationStyleMetadataKey(projectKey: string, style: AnimationBoardStyle): string | null {
  const stableKey = projectKey.trim();
  return stableKey
    ? `${ANIMATION_STYLE_METADATA_PREFIX}${encodeURIComponent(stableKey)}:${style}`
    : null;
}

function boundedMetadataNumber(value: unknown, fallback: number, minimum: number, maximum: number): number {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.round(Math.min(maximum, Math.max(minimum, value)))
    : fallback;
}

/**
 * Animation tuning is machine-local, project-scoped, and style-scoped. New
 * styles can reuse this envelope without changing task data or older records.
 */
export function getAnimationStyleMetadata(projectKey: string, style: AnimationBoardStyle): AnimationStyleMetadata {
  const key = animationStyleMetadataKey(projectKey, style);
  if (!key) return { ...DEFAULT_ANIMATION_STYLE_METADATA };

  try {
    const value: unknown = JSON.parse(read(key) ?? "null");
    if (!value || typeof value !== "object" || Array.isArray(value)) return { ...DEFAULT_ANIMATION_STYLE_METADATA };
    const candidate = value as Partial<Record<keyof AnimationStyleMetadata, unknown>>;
    return {
      speed: boundedMetadataNumber(
        candidate.speed,
        DEFAULT_ANIMATION_STYLE_METADATA.speed,
        ANIMATION_SPEED_MIN,
        ANIMATION_SPEED_MAX,
      ),
      volatility: boundedMetadataNumber(
        candidate.volatility,
        DEFAULT_ANIMATION_STYLE_METADATA.volatility,
        ANIMATION_VOLATILITY_MIN,
        ANIMATION_VOLATILITY_MAX,
      ),
    };
  } catch {
    return { ...DEFAULT_ANIMATION_STYLE_METADATA };
  }
}

export function setAnimationStyleMetadata(
  projectKey: string,
  style: AnimationBoardStyle,
  metadata: AnimationStyleMetadata,
): void {
  const key = animationStyleMetadataKey(projectKey, style);
  if (!key) return;
  write(key, JSON.stringify({
    speed: boundedMetadataNumber(
      metadata.speed,
      DEFAULT_ANIMATION_STYLE_METADATA.speed,
      ANIMATION_SPEED_MIN,
      ANIMATION_SPEED_MAX,
    ),
    volatility: boundedMetadataNumber(
      metadata.volatility,
      DEFAULT_ANIMATION_STYLE_METADATA.volatility,
      ANIMATION_VOLATILITY_MIN,
      ANIMATION_VOLATILITY_MAX,
    ),
  }));
}

function boardStatusSectionsKey(storageKey: string): string | null {
  const stableKey = storageKey.trim();
  return stableKey ? `${BOARD_STATUS_SECTIONS_PREFIX}${encodeURIComponent(stableKey)}` : null;
}

function readBoardStatusSections(storageKey: string): Record<string, boolean> {
  const key = boardStatusSectionsKey(storageKey);
  if (!key) return {};

  try {
    const value: unknown = JSON.parse(read(key) ?? "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    return Object.fromEntries(
      Object.entries(value).filter((entry): entry is [string, boolean] => typeof entry[1] === "boolean"),
    );
  } catch {
    // A malformed historical preference must never prevent a board from opening.
    return {};
  }
}

/**
 * Section state is keyed by Carbon's stable Home/project storage identity rather
 * than an absolute filesystem path. Unknown statuses use the workflow default.
 */
export function getBoardStatusSectionOpen(storageKey: string, status: string, fallback: boolean): boolean {
  const value = readBoardStatusSections(storageKey)[status];
  return typeof value === "boolean" ? value : fallback;
}

export function setBoardStatusSectionOpen(storageKey: string, status: string, open: boolean): void {
  const key = boardStatusSectionsKey(storageKey);
  if (!key || !status.trim()) return;
  write(key, JSON.stringify({ ...readBoardStatusSections(storageKey), [status]: open }));
}

export function getLastProjectSelection(homeId?: string): LastProjectSelection | null {
  const key = lastProjectKey(homeId);
  if (!key) return null;

  try {
    const value: unknown = JSON.parse(read(key) ?? "null");
    if (
      value
      && typeof value === "object"
      && "projectId" in value
      && typeof value.projectId === "string"
      && value.projectId.trim()
    ) {
      const clusterId = "clusterId" in value && typeof value.clusterId === "string" && value.clusterId.trim()
        ? value.clusterId
        : undefined;
      return { ...(clusterId ? { clusterId } : {}), projectId: value.projectId };
    }
  } catch {
    // Ignore malformed historical values and use the first valid project instead.
  }

  return null;
}

export function setLastProjectSelection(homeId: string | undefined, selection: LastProjectSelection): void {
  const key = lastProjectKey(homeId);
  if (!key) return;
  write(key, JSON.stringify({ ...(selection.clusterId?.trim() ? { clusterId: selection.clusterId } : {}), projectId: selection.projectId }));
}
