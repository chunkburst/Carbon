import { useEffect, useState } from "react";

const CURRENT_CLUSTER_KEY = "carbon-current-cluster";
const CLUSTER_CHANGE_EVENT = "carbon-cluster-change";
const LEGACY_CURRENT_CLUSTER_KEY = "cairn-current-cluster";
let volatileCurrentCluster: string | null = null;

function readCurrentCluster(): string | null {
  try {
    const current = localStorage.getItem(CURRENT_CLUSTER_KEY);
    const root = current?.trim();
    if (root || current !== null) return root || volatileCurrentCluster;

    const legacy = localStorage.getItem(LEGACY_CURRENT_CLUSTER_KEY)?.trim();
    if (legacy) {
      try {
        localStorage.setItem(CURRENT_CLUSTER_KEY, legacy);
        localStorage.removeItem(LEGACY_CURRENT_CLUSTER_KEY);
      } catch {
        // The current window can still use the legacy selection.
      }
      return legacy;
    }
    return root || volatileCurrentCluster;
  } catch {
    return volatileCurrentCluster;
  }
}

function emitClusterChange(): void {
  window.dispatchEvent(new Event(CLUSTER_CHANGE_EVENT));
}

/** The locally selected cluster root. Project paths remain in the workspace registry. */
export function currentClusterRoot(): string | null {
  return readCurrentCluster();
}

/** Persist the current cluster and notify this window as well as other app windows. */
export function setCurrentClusterRoot(root: string | null): void {
  const next = root?.trim() || null;
  volatileCurrentCluster = next;
  try {
    if (next) localStorage.setItem(CURRENT_CLUSTER_KEY, next);
    else localStorage.removeItem(CURRENT_CLUSTER_KEY);
  } catch {
    // The in-memory event still lets the current UI recover in restricted webviews.
  }
  emitClusterChange();
}

export function clearCurrentCluster(): void {
  setCurrentClusterRoot(null);
}

/** Subscribe to current-cluster changes from this window or another Carbon window. */
export function subscribeToClusterChanges(listener: () => void): () => void {
  const onStorage = (event: StorageEvent) => {
    if (event.key === CURRENT_CLUSTER_KEY) listener();
  };
  window.addEventListener(CLUSTER_CHANGE_EVENT, listener);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(CLUSTER_CHANGE_EVENT, listener);
    window.removeEventListener("storage", onStorage);
  };
}

/** React bridge for the storage/custom-event-backed current cluster selection. */
export function useCurrentClusterRoot(): string | null {
  const [root, setRoot] = useState<string | null>(readCurrentCluster);
  useEffect(() => {
    setRoot(readCurrentCluster());
    return subscribeToClusterChanges(() => setRoot(readCurrentCluster()));
  }, []);
  return root;
}
