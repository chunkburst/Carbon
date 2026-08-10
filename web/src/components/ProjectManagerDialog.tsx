import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  ArrowLeft,
  ChevronDown,
  FolderCog,
  FolderPlus,
  FolderSearch,
  Loader2,
  Pencil,
  Plus,
  Search,
  Trash2,
  Wrench,
} from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
  CatalogIcon,
  catalogIconAssetURL,
  catalogIconFor,
  catalogIconsEqual,
  notifyCatalogIconAssetChanged,
  type CatalogIconMutation,
  type CatalogIconToken,
  type CatalogPresentationIcons,
} from "@/components/CatalogIcon";
import { CatalogIconPicker } from "@/components/CatalogIconPicker";
import { ProjectImageIconPicker, type ProjectImageIntent } from "@/components/ProjectImageIconPicker";
import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import {
  useAddHomeProject,
  useApplyLegacyMigration,
  useCreateHomeCluster,
  useClearHomeProjectTaskData,
  useDeleteHomeProject,
  useDeleteCarbonCatalogAsset,
  useHomeDoctor,
  useLegacyMigrationReceipts,
  usePreviewLegacyMigration,
  useRelinkHomeProject,
  useUploadCarbonCatalogAsset,
  useUpdateHomeCluster,
  useUpdateHomeProject,
} from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { isTauri, pickFolder } from "@/lib/tauri";

type ProjectSelection = { clusterId?: string; projectId: string };

function matchesSearch(value: string | undefined, query: string): boolean {
  return value?.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()) ?? false;
}

type ManagerProps = {
  home: string;
  clusters: CarbonHomeCluster[];
  projects?: CarbonHomeProject[];
  onOpenProject: (selection: ProjectSelection) => void;
  presentation?: CatalogPresentationIcons;
  onSetIcon?: (input: CatalogIconMutation) => Promise<void>;
};

export function ProjectManagerDialog({
  open,
  onOpenChange,
  onOpenManagementPage,
  ...props
}: ManagerProps & {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenManagementPage: () => void;
}) {
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[min(42rem,86vh)] max-w-5xl flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl">
        <DialogHeader className="border-b px-5 py-4 pr-14">
          <DialogTitle>{t("Projects", "项目")}</DialogTitle>
          <DialogDescription>{t("Open a task board or organize the projects in this Carbon Home.", "打开任务看板，或整理此 Carbon 主目录中的项目。")}</DialogDescription>
        </DialogHeader>
        <ProjectManagerContent {...props} compact onOpenManagementPage={onOpenManagementPage} />
      </DialogContent>
    </Dialog>
  );
}

export function ProjectManagementPage({
  home,
  clusters,
  projects,
  onOpenProject,
  presentation,
  onSetIcon,
  onBack,
  headerActions,
}: ManagerProps & {
  onBack: () => void;
  headerActions?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <div className="flex h-full min-w-0 flex-col bg-app">
      <header className="flex min-h-14 shrink-0 items-center gap-2 border-b bg-panel px-3 py-2">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft data-icon="inline-start" />
          {t("Back to tasks", "返回任务")}
        </Button>
        <div className="min-w-0 flex-1"><h1 className="text-base font-semibold">{t("Project management", "项目管理")}</h1></div>
        {headerActions}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <ProjectManagerContent
          home={home}
          clusters={clusters}
          projects={projects}
          onOpenProject={onOpenProject}
          presentation={presentation}
          onSetIcon={onSetIcon}
        />
        <AdvancedMaintenance home={home} />
      </div>
    </div>
  );
}

function ProjectManagerContent({
  home,
  clusters,
  projects = [],
  onOpenProject,
  presentation,
  onSetIcon,
  compact = false,
  onOpenManagementPage,
}: ManagerProps & {
  compact?: boolean;
  onOpenManagementPage?: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [extensionsOpen, setExtensionsOpen] = useState(false);
  const [selectedClusterId, setSelectedClusterId] = useState("");
  const [clusterEditor, setClusterEditor] = useState<{ open: boolean; cluster?: CarbonHomeCluster }>({ open: false });
  const [projectEditor, setProjectEditor] = useState<{ open: boolean; clusterId?: string; project?: CarbonHomeProject }>({ open: false });

  const visibleClusters = useMemo(
    () => clusters.filter((cluster) => !query.trim()
      || matchesSearch(cluster.name, query)
      || matchesSearch(cluster.slug, query)
      || matchesSearch(cluster.description, query)
      || cluster.projects.some((project) => matchesSearch(project.name, query) || matchesSearch(project.slug, query) || matchesSearch(project.description, query) || matchesSearch(project.source?.path, query))),
    [clusters, query],
  );
  const standaloneProjects = useMemo(
    () => projects.filter((project) => !query.trim()
      || matchesSearch(project.name, query)
      || matchesSearch(project.slug, query)
      || matchesSearch(project.description, query)
      || matchesSearch(project.source?.path, query)),
    [projects, query],
  );
  const selectedCluster = visibleClusters.find((cluster) => cluster.id === selectedClusterId) ?? visibleClusters[0];
  const visibleProjects = useMemo(
    () => selectedCluster?.projects.filter((project) => !query.trim()
      || matchesSearch(project.name, query)
      || matchesSearch(project.slug, query)
      || matchesSearch(project.description, query)
      || matchesSearch(project.source?.path, query)) ?? [],
    [query, selectedCluster],
  );

  useEffect(() => {
    if (selectedCluster && selectedCluster.id !== selectedClusterId) setSelectedClusterId(selectedCluster.id);
  }, [selectedCluster, selectedClusterId]);

  const openProject = (clusterId: string, projectId: string) => onOpenProject({ clusterId, projectId });

  return (
    <>
      <div className={compact ? "flex min-h-0 flex-1 flex-col" : "mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 sm:p-6"}>
        <div className={compact ? "flex flex-wrap items-center gap-2 border-b px-5 py-3" : "flex flex-wrap items-center gap-2"}>
          <div className="relative min-w-[min(100%,15rem)] flex-1">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input value={query} onChange={(event) => setQuery(event.target.value)} className="pl-8" placeholder={t("Search projects and groups", "搜索项目和分组")} aria-label={t("Search projects", "搜索项目")} />
          </div>
          <Button size="sm" onClick={() => setProjectEditor({ open: true })}>
            <FolderPlus data-icon="inline-start" />
            {t("New project", "新建项目")}
          </Button>
        </div>

        <section className="space-y-2 rounded-xl border bg-panel p-3" aria-label={t("Projects", "项目")}>
          <div className="flex items-center justify-between gap-3 px-1">
            <div className="min-w-0"><h2 className="text-sm font-semibold">{t("Projects", "项目")}</h2><p className="truncate text-xs text-muted-foreground">{t("Independent by default. Groups are optional extensions.", "默认独立创建；分组是可选扩展。")}</p></div>
            <span className="text-xs text-muted-foreground">{standaloneProjects.length}</span>
          </div>
          <div className="space-y-1">
            {standaloneProjects.map((project) => (
              <div key={project.id} className="flex items-center gap-3 rounded-lg border px-3 py-2.5 transition-colors hover:bg-muted/45">
                <span className="grid size-7 shrink-0 place-items-center overflow-hidden rounded-md bg-brand/10 text-brand"><CatalogIcon icon={catalogIconFor(presentation, "project", project.id)} assetURL={catalogIconAssetURL(home, "project", project.id)} fallback={FolderPlus} /></span>
                <div className="min-w-0 flex-1"><p className="break-words text-sm font-medium leading-5" title={project.name}>{project.name}</p><p className="mt-0.5 break-all text-xs text-muted-foreground">{project.source?.path ?? t("Source folder not linked", "尚未关联源代码文件夹")}</p></div>
                <div className="flex shrink-0 items-center gap-1"><Button variant="ghost" size="icon-sm" aria-label={t("Edit project", "编辑项目")} onClick={() => setProjectEditor({ open: true, project })}><Pencil /></Button><Button size="sm" onClick={() => onOpenProject({ projectId: project.id })}>{t("Open", "打开")}</Button></div>
              </div>
            ))}
            {!standaloneProjects.length && <div className="grid min-h-28 place-items-center rounded-lg border border-dashed p-4 text-center"><div><p className="text-sm font-medium">{query.trim() ? t("No matching project", "没有匹配项目") : t("No independent project yet", "还没有独立项目")}</p><Button className="mt-3" size="sm" onClick={() => setProjectEditor({ open: true })}><Plus data-icon="inline-start" />{t("Create project", "创建项目")}</Button></div></div>}
          </div>
        </section>

        <Collapsible open={extensionsOpen} onOpenChange={setExtensionsOpen} className="rounded-xl border bg-panel">
          <div className="flex items-center gap-3 px-4 py-3">
            <CollapsibleTrigger asChild><Button type="button" variant="ghost" size="sm" className="px-0"><ChevronDown className={`transition-transform ${extensionsOpen ? "rotate-180" : ""}`} />{t("Project groups (advanced)", "项目分组（高级）")}</Button></CollapsibleTrigger>
            <p className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{t("Create a group only for related project extensions.", "仅为相关项目扩展创建分组。")}</p>
            <Button variant="outline" size="sm" onClick={() => setClusterEditor({ open: true })}><Plus data-icon="inline-start" />{t("New group", "新建分组")}</Button>
          </div>
          <CollapsibleContent>
        <div className={compact ? "grid min-h-0 flex-1 md:grid-cols-[minmax(14rem,0.8fr)_minmax(0,1.7fr)]" : "grid min-h-[25rem] overflow-hidden border-t md:grid-cols-[minmax(15rem,0.8fr)_minmax(0,1.7fr)]"}>
          <section className="min-h-0 border-b bg-muted/15 p-2 md:border-r md:border-b-0" aria-label={t("Clusters", "项目集群")}>
            <div className="mb-1 flex items-center justify-between px-2 py-1.5 text-xs font-medium text-muted-foreground">
              <span>{t("Clusters", "项目集群")}</span>
              <span>{visibleClusters.length}</span>
            </div>
            <div className="max-h-[17rem] space-y-1 overflow-y-auto pr-1 md:max-h-none md:h-[calc(min(42rem,86vh)-10.6rem)]">
              {visibleClusters.map((cluster) => (
                <button
                  key={cluster.id}
                  type="button"
                  onClick={() => setSelectedClusterId(cluster.id)}
                  className={`w-full rounded-lg px-3 py-2.5 text-left transition-colors hover:bg-muted ${cluster.id === selectedCluster?.id ? "bg-muted" : ""}`}
                >
                  <div className="flex items-start gap-2">
                    <CatalogIcon icon={catalogIconFor(presentation, "cluster", cluster.id)} fallback={FolderCog} className="mt-0.5 text-brand" />
                    <div className="min-w-0 flex-1">
                      <p className="break-words text-sm font-medium leading-5" title={cluster.name}>{cluster.name}</p>
                      <p className="mt-0.5 text-xs text-muted-foreground">{cluster.projects.length} {t("projects", "个项目")}</p>
                    </div>
                  </div>
                </button>
              ))}
              {!visibleClusters.length && <p className="px-3 py-6 text-sm text-muted-foreground">{t("No matching cluster", "没有匹配的项目集群")}</p>}
            </div>
          </section>

          <section className="min-h-0 p-3" aria-label={t("Projects", "项目")}>
            {selectedCluster ? (
              <>
                <div className="mb-2 flex flex-wrap items-center gap-2 px-1 py-1">
                  <div className="flex min-w-0 flex-1 items-start gap-2">
                    <CatalogIcon icon={catalogIconFor(presentation, "cluster", selectedCluster.id)} fallback={FolderCog} className="mt-0.5 text-brand" />
                    <div className="min-w-0">
                      <p className="break-words text-sm font-semibold" title={selectedCluster.name}>{selectedCluster.name}</p>
                      {selectedCluster.description && <p className="mt-0.5 text-xs text-muted-foreground">{selectedCluster.description}</p>}
                    </div>
                  </div>
                  <Button variant="ghost" size="sm" onClick={() => setClusterEditor({ open: true, cluster: selectedCluster })}>
                    <Pencil data-icon="inline-start" />
                    {t("Edit", "编辑")}
                  </Button>
                </div>
                <div className="max-h-[22rem] space-y-1 overflow-y-auto pr-1 md:max-h-none md:h-[calc(min(42rem,86vh)-11.1rem)]">
                  {visibleProjects.map((project) => (
                    <div key={project.id} className="flex items-center gap-3 rounded-lg border px-3 py-2.5 transition-colors hover:bg-muted/45">
                      <CatalogIcon icon={catalogIconFor(presentation, "project", project.id)} assetURL={catalogIconAssetURL(home, "project", project.id)} fallback={FolderPlus} className="text-brand" />
                      <div className="min-w-0 flex-1">
                        <p className="break-words text-sm font-medium leading-5" title={project.name}>{project.name}</p>
                        <p className="mt-0.5 break-all text-xs text-muted-foreground">{project.source?.path ?? t("Source folder not linked", "尚未关联源码文件夹")}</p>
                      </div>
                      <div className="flex shrink-0 items-center gap-1">
                        <Button variant="ghost" size="icon-sm" aria-label={t("Edit project", "编辑项目")} onClick={() => setProjectEditor({ open: true, clusterId: selectedCluster.id, project })}><Pencil /></Button>
                        <Button size="sm" onClick={() => openProject(selectedCluster.id, project.id)}>{t("Open", "打开")}</Button>
                      </div>
                    </div>
                  ))}
                  {!visibleProjects.length && <div className="grid min-h-40 place-items-center rounded-lg border border-dashed p-5 text-center"><div><p className="text-sm font-medium">{t("No project here yet", "这里还没有项目")}</p><Button className="mt-3" size="sm" onClick={() => setProjectEditor({ open: true, clusterId: selectedCluster.id })}><Plus data-icon="inline-start" />{t("Create project", "创建项目")}</Button></div></div>}
                </div>
              </>
            ) : <div className="grid h-full min-h-48 place-items-center text-sm text-muted-foreground">{t("Choose a cluster", "选择一个项目集群")}</div>}
          </section>
        </div>
          </CollapsibleContent>
        </Collapsible>

        {compact && onOpenManagementPage && <div className="flex justify-end border-t px-5 py-3"><Button variant="ghost" size="sm" onClick={onOpenManagementPage}>{t("Open full management page", "打开完整管理页")}</Button></div>}
      </div>

      <ClusterEditorDialog
        open={clusterEditor.open}
        onOpenChange={(open) => setClusterEditor((current) => ({ ...current, open }))}
        home={home}
        cluster={clusterEditor.cluster}
        icon={catalogIconFor(presentation, "cluster", clusterEditor.cluster?.id)}
        onSetIcon={onSetIcon}
      />
      <ProjectEditorDialog
        open={projectEditor.open}
        onOpenChange={(open) => setProjectEditor((current) => ({ ...current, open }))}
        home={home}
        clusters={clusters}
        initialClusterId={projectEditor.clusterId}
        project={projectEditor.project}
        icon={catalogIconFor(presentation, "project", projectEditor.project?.id)}
        onSetIcon={onSetIcon}
        onCreated={onOpenProject}
      />
    </>
  );
}

function ClusterEditorDialog({
  open,
  onOpenChange,
  home,
  cluster,
  icon: initialIcon,
  onSetIcon,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  home: string;
  cluster?: CarbonHomeCluster;
  icon?: CatalogIconToken | null;
  onSetIcon?: (input: CatalogIconMutation) => Promise<void>;
}) {
  const { t } = useI18n();
  const create = useCreateHomeCluster(home);
  const update = useUpdateHomeCluster(home);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [selectedIcon, setSelectedIcon] = useState<CatalogIconToken | null>(null);
  const [savingIcon, setSavingIcon] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName(cluster?.name ?? "");
    setSlug(cluster?.slug ?? "");
    setDescription(cluster?.description ?? "");
    setSelectedIcon(initialIcon ?? null);
    setError(null);
  }, [cluster, initialIcon, open]);

  const submit = async () => {
    if (!name.trim()) return;
    setError(null);
    try {
      const result = cluster
        ? await update.mutateAsync({ clusterId: cluster.id, name: name.trim(), slug: slug.trim(), description: description.trim() })
        : await create.mutateAsync({ name: name.trim(), slug: slug.trim() || undefined, description: description.trim() || undefined });
      if (!result.available) {
        setError(t("This Carbon sidecar cannot save clusters.", "此 Carbon 本地服务无法保存项目集群。"));
        return;
      }
      if (cluster && onSetIcon && !catalogIconsEqual(selectedIcon, initialIcon)) {
        setSavingIcon(true);
        try {
          await onSetIcon({ target: "cluster", id: cluster.id, icon: selectedIcon });
        } finally {
          setSavingIcon(false);
        }
      }
      onOpenChange(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const busy = create.isPending || update.isPending || savingIcon;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{cluster ? t("Edit cluster", "编辑项目集群") : t("New cluster", "新建项目集群")}</DialogTitle>
          <DialogDescription>{t("A cluster groups related projects without mixing their project-level tasks.", "项目集群将相关项目聚在一起，但不会混合各项目自己的任务。")}</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <FieldGroup className="gap-4">
            <Field data-invalid={!!error}>
              <FieldLabel htmlFor="manager-cluster-name">{t("Cluster name", "项目集群名称")}</FieldLabel>
              <Input id="manager-cluster-name" autoFocus value={name} onChange={(event) => setName(event.target.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="manager-cluster-slug">{t("English ID", "英文标识")}</FieldLabel>
              <Input id="manager-cluster-slug" value={slug} onChange={(event) => setSlug(event.target.value)} placeholder={t("Optional", "可选")} />
            </Field>
            <Field>
              <FieldLabel htmlFor="manager-cluster-description">{t("Description", "描述")}</FieldLabel>
              <Textarea id="manager-cluster-description" value={description} onChange={(event) => setDescription(event.target.value)} placeholder={t("Optional", "可选")} rows={3} />
            </Field>
            {cluster && onSetIcon && (
              <Field>
                <FieldLabel>{t("Cluster icon", "项目集群图标")}</FieldLabel>
                <CatalogIconPicker
                  value={selectedIcon}
                  onChange={setSelectedIcon}
                  disabled={busy}
                  ariaLabel={t("Choose cluster icon", "选择项目集群图标")}
                />
              </Field>
            )}
            <FieldError>{error}</FieldError>
          </FieldGroup>
          <DialogFooter>
            <Button type="submit" disabled={!name.trim() || busy}>{busy && <Loader2 className="animate-spin" />}{cluster ? t("Save cluster", "保存项目集群") : t("Create cluster", "创建项目集群")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ProjectEditorDialog({
  open,
  onOpenChange,
  home,
  clusters,
  initialClusterId,
  project,
  icon: initialIcon,
  onSetIcon,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  home: string;
  clusters: CarbonHomeCluster[];
  initialClusterId?: string;
  project?: CarbonHomeProject;
  icon?: CatalogIconToken | null;
  onSetIcon?: (input: CatalogIconMutation) => Promise<void>;
  onCreated: (selection: ProjectSelection) => void;
}) {
  const { t } = useI18n();
  const create = useAddHomeProject(home);
  const update = useUpdateHomeProject(home);
  const relink = useRelinkHomeProject(home);
  const uploadAsset = useUploadCarbonCatalogAsset(home);
  const deleteAsset = useDeleteCarbonCatalogAsset(home);
  const clearTaskData = useClearHomeProjectTaskData(home);
  const deleteProject = useDeleteHomeProject(home);
  const [clusterId, setClusterId] = useState("");
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [kind, setKind] = useState("foundation");
  const [sourcePath, setSourcePath] = useState("");
  const [selectedIcon, setSelectedIcon] = useState<CatalogIconToken | null>(null);
  const [imageIntent, setImageIntent] = useState<ProjectImageIntent>({ kind: "unchanged" });
  const [createdProject, setCreatedProject] = useState<{ project: CarbonHomeProject; clusterId?: string } | null>(null);
  const [assetRevision, setAssetRevision] = useState(0);
  const [placementOpen, setPlacementOpen] = useState(false);
  const [savingIcon, setSavingIcon] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [clearTaskDataStage, setClearTaskDataStage] = useState<1 | 2 | null>(null);
  const [clearTaskDataName, setClearTaskDataName] = useState("");
  const [clearTaskDataError, setClearTaskDataError] = useState<string | null>(null);
  const [deleteProjectStage, setDeleteProjectStage] = useState<1 | 2 | null>(null);
  const [deleteProjectName, setDeleteProjectName] = useState("");
  const [deleteProjectData, setDeleteProjectData] = useState(false);
  const [deleteProjectError, setDeleteProjectError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    // Omitting the cluster is the normal project-first path.  A group is chosen
    // only by an explicit advanced action or when editing a legacy grouped item.
    setClusterId(initialClusterId ?? "");
    setName(project?.name ?? "");
    setSlug(project?.slug ?? "");
    setDescription(project?.description ?? "");
    setKind(project?.kind ?? "foundation");
    setSourcePath(project?.source?.path ?? "");
    setSelectedIcon(initialIcon ?? null);
    setImageIntent({ kind: "unchanged" });
    setCreatedProject(null);
    setAssetRevision(0);
    setPlacementOpen(Boolean(initialClusterId));
    setError(null);
    setClearTaskDataStage(null);
    setClearTaskDataName("");
    setClearTaskDataError(null);
    setDeleteProjectStage(null);
    setDeleteProjectName("");
    setDeleteProjectData(false);
    setDeleteProjectError(null);
  }, [clusters, initialClusterId, initialIcon, open, project]);

  const chooseSourceFolder = async () => {
    const picked = await pickFolder();
    if (picked) setSourcePath(picked);
  };

  const closeTaskDataClear = () => {
    if (clearTaskData.isPending) return;
    setClearTaskDataStage(null);
    setClearTaskDataName("");
    setClearTaskDataError(null);
  };

  const clearProjectTaskData = async () => {
    if (!project || clearTaskDataName !== project.name || clearTaskData.isPending) return;
    setClearTaskDataError(null);
    try {
      const result = await clearTaskData.mutateAsync({
        clusterId: clusterId || undefined,
        projectId: project.id,
        // Do not trim or normalize: the server receives exactly what the user
        // typed and enforces the same destructive-action confirmation boundary.
        name: clearTaskDataName,
      });
      if (!result.available) {
        setClearTaskDataError(t("This Carbon sidecar cannot clear project task data.", "此 Carbon sidecar 不支持清空项目任务数据。"));
        return;
      }
      setClearTaskDataStage(null);
      setClearTaskDataName("");
      setClearTaskDataError(null);
    } catch (cause) {
      setClearTaskDataError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const closeProjectDelete = () => {
    if (deleteProject.isPending) return;
    setDeleteProjectStage(null);
    setDeleteProjectName("");
    setDeleteProjectData(false);
    setDeleteProjectError(null);
  };

  const deleteProjectFromCatalog = async () => {
    if (!project || deleteProjectName !== project.name || deleteProject.isPending) return;
    setDeleteProjectError(null);
    try {
      const result = await deleteProject.mutateAsync({
        clusterId: clusterId || undefined,
        projectId: project.id,
        // Keep the typed confirmation byte-for-byte. The server requires the fresh
        // exact name and does not trim, fold case, or accept a display-name target.
        name: deleteProjectName,
        deleteData: deleteProjectData,
      });
      if (!result.available) {
        setDeleteProjectError(t("This Carbon sidecar cannot delete projects.", "此 Carbon 本地服务不支持删除项目。"));
        return;
      }
      closeProjectDelete();
      // Home invalidation in the mutation makes a currently selected deleted project
      // fall back through HomeShell's normal selection path. Closing the editor avoids
      // retaining a stale editable project object while that refetch lands.
      onOpenChange(false);
    } catch (cause) {
      setDeleteProjectError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const submit = async () => {
    const source = sourcePath.trim();
    if (!name.trim() || !source) return;
    setError(null);
    try {
      const persistPresentation = async (target: CarbonHomeProject, targetClusterId?: string) => {
        if (onSetIcon && !catalogIconsEqual(selectedIcon, initialIcon)) {
          setSavingIcon(true);
          try {
            await onSetIcon({ target: "project", id: target.id, icon: selectedIcon });
          } finally {
            setSavingIcon(false);
          }
        }
        if (imageIntent.kind === "upload") {
          const result = await uploadAsset.mutateAsync({ target: "project", id: target.id, file: imageIntent.file });
          if (!result.available) throw new Error(t("Project image upload needs a newer Carbon sidecar.", "项目图片上传需要更新的 Carbon 本地服务。"));
          setAssetRevision((value) => value + 1);
          notifyCatalogIconAssetChanged();
        } else if (imageIntent.kind === "clear") {
          const result = await deleteAsset.mutateAsync({ target: "project", id: target.id });
          if (!result.available) throw new Error(t("Project image removal needs a newer Carbon sidecar.", "项目图片移除需要更新的 Carbon 本地服务。"));
          setAssetRevision((value) => value + 1);
          notifyCatalogIconAssetChanged();
        }
        onOpenChange(false);
        onCreated({ clusterId: targetClusterId, projectId: target.id });
      };

      // Creation is deliberately a one-shot transition.  If the later icon
      // upload fails, retain the returned stable project ID and retry only its
      // presentation mutation instead of sending another create request.
      if (createdProject) {
        await persistPresentation(createdProject.project, createdProject.clusterId);
        return;
      }

      if (!project) {
        const result = await create.mutateAsync({
          clusterId: clusterId || undefined,
          name: name.trim(),
          slug: slug.trim() || undefined,
          description: description.trim() || undefined,
          kind: kind.trim() || undefined,
          sourcePath: source,
        });
        if (!result.available) {
          setError(t("This Carbon sidecar cannot add projects.", "此 Carbon 本地服务无法添加项目。"));
          return;
        }
        const target = { project: result.data, clusterId: clusterId || undefined };
        setCreatedProject(target);
        try {
          await persistPresentation(target.project, target.clusterId);
        } catch (cause) {
          setError(t("Project was created. Fix the icon error and retry; another project will not be created.", "项目已创建。请修复图标错误后重试；不会重复创建项目。"));
          throw cause;
        }
        return;
      }

      const detailsChanged = name.trim() !== project.name
        || slug.trim() !== (project.slug ?? "")
        || description.trim() !== (project.description ?? "")
        || kind.trim() !== (project.kind ?? "foundation");
      if (detailsChanged) {
        const result = await update.mutateAsync({
          clusterId: clusterId || undefined,
          projectId: project.id,
          name: name.trim(),
          slug: slug.trim(),
          description: description.trim(),
          kind: kind.trim() || undefined,
        });
        if (!result.available) {
          setError(t("This Carbon sidecar cannot save projects.", "此 Carbon 本地服务无法保存项目。"));
          return;
        }
      }
      if (source !== (project.source?.path ?? "")) {
        const result = await relink.mutateAsync({ clusterId: clusterId || undefined, projectId: project.id, sourcePath: source });
        if (!result.available) {
          setError(t("This Carbon sidecar cannot link this source folder.", "此 Carbon 本地服务无法关联该源码文件夹。"));
          return;
        }
      }
      await persistPresentation(project, clusterId || undefined);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const busy = create.isPending || update.isPending || relink.isPending || uploadAsset.isPending || deleteAsset.isPending || clearTaskData.isPending || deleteProject.isPending || savingIcon;
  return (
    <>
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen && deleteProject.isPending) return;
      onOpenChange(nextOpen);
    }}>
      <DialogContent className="max-h-[86vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{project ? t("Edit project", "编辑项目") : t("New project", "新建项目")}</DialogTitle>
          <DialogDescription>{t("A project has its own task boundary and links to a real source folder.", "项目拥有自己的任务边界，并关联到真实源码文件夹。")}</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <FieldGroup className="gap-4">
            {!project && (
              <Collapsible open={placementOpen} onOpenChange={setPlacementOpen} className="rounded-lg border bg-muted/15">
                <CollapsibleTrigger asChild><Button type="button" variant="ghost" size="sm" className="w-full justify-start"><ChevronDown className={`transition-transform ${placementOpen ? "rotate-180" : ""}`} />{t("Add to a project group (advanced)", "添加到项目分组（高级）")}</Button></CollapsibleTrigger>
                <CollapsibleContent>
                  <Field className="border-t px-3 py-3">
                    <FieldLabel htmlFor="manager-project-cluster">{t("Project group", "项目分组")}</FieldLabel>
                    <Select value={clusterId || "standalone"} onValueChange={(value) => setClusterId(value === "standalone" ? "" : value)}>
                      <SelectTrigger id="manager-project-cluster"><SelectValue /></SelectTrigger>
                      <SelectContent><SelectItem value="standalone">{t("Independent project (default)", "独立项目（默认）")}</SelectItem>{clusters.map((cluster) => <SelectItem key={cluster.id} value={cluster.id}>{cluster.name}</SelectItem>)}</SelectContent>
                    </Select>
                    <p className="mt-1 text-xs text-muted-foreground">{t("Groups are for optional related-project extensions; they are not required for a task board.", "分组仅用于可选的关联项目扩展；任务看板不需要分组。")}</p>
                  </Field>
                </CollapsibleContent>
              </Collapsible>
            )}
            <Field data-invalid={!!error}>
              <FieldLabel htmlFor="manager-project-name">{t("Project name", "项目名称")}</FieldLabel>
              <Input id="manager-project-name" autoFocus value={name} onChange={(event) => setName(event.target.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="manager-project-slug">{t("English ID", "英文标识")}</FieldLabel>
              <Input id="manager-project-slug" value={slug} onChange={(event) => setSlug(event.target.value)} placeholder={t("Optional", "可选")} />
            </Field>
            <Field>
              <FieldLabel htmlFor="manager-project-kind">{t("Project type", "项目类型")}</FieldLabel>
              <Input id="manager-project-kind" value={kind} onChange={(event) => setKind(event.target.value)} placeholder="foundation" />
            </Field>
            <Field>
              <FieldLabel htmlFor="manager-project-description">{t("Description", "描述")}</FieldLabel>
              <Textarea id="manager-project-description" value={description} onChange={(event) => setDescription(event.target.value)} placeholder={t("Optional", "可选")} rows={2} />
            </Field>
            <Field data-invalid={!!error}>
              <FieldLabel htmlFor="manager-project-source">{t("Source folder", "源码文件夹")}</FieldLabel>
              <div className="flex gap-2">
                <Input id="manager-project-source" value={sourcePath} onChange={(event) => setSourcePath(event.target.value)} placeholder={t("A real project folder path", "填写真实项目文件夹路径")} className="font-mono text-sm" />
                {isTauri() && <Button type="button" variant="outline" size="icon" aria-label={t("Choose source folder", "选择源码文件夹")} onClick={() => void chooseSourceFolder()}><FolderSearch /></Button>}
              </div>
              <FieldError>{error}</FieldError>
            </Field>
            <Field>
              <FieldLabel>{t("Project icon", "项目图标")}</FieldLabel>
              <div className="space-y-3">
                {onSetIcon && <CatalogIconPicker value={selectedIcon} assetURL={imageIntent.kind === "unchanged" ? catalogIconAssetURL(home, "project", project?.id ?? createdProject?.project.id, assetRevision) : undefined} onChange={setSelectedIcon} disabled={busy} ariaLabel={t("Choose project icon", "选择项目图标")} />}
                <ProjectImageIconPicker
                  home={home}
                  projectId={project?.id ?? createdProject?.project.id}
                  token={selectedIcon}
                  intent={imageIntent}
                  assetRevision={assetRevision}
                  disabled={busy}
                  pending={uploadAsset.isPending || deleteAsset.isPending}
                  onIntentChange={setImageIntent}
                />
              </div>
            </Field>
            {project && (
              <section className="flex flex-col gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-4" aria-label={t("Danger zone", "危险操作区")}>
                <div>
                  <h3 className="text-sm font-medium text-destructive">{t("Danger zone", "危险操作区")}</h3>
                  <p className="mt-1 text-sm text-muted-foreground">{t("Manage irreversible catalog and task-data actions for this project.", "管理该项目不可逆的目录与任务数据操作。")}</p>
                </div>
                <div className="flex flex-col gap-2">
                  <p className="text-sm font-medium">{t("Clear task data", "清空任务数据")}</p>
                  <p className="text-sm text-muted-foreground">{t("Clear Carbon task records while keeping this project in the catalog.", "清除 Carbon 任务记录，但保留项目目录条目。")}</p>
                  <Alert variant="destructive">
                    <AlertTitle>{t("This cannot be undone", "此操作无法撤销")}</AlertTitle>
                    <AlertDescription>{t("Task IDs keep increasing after a clear; existing IDs are never reset or reused.", "清空后任务 ID 仍会继续递增；既有 ID 不会重置或复用。")}</AlertDescription>
                  </Alert>
                  <Button type="button" variant="destructive" size="sm" className="self-start" disabled={busy} onClick={() => {
                    setClearTaskDataName("");
                    setClearTaskDataError(null);
                    setClearTaskDataStage(1);
                  }}>
                    <Trash2 data-icon="inline-start" />
                    {t("Clear task data", "清空任务数据")}
                  </Button>
                </div>
                <div className="border-t border-destructive/20" />
                <div className="flex flex-col gap-2">
                  <p className="text-sm font-medium text-destructive">{t("Delete project", "删除项目")}</p>
                  <p className="text-sm text-muted-foreground">{t("Remove this project from the Carbon Home catalog. Its source folder is always retained.", "将项目移出 Carbon 项目目录；源码文件夹永远保留。")}</p>
                  <Button type="button" variant="destructive" size="sm" className="self-start" disabled={busy} onClick={() => {
                    setDeleteProjectName("");
                    setDeleteProjectData(false);
                    setDeleteProjectError(null);
                    setDeleteProjectStage(1);
                  }}>
                    <Trash2 data-icon="inline-start" />
                    {t("Delete project", "删除项目")}
                  </Button>
                </div>
              </section>
            )}
          </FieldGroup>
          <DialogFooter>
            <Button type="submit" disabled={!name.trim() || !sourcePath.trim() || busy}>{busy && <Loader2 className="animate-spin" />}{createdProject ? t("Retry icon and open", "重试图标并打开") : project ? t("Save project", "保存项目") : t("Create and open", "创建并打开")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
    {project && (
      <ProjectTaskDataClearDialog
        project={project}
        stage={clearTaskDataStage}
        confirmationName={clearTaskDataName}
        error={clearTaskDataError}
        pending={clearTaskData.isPending}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) closeTaskDataClear();
        }}
        onContinue={() => setClearTaskDataStage(2)}
        onConfirmationNameChange={(value) => {
          setClearTaskDataName(value);
          setClearTaskDataError(null);
        }}
        onConfirm={() => void clearProjectTaskData()}
      />
    )}
    {project && (
      <ProjectDeleteDialog
        project={project}
        stage={deleteProjectStage}
        confirmationName={deleteProjectName}
        deleteData={deleteProjectData}
        error={deleteProjectError}
        pending={deleteProject.isPending}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) closeProjectDelete();
        }}
        onContinue={() => setDeleteProjectStage(2)}
        onConfirmationNameChange={(value) => {
          setDeleteProjectName(value);
          setDeleteProjectError(null);
        }}
        onDeleteDataChange={(value) => {
          setDeleteProjectData(value);
          setDeleteProjectError(null);
        }}
        onConfirm={() => void deleteProjectFromCatalog()}
      />
    )}
    </>
  );
}

function ProjectTaskDataClearDialog({
  project,
  stage,
  confirmationName,
  error,
  pending,
  onOpenChange,
  onContinue,
  onConfirmationNameChange,
  onConfirm,
}: {
  project: CarbonHomeProject;
  stage: 1 | 2 | null;
  confirmationName: string;
  error: string | null;
  pending: boolean;
  onOpenChange: (open: boolean) => void;
  onContinue: () => void;
  onConfirmationNameChange: (value: string) => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  const nameMatches = confirmationName === project.name;

  return (
    <AlertDialog open={stage !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-h-[86vh] overflow-y-auto">
        <AlertDialogHeader>
          <AlertDialogTitle>{stage === 1 ? t("Clear project task data?", "清空项目任务数据？") : t("Confirm task data clear", "确认清空任务数据")}</AlertDialogTitle>
          <AlertDialogDescription>
            {stage === 1
              ? t("Review exactly what this irreversible operation changes before continuing.", "继续前请确认这项不可逆操作会影响的范围。")
              : t("Type the project name exactly as shown. Case and spaces must match.", "请按显示内容原样输入项目名称；大小写和空格必须完全一致。")}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {stage === 1 && (
          <div className="flex flex-col gap-3">
            <Alert variant="destructive">
              <AlertTitle>{t("Will permanently delete", "将永久删除")}</AlertTitle>
              <AlertDescription>
                <ul className="ml-4 flex list-disc flex-col gap-1">
                  <li>{t("All tasks and their task-level records, including details, dependencies, checks, evidence, and notes.", "该项目的全部任务及其任务级记录，包括详情、依赖、检查、证明和备注。")}</li>
                  <li>{t("This project's task data in Trash.", "该项目回收站中的任务数据。")}</li>
                </ul>
              </AlertDescription>
            </Alert>
            <Alert>
              <AlertTitle>{t("Will keep", "将保留")}</AlertTitle>
              <AlertDescription>
                <ul className="ml-4 flex list-disc flex-col gap-1">
                  <li>{t("The project, its configuration, source-folder link, and icon.", "项目本身、项目配置、源码文件夹关联和图标。")}</li>
                  <li>{t("Workers, aliases, and lifecycle records.", "Worker、别名及其生命周期记录。")}</li>
                  <li>{t("Work Logs. New task IDs continue increasing and are never reused.", "工作日志。新建任务的 ID 会继续递增，绝不会重置或复用。")}</li>
                </ul>
              </AlertDescription>
            </Alert>
          </div>
        )}

        {stage === 2 && (
          <FieldGroup className="gap-3">
            <Field data-invalid={Boolean(error)}>
              <FieldLabel htmlFor={`clear-project-task-data-${project.id}`}>{t("Project name", "项目名称")}</FieldLabel>
              <FieldDescription>
                {t("Enter the following name exactly:", "请原样输入以下名称：")}{" "}
                <code className="whitespace-pre-wrap rounded bg-muted px-1 py-0.5 font-mono text-foreground">{project.name}</code>
              </FieldDescription>
              <Input
                id={`clear-project-task-data-${project.id}`}
                autoFocus
                value={confirmationName}
                onChange={(event) => onConfirmationNameChange(event.target.value)}
                placeholder={project.name}
                disabled={pending}
                aria-invalid={Boolean(error)}
                autoComplete="off"
              />
              <FieldError>{error}</FieldError>
            </Field>
          </FieldGroup>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("Cancel", "取消")}</AlertDialogCancel>
          {stage === 1 ? (
            <Button type="button" variant="destructive" disabled={pending} onClick={onContinue}>{t("Continue", "继续")}</Button>
          ) : (
            <Button type="button" variant="destructive" disabled={pending || !nameMatches} onClick={onConfirm}>
              {pending && <Loader2 data-icon="inline-start" className="animate-spin" />}
              {t("Permanently clear task data", "永久清空任务数据")}
            </Button>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ProjectDeleteDialog({
  project,
  stage,
  confirmationName,
  deleteData,
  error,
  pending,
  onOpenChange,
  onContinue,
  onConfirmationNameChange,
  onDeleteDataChange,
  onConfirm,
}: {
  project: CarbonHomeProject;
  stage: 1 | 2 | null;
  confirmationName: string;
  deleteData: boolean;
  error: string | null;
  pending: boolean;
  onOpenChange: (open: boolean) => void;
  onContinue: () => void;
  onConfirmationNameChange: (value: string) => void;
  onDeleteDataChange: (value: boolean) => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  const nameMatches = confirmationName === project.name;
  const checkboxID = `delete-project-task-data-${project.id}`;

  return (
    <AlertDialog open={stage !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-h-[86vh] overflow-y-auto">
        <AlertDialogHeader>
          <AlertDialogTitle>{stage === 1 ? t("Delete project?", "删除项目？") : t("Confirm project deletion", "确认删除项目")}</AlertDialogTitle>
          <AlertDialogDescription>
            {stage === 1
              ? t("Review what will change before continuing.", "继续前请确认此操作会改变的范围。")
              : t("Type the project name exactly as shown. Case and spaces must match.", "请按显示内容原样输入项目名称；大小写和空格必须完全一致。")}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {stage === 1 && (
          <div className="flex flex-col gap-3">
            <Alert variant="destructive">
              <AlertTitle>{t("Will remove", "将移除")}</AlertTitle>
              <AlertDescription>
                <ul className="ml-4 flex list-disc flex-col gap-1">
                  <li>{t("This project's entry from the Carbon Home catalog.", "该项目在 Carbon 项目目录中的条目。")}</li>
                  <li>{deleteData
                    ? t("This project's Carbon task records, including tasks, Trash, sessions, live sessions, and check runs.", "该项目的 Carbon 任务记录，包括任务、回收站、会话、实时会话和检查运行记录。")
                    : t("No Carbon task data. This is catalog-only removal.", "不清除 Carbon 任务数据；这只是移出项目目录。")}</li>
                </ul>
              </AlertDescription>
            </Alert>
            <FieldGroup className="gap-3">
              <Field orientation="horizontal" data-disabled={pending || undefined}>
                <Checkbox
                  id={checkboxID}
                  checked={deleteData}
                  disabled={pending}
                  onCheckedChange={(checked) => onDeleteDataChange(checked === true)}
                />
                <FieldContent>
                  <FieldLabel htmlFor={checkboxID}>{t("Also clear this project's Carbon task data", "同时清除该项目的 Carbon 任务数据")}</FieldLabel>
                  <FieldDescription>{t("When unchecked, this only removes the project from the catalog; task data is retained.", "未勾选时仅移出项目目录，任务数据保留。")}</FieldDescription>
                </FieldContent>
              </Field>
            </FieldGroup>
            <Alert>
              <AlertTitle>{t("Will always keep", "将始终保留")}</AlertTitle>
              <AlertDescription>
                <ul className="ml-4 flex list-disc flex-col gap-1">
                  <li>{t("The source folder. Carbon never deletes source files or the linked source folder.", "源码文件夹。Carbon 永远不会删除源码文件或已关联的源码文件夹。")}</li>
                  <li>{t("The internal Carbon data root and project-level configuration, templates, views, and Work Logs.", "内部 Carbon 数据根，以及项目级配置、模板、视图和 Work Logs。")}</li>
                  <li>{t("Other projects, shared cluster roots, peer task data, and cluster-wide tasks.", "其他项目、共享集群根、同级项目任务数据和集群级任务。")}</li>
                </ul>
              </AlertDescription>
            </Alert>
          </div>
        )}

        {stage === 2 && (
          <FieldGroup className="gap-3">
            <Alert>
              <AlertTitle>{deleteData ? t("Carbon task data will be cleared", "将清除 Carbon 任务数据") : t("Task data will be retained", "任务数据将被保留")}</AlertTitle>
              <AlertDescription>{deleteData
                ? t("Only task-shaped records are cleared; the source folder, internal root, configuration, templates, views, and Work Logs stay in place.", "仅清除任务相关记录；源码文件夹、内部根、配置、模板、视图和 Work Logs 均会保留。")
                : t("This only removes the project from the Carbon catalog. Its task data remains in the internal Carbon root.", "这只会将项目移出 Carbon 项目目录；任务数据仍保留在内部 Carbon 根目录。")}</AlertDescription>
            </Alert>
            <Field data-invalid={Boolean(error)}>
              <FieldLabel htmlFor={`delete-project-name-${project.id}`}>{t("Project name", "项目名称")}</FieldLabel>
              <FieldDescription>
                {t("Enter the following name exactly:", "请原样输入以下名称：")}{" "}
                <code className="whitespace-pre-wrap rounded bg-muted px-1 py-0.5 font-mono text-foreground">{project.name}</code>
              </FieldDescription>
              <Input
                id={`delete-project-name-${project.id}`}
                autoFocus
                value={confirmationName}
                onChange={(event) => onConfirmationNameChange(event.target.value)}
                placeholder={project.name}
                disabled={pending}
                aria-invalid={Boolean(error)}
                autoComplete="off"
              />
              <FieldError>{error}</FieldError>
            </Field>
          </FieldGroup>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("Cancel", "取消")}</AlertDialogCancel>
          {stage === 1 ? (
            <Button type="button" variant="destructive" disabled={pending} onClick={onContinue}>{t("Continue", "继续")}</Button>
          ) : (
            <Button type="button" variant="destructive" disabled={pending || !nameMatches} onClick={onConfirm}>
              {pending && <Loader2 data-icon="inline-start" className="animate-spin" />}
              {t("Delete project", "删除项目")}
            </Button>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function AdvancedMaintenance({ home }: { home: string }) {
  const { t } = useI18n();
  const doctor = useHomeDoctor(home);
  const receipts = useLegacyMigrationReceipts(home);
  const previewMigration = usePreviewLegacyMigration(home);
  const applyMigration = useApplyLegacyMigration(home);
  const [legacyCluster, setLegacyCluster] = useState("");
  const [reviewedLegacyCluster, setReviewedLegacyCluster] = useState("");
  const [configPolicy, setConfigPolicy] = useState("");
  const report = doctor.data?.available ? doctor.data.data : undefined;
  const plan = previewMigration.data?.available ? previewMigration.data.data : undefined;
  const receiptHistory = receipts.data?.available ? receipts.data.data.receipts : [];
  const requiresPrimaryPolicy = (plan?.configConflicts?.length ?? 0) > 0;
  const canApply = Boolean(plan && legacyCluster.trim() === reviewedLegacyCluster && (!requiresPrimaryPolicy || configPolicy === "primary"));

  return (
    <section className="mx-auto w-full max-w-6xl space-y-4 px-4 pb-8 sm:px-6" aria-label={t("Advanced maintenance", "高级维护")}>
      <div className="border-t pt-6"><h2 className="flex items-center gap-2 text-base font-semibold"><Wrench className="size-4 text-brand" />{t("Advanced maintenance", "高级维护")}</h2><p className="mt-1 text-sm text-muted-foreground">{t("Diagnostics and legacy imports stay out of the everyday project switcher.", "诊断和旧版迁移不会出现在日常项目切换器中。")}</p></div>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader><CardTitle className="text-base">{t("Home doctor", "Home 检查")}</CardTitle><CardDescription>{t("Review deterministic metadata repairs before applying them.", "先查看可确定的元数据修复，再决定是否应用。")}</CardDescription></CardHeader>
          <CardContent className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" disabled={doctor.isPending} onClick={() => doctor.mutate({ repair: false })}>{doctor.isPending && <Loader2 className="animate-spin" />}{t("Check", "检查")}</Button>
              <Button size="sm" disabled={!report?.changed || report.applied || doctor.isPending} onClick={() => { if (window.confirm(t("Apply the listed deterministic repairs?", "应用列出的可确定修复吗？"))) doctor.mutate({ repair: true }); }}>{t("Apply repairs", "应用修复")}</Button>
            </div>
            {report && <div className="rounded-lg border p-3 text-sm"><p className="font-medium">{report.changed ? t("Repairs are available", "存在可应用修复") : t("No repair is needed", "无需修复")}</p>{report.issues.length > 0 && <ul className="mt-2 space-y-1 text-xs text-muted-foreground">{report.issues.map((issue, index) => <li key={`${issue.code}-${index}`}><code>{issue.code}</code> · {issue.detail}</li>)}</ul>}</div>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle className="text-base">{t("Legacy migration", "旧版迁移")}</CardTitle><CardDescription>{t("Preview an older cluster and keep an auditable receipt before applying it.", "先预览旧集群，确认后才迁移并保留可审计回执。")}</CardDescription></CardHeader>
          <CardContent className="space-y-3">
            <form className="flex gap-2" onSubmit={(event) => { event.preventDefault(); const value = legacyCluster.trim(); if (!value) return; setConfigPolicy(""); previewMigration.mutate(value, { onSuccess: (result) => { if (result.available) setReviewedLegacyCluster(value); } }); }}>
              <Input value={legacyCluster} onChange={(event) => setLegacyCluster(event.target.value)} placeholder={t("Legacy cluster path", "旧版项目集群路径")} className="font-mono text-sm" />
              <Button type="submit" variant="outline" disabled={!legacyCluster.trim() || previewMigration.isPending}>{previewMigration.isPending && <Loader2 className="animate-spin" />}{t("Preview", "预览")}</Button>
            </form>
            {plan && <div className="rounded-lg border p-3 text-xs"><p className="font-medium text-sm">{t("Preview ready", "迁移预览已就绪")}</p><p className="mt-1 text-muted-foreground">{plan.projects?.length ?? 0} {t("projects", "个项目")} · {plan.tasks?.length ?? 0} {t("tasks", "个任务")}</p>{requiresPrimaryPolicy && <Select value={configPolicy} onValueChange={setConfigPolicy}><SelectTrigger className="mt-2"><SelectValue placeholder={t("Choose config policy", "选择配置策略")} /></SelectTrigger><SelectContent><SelectItem value="primary">{t("Use imported configuration as primary", "将导入配置设为主配置")}</SelectItem></SelectContent></Select>}<Button className="mt-3" size="sm" disabled={!canApply || applyMigration.isPending} onClick={() => { if (plan && window.confirm(t("Apply this reviewed migration?", "应用此已审阅的迁移吗？"))) applyMigration.mutate({ legacyCluster: legacyCluster.trim(), expectedDigest: plan.reviewDigest, configPolicy: requiresPrimaryPolicy ? configPolicy : undefined }); }}>{applyMigration.isPending && <Loader2 className="animate-spin" />}{t("Apply migration", "应用迁移")}</Button></div>}
            {receiptHistory.length > 0 && <p className="text-xs text-muted-foreground">{t("Latest migration receipt", "最近迁移回执")}: <code>{receiptHistory[0].id}</code></p>}
          </CardContent>
        </Card>
      </div>
    </section>
  );
}
