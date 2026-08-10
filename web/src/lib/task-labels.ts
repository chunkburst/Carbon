import type { Translate } from "@/lib/i18n";

// These are persisted machine keys, not display strings. Keep their values stable
// across locales; only translate known built-ins at the presentation boundary.
export const CARBON_TASK_TYPES = ["foundation", "library", "patch", "extension", "plugin"] as const;
export const CARBON_IMPORTANCE = ["core", "important", "normal", "optional", "experimental"] as const;

const TASK_TYPE_LABELS: Record<(typeof CARBON_TASK_TYPES)[number], [string, string]> = {
  foundation: ["Foundation", "基础"],
  library: ["Library", "库"],
  patch: ["Patch", "补丁"],
  extension: ["Extension", "扩展功能"],
  plugin: ["Plugin", "插件"],
};

const IMPORTANCE_LABELS: Record<(typeof CARBON_IMPORTANCE)[number], [string, string]> = {
  core: ["Core", "核心"],
  important: ["Important", "重要"],
  normal: ["Normal", "普通"],
  optional: ["Optional", "可选"],
  experimental: ["Experimental", "实验"],
};

function builtInLabel(
  value: string | undefined,
  labels: Record<string, [string, string]>,
  t: Translate,
): string {
  if (!value) return "";
  const label = labels[value.trim().toLowerCase()];
  // Custom values are user content. Do not translate, normalize, or replace them.
  return label ? t(label[0], label[1]) : value;
}

export function carbonTaskTypeLabel(value: string | undefined, t: Translate): string {
  return builtInLabel(value, TASK_TYPE_LABELS, t);
}

export function carbonImportanceLabel(value: string | undefined, t: Translate): string {
  return builtInLabel(value, IMPORTANCE_LABELS, t);
}
