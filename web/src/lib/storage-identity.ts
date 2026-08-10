/**
 * Stable local-storage identity for a Carbon project.
 *
 * The legacy key used the data-home path. Keep that value only in this module's
 * in-memory migration map so callers can move existing preferences without
 * writing the path back to persistent browser storage.
 */
export type CarbonStorageIdentity = {
  home: string;
  homeId?: string;
  /** Undefined for a first-class standalone project. */
  clusterId?: string;
  projectId: string;
};

const legacyPaths = new Map<string, string>();
const MISSING_HOME_ID = "unidentified";

const part = (value: string) => encodeURIComponent(value);

/**
 * Returns the persistent key for local Carbon fallbacks. A current Carbon
 * manifest always supplies `homeId`; the defensive sentinel deliberately does
 * not fall back to the filesystem path when talking to an older sidecar.
 */
export function carbonStorageKey({ home, homeId, clusterId, projectId }: CarbonStorageIdentity): string {
  const stableHomeId = homeId?.trim() || MISSING_HOME_ID;
  const cluster = clusterId?.trim() ?? "";
  const storageKey = `carbon:v2:${part(stableHomeId)}|${part(cluster)}|${part(projectId)}`;
  if (home) legacyPaths.set(storageKey, `carbon:${home}|${cluster}|${projectId}`);
  return storageKey;
}

/** Returns the old in-memory key only for a scope registered in this page load. */
export function legacyCarbonStorageKey(storageKey: string): string | null {
  return legacyPaths.get(storageKey) ?? null;
}
