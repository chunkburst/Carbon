import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import type { Translate } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";

/**
 * A cluster-wide task view only adds useful context when there is another project
 * to see. Keeping the policy at the Carbon project boundary prevents individual
 * screens from inventing different definitions of a cluster workspace.
 */
export function isMultiProjectCluster(cluster: Pick<CarbonHomeCluster, "projects">): boolean {
  return cluster.projects.length > 1;
}

function sourceFolderName(path?: string): string | undefined {
  const normalized = path?.trim().replace(/[\\/]+$/, "");
  if (!normalized) return undefined;
  const name = normalized.split(/[\\/]/).at(-1)?.trim();
  return name || undefined;
}

/**
 * Project ids are implementation details, so never use them as the empty-state
 * fallback beneath a project name. A description wins; otherwise surface the
 * human-authored kind and the linked source-folder's basename.
 */
export function carbonProjectSummary(project: CarbonHomeProject, t: Translate): string {
  const description = project.description?.trim();
  if (description) return description;

  const kind = carbonTaskTypeLabel(project.kind, t);
  const source = sourceFolderName(project.source?.path);
  const sourceLabel = source ? t("Source: {name}", "源码：{name}", { name: source }) : "";
  const details = [kind, sourceLabel].filter(Boolean);
  return details.join(" · ") || t("No project description yet", "尚未填写项目简介");
}

/** Command's matching includes the visible description and useful project metadata. */
export function carbonProjectSearchText(project: CarbonHomeProject, cluster: CarbonHomeCluster): string {
  return [
    cluster.name,
    cluster.slug,
    cluster.description,
    project.name,
    project.slug,
    project.description,
    project.kind,
    sourceFolderName(project.source?.path),
  ]
    .filter(Boolean)
    .join(" ");
}
