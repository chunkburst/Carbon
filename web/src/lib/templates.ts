import type { CarbonImportance, CarbonTaskType } from "@/lib/carbon-api";
import type { Check } from "@/lib/api";
import { legacyCarbonStorageKey } from "@/lib/storage-identity";

export type LocalTaskTemplate = {
  id: string;
  name: string;
  title?: string;
  body?: string;
  deps?: string;
  labels?: string;
  priority?: string;
  projectId?: string;
  type?: CarbonTaskType;
  importance?: CarbonImportance;
  checks?: Check[];
  createdAt: string;
};

const key = (path: string) => `carbon-local-task-templates:${path}`;

function parseTemplates(raw: string | null): LocalTaskTemplate[] {
  try {
    const parsed: unknown = JSON.parse(raw ?? "[]");
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (value): value is LocalTaskTemplate =>
        !!value &&
        typeof value === "object" &&
        "id" in value &&
        typeof value.id === "string" &&
        "name" in value &&
        typeof value.name === "string",
    );
  } catch {
    return [];
  }
}

function mergeTemplates(legacy: LocalTaskTemplate[], current: LocalTaskTemplate[]): LocalTaskTemplate[] {
  const byName = new Map<string, LocalTaskTemplate>();
  for (const template of legacy) byName.set(template.name, template);
  // Preserve an edit already made under the stable key over an older copy.
  for (const template of current) byName.set(template.name, template);
  return [...byName.values()];
}

function migrateLegacyTemplates(path: string, current: LocalTaskTemplate[]): LocalTaskTemplate[] {
  const legacyPath = legacyCarbonStorageKey(path);
  if (!legacyPath) return current;

  const legacyKey = key(legacyPath);
  let raw: string | null;
  try {
    raw = localStorage.getItem(legacyKey);
  } catch {
    return current;
  }
  if (raw === null) return current;

  const merged = mergeTemplates(parseTemplates(raw), current);
  try {
    localStorage.setItem(key(path), JSON.stringify(merged));
  } catch {
    // Never remove the raw-path fallback unless its stable copy was written.
    return current;
  }
  try {
    localStorage.removeItem(legacyKey);
  } catch {
    // Keep serving the stable result and retry deletion next time.
  }
  return merged;
}

export function loadLocalTemplates(path: string): LocalTaskTemplate[] {
  try {
    return migrateLegacyTemplates(path, parseTemplates(localStorage.getItem(key(path))));
  } catch {
    return [];
  }
}

function save(path: string, templates: LocalTaskTemplate[]): void {
  localStorage.setItem(key(path), JSON.stringify(templates));
}

export function saveLocalTemplate(path: string, input: Omit<LocalTaskTemplate, "id" | "createdAt">): LocalTaskTemplate[] {
  const template: LocalTaskTemplate = {
    ...input,
    id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    createdAt: new Date().toISOString(),
  };
  const next = [...loadLocalTemplates(path).filter((item) => item.name !== template.name), template];
  save(path, next);
  return next;
}

export function removeLocalTemplate(path: string, id: string): LocalTaskTemplate[] {
  const next = loadLocalTemplates(path).filter((template) => template.id !== id);
  save(path, next);
  return next;
}
