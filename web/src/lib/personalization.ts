// Carbon interface preferences intentionally use the stable Home id only.  A
// portable folder can move without changing a user's project landing place.

export type ProjectManagementPresentation = "dialog" | "page";
export type BoardPresentation = "row" | "card";

export type LastProjectSelection = {
  /** Absent for a standalone project; older records retain their cluster id. */
  clusterId?: string;
  projectId: string;
};

const PROJECT_MANAGEMENT_PRESENTATION_KEY = "carbon:project-management-presentation";
const BOARD_PRESENTATION_KEY = "carbon:board-presentation";
const BOARD_STATUS_SECTIONS_PREFIX = "carbon:board-status-sections:v1:";
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
  return read(BOARD_PRESENTATION_KEY) === "card" ? "card" : "row";
}

export function setBoardPresentation(value: BoardPresentation): void {
  write(BOARD_PRESENTATION_KEY, value);
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
