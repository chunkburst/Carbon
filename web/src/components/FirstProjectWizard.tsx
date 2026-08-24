import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Check, FolderCog, FolderPlus, FolderSearch, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ProjectImageIconPicker, type ProjectImageIntent } from "@/components/ProjectImageIconPicker";
import { notifyCatalogIconAssetChanged } from "@/components/CatalogIcon";
import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import { useAddHomeProject, useCreateHomeCluster, useDeleteCarbonCatalogAsset, useEnsureCarbonHome, useUploadCarbonCatalogAsset } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { isTauri, pickFolder } from "@/lib/tauri";

type WizardStep = "home" | "cluster" | "project";

export function FirstProjectWizard({
  home,
  initialized,
  clusters,
  onComplete,
}: {
  home: string;
  initialized: boolean;
  clusters: CarbonHomeCluster[];
  projects?: CarbonHomeProject[];
  onComplete: (selection: { clusterId?: string; projectId: string }) => void;
}) {
  const { t } = useI18n();
  const ensureHome = useEnsureCarbonHome();
  const createCluster = useCreateHomeCluster(home);
  const addProject = useAddHomeProject(home);
  const uploadAsset = useUploadCarbonCatalogAsset(home);
  const deleteAsset = useDeleteCarbonCatalogAsset(home);
  const [step, setStep] = useState<WizardStep>(() => !initialized ? "home" : "project");
  const [newCluster, setNewCluster] = useState<CarbonHomeCluster | null>(null);
  const [clusterId, setClusterId] = useState("");
  const [clusterName, setClusterName] = useState("");
  const [clusterSlug, setClusterSlug] = useState("");
  const [projectName, setProjectName] = useState("");
  const [projectSlug, setProjectSlug] = useState("");
  const [sourcePath, setSourcePath] = useState("");
  const [imageIntent, setImageIntent] = useState<ProjectImageIntent>({ kind: "unchanged" });
  const [createdProject, setCreatedProject] = useState<{ project: CarbonHomeProject; clusterId?: string } | null>(null);
  const [groupOpen, setGroupOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const availableClusters = useMemo(() => {
    if (!newCluster || clusters.some((cluster) => cluster.id === newCluster.id)) return clusters;
    return [...clusters, newCluster];
  }, [clusters, newCluster]);

  useEffect(() => {
    if (!initialized) {
      setStep("home");
      return;
    }
    if (step === "home") setStep("project");
  }, [availableClusters.length, initialized, step]);

  const errorMessage = (cause: unknown) => cause instanceof Error ? cause.message : String(cause);

  const initializeHome = async () => {
    setError(null);
    try {
      const result = await ensureHome.mutateAsync(home);
      if (!result.available) {
        setError(t("This Carbon version cannot initialize a Main Data Home. Update Carbon and try again.", "当前 Carbon 版本暂不支持初始化主数据目录，请更新后重试。"));
      }
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const createFirstCluster = async () => {
    const name = clusterName.trim();
    if (!name) return;
    setError(null);
    try {
      const result = await createCluster.mutateAsync({
        name,
        slug: clusterSlug.trim() || undefined,
      });
      if (!result.available) {
        setError(t("This Carbon version cannot create clusters. Update Carbon and try again.", "当前 Carbon 版本暂不支持创建项目集群，请更新后重试。"));
        return;
      }
      setNewCluster(result.data);
      setClusterId(result.data.id);
      setStep("project");
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const chooseSourceFolder = async () => {
    const picked = await pickFolder();
    if (picked) setSourcePath(picked);
  };

  const createFirstProject = async () => {
    const name = projectName.trim();
    const path = sourcePath.trim();
    if (!name || !path) return;
    setError(null);
    try {
      const finish = async (target: { project: CarbonHomeProject; clusterId?: string }) => {
        if (imageIntent.kind === "upload") {
          const upload = await uploadAsset.mutateAsync({ target: "project", id: target.project.id, file: imageIntent.file });
          if (!upload.available) {
            throw new Error(t("Update Carbon before uploading a project image.", "请更新 Carbon 后再上传项目图片。"));
          }
          notifyCatalogIconAssetChanged();
        } else if (imageIntent.kind === "clear") {
          const remove = await deleteAsset.mutateAsync({ target: "project", id: target.project.id });
          if (!remove.available) {
            throw new Error(t("Update Carbon before removing a project image.", "请更新 Carbon 后再移除项目图片。"));
          }
          notifyCatalogIconAssetChanged();
        }
        onComplete({ clusterId: target.clusterId, projectId: target.project.id });
      };

      // The project create request is never retried after it succeeds. An image
      // failure retains the stable ID, so retrying only updates presentation.
      if (createdProject) {
        await finish(createdProject);
        return;
      }

      const result = await addProject.mutateAsync({
        clusterId: clusterId || undefined,
        name,
        slug: projectSlug.trim() || undefined,
        sourcePath: path,
      });
      if (!result.available) {
        setError(t("This Carbon version cannot add projects. Update Carbon and try again.", "当前 Carbon 版本暂不支持添加项目，请更新后重试。"));
        return;
      }
      const target = { project: result.data, clusterId: clusterId || undefined };
      setCreatedProject(target);
      try {
        await finish(target);
      } catch (cause) {
        setError(`${t("Project was created. Fix the image error and retry; another project will not be created.", "项目已创建。请修复图片错误后重试；不会重复创建项目。")} ${errorMessage(cause)}`);
        return;
      }
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const busy = ensureHome.isPending || createCluster.isPending || addProject.isPending || uploadAsset.isPending || deleteAsset.isPending;
  const stage = step === "home" ? 1 : step === "cluster" ? 2 : 3;

  return (
    <div className="grid h-full min-w-0 place-items-center bg-app p-5 sm:p-8">
      <Card className="w-full max-w-xl">
        <CardHeader className="gap-3">
          <div className="flex items-center justify-between gap-3">
            <div className="grid size-9 place-items-center rounded-lg bg-brand/10 text-brand">
              {step === "project" ? <FolderPlus className="size-5" /> : <FolderCog className="size-5" />}
            </div>
            <div className="flex items-center gap-1" aria-label={t("Setup progress", "初始化进度")}>
              {[1, 2, 3].map((item) => <span key={item} className={`h-1.5 w-7 rounded-full ${item <= stage ? "bg-brand" : "bg-muted"}`} />)}
            </div>
          </div>
          <div>
            <CardTitle>{t("Create your first project", "创建第一个项目")}</CardTitle>
            <CardDescription>
              {step === "home"
                ? t("Set up the local data space first.", "先准备本地数据空间。")
                : step === "cluster"
                  ? t("Group related projects under one shared goal.", "先为同一目标下的项目创建一个集群。")
                  : t("Link a real source folder, then open an empty task board.", "关联真实源码目录后，直接进入空白任务看板。")}
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent>
          {step === "home" && (
            <div className="space-y-4">
              <p className="rounded-lg border bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground break-all">{home}</p>
              <FieldError>{error}</FieldError>
              <Button className="w-full" disabled={busy} onClick={() => void initializeHome()}>
                {busy ? <Loader2 className="animate-spin" /> : <Check />}
                {t("Set up Main Data Home", "初始化主数据目录")}
              </Button>
            </div>
          )}

          {step === "cluster" && (
            <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void createFirstCluster(); }}>
              <FieldGroup className="gap-4">
                <Field data-invalid={!!error}>
                  <FieldLabel htmlFor="first-cluster-name">{t("Cluster name", "项目集群名称")}</FieldLabel>
                  <Input id="first-cluster-name" autoFocus value={clusterName} onChange={(event) => setClusterName(event.target.value)} placeholder={t("For example: Carbon desktop", "例如：Carbon 桌面端")} />
                </Field>
                <Field>
                  <FieldLabel htmlFor="first-cluster-slug">{t("English ID", "英文标识")}</FieldLabel>
                  <Input id="first-cluster-slug" value={clusterSlug} onChange={(event) => setClusterSlug(event.target.value)} placeholder={t("Optional, for example carbon-desktop", "可选，例如 carbon-desktop")} />
                </Field>
                <FieldError>{error}</FieldError>
              </FieldGroup>
              <Button className="w-full" type="submit" disabled={!clusterName.trim() || busy}>
                {busy && <Loader2 className="animate-spin" />}
                {t("Continue to project", "继续创建项目")}
              </Button>
            </form>
          )}

          {step === "project" && (
            <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void createFirstProject(); }}>
              <FieldGroup className="gap-4">
                <Collapsible open={groupOpen} onOpenChange={setGroupOpen} className="rounded-lg border bg-muted/15">
                  <CollapsibleTrigger asChild><Button type="button" variant="ghost" size="sm" className="w-full justify-start"><ArrowLeft className={`transition-transform ${groupOpen ? "rotate-90" : "rotate-180"}`} />{t("Add to a project group (advanced)", "添加到项目分组（高级）")}</Button></CollapsibleTrigger>
                  <CollapsibleContent>
                    <Field className="border-t px-3 py-3">
                      <FieldLabel htmlFor="first-project-cluster">{t("Project group", "项目分组")}</FieldLabel>
                      <Select value={clusterId || "standalone"} onValueChange={(value) => setClusterId(value === "standalone" ? "" : value)}>
                        <SelectTrigger id="first-project-cluster"><SelectValue /></SelectTrigger>
                        <SelectContent><SelectItem value="standalone">{t("Independent project (default)", "独立项目（默认）")}</SelectItem>{availableClusters.map((cluster) => <SelectItem key={cluster.id} value={cluster.id}>{cluster.name}</SelectItem>)}</SelectContent>
                      </Select>
                    </Field>
                    <div className="border-t px-3 py-2">
                      <Button type="button" variant="ghost" size="sm" onClick={() => { setError(null); setStep("cluster"); }}>
                        <FolderCog data-icon="inline-start" />
                        {t("Create a project group", "创建项目分组")}
                      </Button>
                    </div>
                  </CollapsibleContent>
                </Collapsible>
                <Field>
                  <FieldLabel htmlFor="first-project-name">{t("Project name", "项目名称")}</FieldLabel>
                  <Input id="first-project-name" autoFocus value={projectName} onChange={(event) => setProjectName(event.target.value)} placeholder={t("For example: Carbon for Windows", "例如：Carbon for Windows")} />
                </Field>
                <Field>
                  <FieldLabel htmlFor="first-project-slug">{t("English ID", "英文标识")}</FieldLabel>
                  <Input id="first-project-slug" value={projectSlug} onChange={(event) => setProjectSlug(event.target.value)} placeholder={t("Optional", "可选")} />
                </Field>
                <Field data-invalid={!!error}>
                  <FieldLabel htmlFor="first-project-source">{t("Source folder", "源码文件夹")}</FieldLabel>
                  <div className="flex gap-2">
                    <Input id="first-project-source" value={sourcePath} onChange={(event) => setSourcePath(event.target.value)} placeholder={t("A real project folder path", "填写真实项目文件夹路径")} className="font-mono text-sm" />
                    {isTauri() && <Button type="button" variant="outline" size="icon" aria-label={t("Choose source folder", "选择源码文件夹")} onClick={() => void chooseSourceFolder()}><FolderSearch /></Button>}
                  </div>
                  <FieldError>{error}</FieldError>
                </Field>
                <Field>
                  <FieldLabel>{t("Project image", "项目图片")}</FieldLabel>
                  <ProjectImageIconPicker
                    home={home}
                    projectId={createdProject?.project.id}
                    intent={imageIntent}
                    disabled={busy}
                    pending={uploadAsset.isPending || deleteAsset.isPending}
                    onIntentChange={setImageIntent}
                  />
                </Field>
              </FieldGroup>
              <div className="flex gap-2">
                <Button className="min-w-0 flex-1" type="submit" disabled={!projectName.trim() || !sourcePath.trim() || busy}>
                  {busy && <Loader2 className="animate-spin" />}
                  {createdProject ? t("Retry image and open", "重试图片并打开") : t("Open empty board", "进入空白看板")}
                </Button>
              </div>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
