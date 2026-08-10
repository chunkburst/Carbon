// Saved board views — named filter presets persisted per workspace in localStorage.

import { legacyCarbonStorageKey } from "@/lib/storage-identity";

export type SavedView = {
  name: string;
  filter: string; // base filter (all/backlog/ready/active/stalled/review)
  query?: string;
  label?: string;
  assignee?: string;
  priority?: string;
};

const key = (path: string) => `carbon-views:${path}`;
const legacyKey = (path: string) => `cairn-views:${path}`;

function parseViews(raw: string | null): SavedView[] {
  try {
    const parsed: unknown = JSON.parse(raw ?? "[]");
    return Array.isArray(parsed) ? parsed as SavedView[] : [];
  } catch {
    return [];
  }
}

function mergeViews(legacy: SavedView[], current: SavedView[]): SavedView[] {
  const byName = new Map<string, SavedView>();
  for (const view of legacy) if (view && typeof view.name === "string") byName.set(view.name, view);
  // Prefer a view already saved under the stable key if both versions have a name.
  for (const view of current) if (view && typeof view.name === "string") byName.set(view.name, view);
  return [...byName.values()];
}

function migrateLegacyViews(path: string, current: SavedView[]): SavedView[] {
  const legacyPaths = [...new Set([path, legacyCarbonStorageKey(path)].filter((value): value is string => !!value))];
  const legacyEntries: Array<{ key: string; raw: string }> = [];
  try {
    for (const legacyPath of legacyPaths) {
      const sourceKey = legacyKey(legacyPath);
      const raw = localStorage.getItem(sourceKey);
      if (raw !== null) legacyEntries.push({ key: sourceKey, raw });
    }
  } catch {
    return current;
  }
  if (!legacyEntries.length) return current;

  const merged = legacyEntries.reduce(
    (result, entry) => mergeViews(parseViews(entry.raw), result),
    current,
  );
  try {
    localStorage.setItem(key(path), JSON.stringify(merged));
  } catch {
    // Keep legacy entries if the Carbon copy could not be durably written.
    return current;
  }
  for (const entry of legacyEntries) {
    try {
      localStorage.removeItem(entry.key);
    } catch {
      // The migrated value remains usable; retry cleanup on a later load.
    }
  }
  return merged;
}

export function loadViews(path: string): SavedView[] {
  let current: SavedView[];
  try {
    current = parseViews(localStorage.getItem(key(path)));
  } catch {
    return [];
  }
  return migrateLegacyViews(path, current);
}

function save(path: string, views: SavedView[]) {
  localStorage.setItem(key(path), JSON.stringify(views));
}

export function addView(path: string, view: SavedView): SavedView[] {
  const next = [...loadViews(path).filter((v) => v.name !== view.name), view];
  save(path, next);
  return next;
}

export function removeView(path: string, name: string): SavedView[] {
  const next = loadViews(path).filter((v) => v.name !== name);
  save(path, next);
  return next;
}
