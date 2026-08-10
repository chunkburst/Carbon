// Workspaces map a clean URL slug (the folder name) to its absolute path, kept in
// localStorage. This lets the URL stay readable at #/carbon/task/ACME-004 while the full
// path (which is machine-specific and ugly) is resolved locally.

import { useEffect, useState } from "react";

const REGISTRY_KEY = "carbon-workspaces";
const LAST_KEY = "carbon-current-folder";
const REGISTRY_CHANGE_EVENT = "carbon-workspace-registry-change";
const LEGACY_REGISTRY_KEY = "cairn-workspaces";
const LEGACY_LAST_KEY = "cairn-current-folder";

type Registry = Record<string, string>; // slug -> absolute path
export type Workspace = { slug: string; path: string };
type RegisterOptions = { makeCurrent?: boolean };

function parseRegistry(raw: string | null): Registry {
  try {
    const parsed: unknown = JSON.parse(raw || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return Object.fromEntries(
      Object.entries(parsed).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
    );
  } catch {
    return {};
  }
}

function load(): Registry {
  try {
    const current = localStorage.getItem(REGISTRY_KEY);
    if (current !== null) return parseRegistry(current);

    const legacy = localStorage.getItem(LEGACY_REGISTRY_KEY);
    if (legacy === null) return {};
    const migrated = parseRegistry(legacy);
    try {
      localStorage.setItem(REGISTRY_KEY, JSON.stringify(migrated));
      localStorage.removeItem(LEGACY_REGISTRY_KEY);
    } catch {
      // Return the valid legacy payload for this session; never write it again.
    }
    return migrated;
  } catch {
    return {};
  }
}

function readLast(): string | null {
  try {
    const current = localStorage.getItem(LAST_KEY);
    if (current !== null) return current.trim() || null;

    const legacy = localStorage.getItem(LEGACY_LAST_KEY)?.trim() || null;
    if (!legacy) return null;
    try {
      localStorage.setItem(LAST_KEY, legacy);
      localStorage.removeItem(LEGACY_LAST_KEY);
    } catch {
      // The current session can still use the legacy value if storage is unavailable.
    }
    return legacy;
  } catch {
    return null;
  }
}

function emitRegistryChange(): void {
  window.dispatchEvent(new Event(REGISTRY_CHANGE_EVENT));
}

function save(reg: Registry): void {
  localStorage.setItem(REGISTRY_KEY, JSON.stringify(reg));
  emitRegistryChange();
}

function setLast(path: string): void {
  if (localStorage.getItem(LAST_KEY) === path) return;
  localStorage.setItem(LAST_KEY, path);
  emitRegistryChange();
}

/** Returns the final path segment on POSIX and Windows paths. */
export function workspaceBasename(path: string): string {
  return path.split(/[\\/]+/).filter(Boolean).pop() || path;
}

/** slugify turns a path into a clean URL token from its last segment. */
export function slugify(path: string): string {
  const base = workspaceBasename(path) || "project";
  return base.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "project";
}

/** registerWorkspace returns a stable slug for a path, minting a unique one if needed. */
export function registerWorkspace(path: string, options: RegisterOptions = {}): string {
  const { makeCurrent = true } = options;
  const reg = load();
  for (const [slug, registeredPath] of Object.entries(reg)) {
    if (registeredPath === path) {
      if (makeCurrent) setLast(path);
      return slug;
    }
  }
  const base = slugify(path);
  let slug = base;
  let n = 2;
  while (reg[slug] && reg[slug] !== path) slug = `${base}-${n++}`;
  reg[slug] = path;
  save(reg);
  if (makeCurrent) setLast(path);
  return slug;
}

/** resolveSlug returns the path for a slug, or null if unknown on this machine. */
export function resolveSlug(slug: string): string | null {
  return load()[slug] ?? null;
}

/** listWorkspaces returns every registered workspace (for legacy links and local routing). */
export function listWorkspaces(): Workspace[] {
  return Object.entries(load()).map(([slug, path]) => ({ slug, path }));
}

export function workspaceForPath(path: string): Workspace | null {
  return listWorkspaces().find((workspace) => workspace.path === path) ?? null;
}

/** lastWorkspace returns the most recently opened workspace, if any. */
export function lastWorkspace(): Workspace | null {
  const path = readLast();
  if (!path) return null;
  return { slug: registerWorkspace(path), path };
}

/** forget is retained for explicit removal only; switching clusters never calls it. */
export function forget(path: string): void {
  const reg = load();
  let changed = false;
  for (const [slug, registeredPath] of Object.entries(reg)) {
    if (registeredPath === path) {
      delete reg[slug];
      changed = true;
    }
  }
  if (changed) save(reg);
  if (readLast() === path) {
    localStorage.removeItem(LAST_KEY);
    emitRegistryChange();
  }
}

/** Refresh workspace consumers when this or another Carbon window changes the registry. */
export function subscribeToWorkspaceChanges(listener: () => void): () => void {
  const onStorage = (event: StorageEvent) => {
    if (event.key === REGISTRY_KEY || event.key === LAST_KEY) listener();
  };
  window.addEventListener(REGISTRY_CHANGE_EVENT, listener);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(REGISTRY_CHANGE_EVENT, listener);
    window.removeEventListener("storage", onStorage);
  };
}

/** React bridge for the storage/custom-event-backed workspace registry. */
export function useWorkspaces(): Workspace[] {
  const [workspaces, setWorkspaces] = useState<Workspace[]>(listWorkspaces);
  useEffect(() => {
    setWorkspaces(listWorkspaces());
    return subscribeToWorkspaceChanges(() => setWorkspaces(listWorkspaces()));
  }, []);
  return workspaces;
}
