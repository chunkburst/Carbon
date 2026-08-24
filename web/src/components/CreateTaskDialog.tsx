import { useEffect, useState, type FormEvent } from "react";
import { BookmarkPlus, FileStack, Loader2, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { MarkdownEditor } from "@/components/MarkdownEditor";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { PriorityIcon, PRIORITIES, priorityLabel } from "@/components/PriorityIcon";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { hasCarbonFeature, type CarbonImportance, type CarbonScopeInput, type CarbonTaskType } from "@/lib/carbon-api";
import { useCarbonCapabilities, useCarbonTaskTypes, useCarbonTemplates, useCreateCarbonTask, useCreateCarbonTaskType, useCreateCarbonTemplate, useCreateTask, useInstantiateCarbonTemplate } from "@/lib/queries";
import type { Check } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { loadLocalTemplates, saveLocalTemplate, type LocalTaskTemplate } from "@/lib/templates";
import { CARBON_IMPORTANCE, CARBON_TASK_TYPES, carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";

export function CreateTaskDialog({
  path,
  open,
  onOpenChange,
  defaultParent,
  carbonScope,
  forceCarbon = false,
  defaultProjectId,
}: {
  path: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultParent?: string;
  carbonScope?: CarbonScopeInput;
  forceCarbon?: boolean;
  defaultProjectId?: string;
}) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [deps, setDeps] = useState("");
  const [checks, setChecks] = useState<Check[]>([]);
  const [priority, setPriority] = useState("");
  const [labels, setLabels] = useState("");
  const [projectId, setProjectId] = useState(defaultProjectId ?? "");
  const [clusterWide, setClusterWide] = useState(false);
  const [taskType, setTaskType] = useState("");
  const [importance, setImportance] = useState("");
  const [templates, setTemplates] = useState<LocalTaskTemplate[]>(() => loadLocalTemplates(path));
  const create = useCreateTask(path);
  const createCarbon = useCreateCarbonTask(path, carbonScope ?? path);
  const templateScope = carbonScope ?? path;
  // Cluster-wide writes must use an explicit cluster-only scope. Keeping this
  // separate from the project-bound scope prevents an omitted projectId from
  // being silently rebound to the current project by the server.
  const clusterScope: CarbonScopeInput | undefined = typeof carbonScope === "object" && carbonScope.clusterId
    ? { home: carbonScope.home, clusterId: carbonScope.clusterId }
    : undefined;
  const createCarbonCluster = useCreateCarbonTask(path, clusterScope ?? carbonScope ?? path);
  // A standalone project owns its own task-type/template configuration.  Preserve
  // its project ID rather than manufacturing a cluster-only scope.
  const typeScope: CarbonScopeInput = typeof templateScope === "string"
    ? templateScope
    : templateScope.clusterId
      ? { home: templateScope.home, clusterId: templateScope.clusterId }
      : templateScope;
  const templateResult = useCarbonTemplates(templateScope);
  const taskTypesResult = useCarbonTaskTypes(typeScope);
  const createTemplate = useCreateCarbonTemplate(templateScope);
  const createTemplateCluster = useCreateCarbonTemplate(clusterScope ?? templateScope);
  const createTaskType = useCreateCarbonTaskType(typeScope);
  const instantiateTemplate = useInstantiateCarbonTemplate(path, templateScope);
  const instantiateTemplateCluster = useInstantiateCarbonTemplate(path, clusterScope ?? templateScope);
  const { data: capabilityResult } = useCarbonCapabilities(carbonScope ?? path);
  const { t } = useI18n();
  const carbonAvailable = forceCarbon || (capabilityResult?.available && hasCarbonFeature(capabilityResult.data, "task_fields"));
  const clusterWideAvailable = Boolean(clusterScope);
  const advancedRequested = forceCarbon || clusterWide || !!(projectId.trim() || taskType.trim() || importance);
  const serverTemplatesAvailable = templateResult.data?.available === true;
  const localTemplatesFallback = templateResult.data?.available === false;
  const remoteTemplates = serverTemplatesAvailable ? templateResult.data?.data?.templates ?? [] : [];
  const registeredTaskTypes = taskTypesResult.data?.available ? taskTypesResult.data.data.types ?? [] : [];
  const taskTypeOptions = [...new Set([...CARBON_TASK_TYPES, ...registeredTaskTypes])];
  const isSubmitting = create.isPending || createCarbon.isPending || createCarbonCluster.isPending || instantiateTemplate.isPending || instantiateTemplateCluster.isPending;
  const submitDisabled = !title.trim() || isSubmitting || (advancedRequested && !carbonAvailable);

  useEffect(() => {
    if (!open) return;
    setClusterWide(false);
    setProjectId(defaultProjectId ?? "");
  }, [defaultProjectId, open]);

  useEffect(() => {
    setTemplates(loadLocalTemplates(path));
  }, [path]);

  function reset() {
    setTitle("");
    setBody("");
    setDeps("");
    setChecks([]);
    setPriority("");
    setLabels("");
    setClusterWide(false);
    setProjectId(defaultProjectId ?? "");
    setTaskType("");
    setImportance("");
  }

  function applyTemplate(template: LocalTaskTemplate) {
    setTitle(template.title ?? "");
    setBody(template.body ?? "");
    setDeps(template.deps ?? "");
    setChecks(template.checks ? template.checks.map((check) => ({ ...check })) : []);
    setPriority(template.priority ?? "");
    setLabels(template.labels ?? "");
    setClusterWide(false);
    setProjectId(template.projectId ?? "");
    setTaskType(template.type ?? "");
    setImportance(template.importance ?? "");
  }

  function saveTemplate() {
    const name = window.prompt(serverTemplatesAvailable ? t("Save shared template as", "保存共享模板为") : t("Save local template as", "保存本地模板为"));
    if (!name?.trim()) return;
    if (serverTemplatesAvailable) {
      const templateInput = {
        name: name.trim(),
        title: title.trim(),
        body: body.trim() || undefined,
        ...(clusterWide
          ? { cluster_wide: true }
          : { project_id: projectId.trim() || defaultProjectId || undefined, cluster_wide: false }),
        type: taskType.trim() || "foundation",
        importance: importance || "normal",
        priority: priority || undefined,
        labels: labels.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean),
        deps: deps.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean),
        checks: checks.filter((check) => check.desc.trim()),
        parent: defaultParent || undefined,
      };
      (clusterWide && clusterScope ? createTemplateCluster : createTemplate).mutate(templateInput);
      return;
    }
    if (!localTemplatesFallback) return;
    setTemplates(
      saveLocalTemplate(path, {
        name: name.trim(),
        title,
        body,
        deps,
        checks,
        priority,
        labels,
        projectId,
        type: taskType as CarbonTaskType,
        importance: importance as CarbonImportance,
      }),
    );
  }

  function instantiateRemoteTemplate(id: string, expectedVersion?: string, templateClusterWide = false) {
    const shared = clusterWide || templateClusterWide;
    const mutation = shared && clusterScope ? instantiateTemplateCluster : instantiateTemplate;
    const input = shared
      ? { expectedVersion, projectId: "" }
      : { expectedVersion, projectId: defaultProjectId };
    mutation.mutate(
      { id, input },
      {
        onSuccess: (result) => {
          if (!result.available) return;
          reset();
          onOpenChange(false);
        },
      },
    );
  }

  function registerTaskType() {
    const key = taskType.trim().toLowerCase();
    if (!key || !taskTypesResult.data?.available || taskTypeOptions.includes(key)) return;
    if (!window.confirm(t("Create reusable cluster type “{key}”? Do not use custom types for one-off tasks.", "创建可复用的集群类型“{key}”？请勿将自定义类型用于一次性任务。", { key }))) return;
    const displayName = window.prompt(t("Display name for {key}", "为 {key} 设置显示名称", { key })) ?? "";
    createTaskType.mutate({ key, displayName: displayName.trim() || undefined });
  }

  function submit() {
    const parsedDeps = deps
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    const parsedLabels = labels
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    const parsedChecks = checks.filter((c) => c.desc.trim());
    const input = {
      title: title.trim(),
      body: body.trim() ? body.trim() + "\n" : undefined,
      deps: parsedDeps.length ? parsedDeps : undefined,
      checks: parsedChecks.length ? parsedChecks : undefined,
      labels: parsedLabels.length ? parsedLabels : undefined,
      priority: priority || undefined,
      parent: defaultParent || undefined,
    };
    const onSuccess = () => {
      reset();
      onOpenChange(false);
    };
    if (advancedRequested) {
      if (!carbonAvailable) return;
      const carbonInput = {
        ...input,
        // An explicit empty projectId is the Carbon wire-level marker for a
        // cluster-wide create. It must travel with the cluster-only scope;
        // omitting the key would be treated as an unbound/defaulting request.
        ...(clusterWide ? { projectId: "" } : { projectId: projectId.trim() || defaultProjectId || undefined }),
        type: taskType.trim() || (forceCarbon ? "foundation" : undefined),
        importance: importance || (forceCarbon ? "normal" : undefined),
      };
      (clusterWide && clusterScope ? createCarbonCluster : createCarbon).mutate(
        carbonInput,
        { onSuccess: (result) => result.available && onSuccess() },
      );
      return;
    }
    create.mutate(input, { onSuccess });
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!submitDisabled) submit();
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="!flex max-h-[90dvh] min-h-0 flex-col !gap-0 !overflow-hidden !p-0 sm:max-w-lg"
        data-testid="create-task-dialog"
      >
        <DialogHeader className="shrink-0 border-b bg-popover px-6 pb-4 pt-6 pr-14" data-testid="create-task-dialog-header">
          <DialogTitle id="create-task-dialog-title">{defaultParent ? t("New sub-task", "新建子任务") : t("New task", "新建任务")}</DialogTitle>
          <DialogDescription>
            {defaultParent ? (
              t(
                "Sub-task of {parent}. The engine assigns the id and initial status.",
                "“{parent}”的子任务。系统会分配任务 ID 和初始状态。",
                { parent: defaultParent },
              )
            ) : (
              t("The engine assigns the id and initial status.", "系统会分配任务 ID 和初始状态。")
            )}
          </DialogDescription>
        </DialogHeader>

        <form
          aria-labelledby="create-task-dialog-title"
          className="flex min-h-0 flex-1 flex-col"
          data-slot="create-task-form"
          data-testid="create-task-form"
          onSubmit={handleSubmit}
        >
          <div
            aria-labelledby="create-task-dialog-title"
            className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-4"
            data-slot="create-task-dialog-scroll-body"
            data-testid="create-task-dialog-scroll-body"
          >
            <div className="flex flex-col gap-4 pb-2">
        <div className="flex items-center justify-between gap-3">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button type="button" variant="outline" size="sm">
                <FileStack data-icon="inline-start" />
                {t("Templates", "模板")}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-64">
              <DropdownMenuLabel>{serverTemplatesAvailable ? t("Shared templates", "共享模板") : localTemplatesFallback ? t("Local fallback templates", "仅本机的兼容模板") : t("Loading templates", "正在加载模板")}</DropdownMenuLabel>
              <DropdownMenuGroup>
                {serverTemplatesAvailable && (remoteTemplates.length ? remoteTemplates.map((template) => (
                  <DropdownMenuItem key={template.id} disabled={instantiateTemplate.isPending || instantiateTemplateCluster.isPending} onSelect={() => instantiateRemoteTemplate(template.id, template.version, template.cluster_wide === true)}>
                    <span className="truncate">{template.name}</span>
                  </DropdownMenuItem>
                )) : <DropdownMenuItem disabled>{t("No shared templates", "暂无共享模板")}</DropdownMenuItem>)}
                {localTemplatesFallback && (templates.length ? templates.map((template) => (
                  <DropdownMenuItem key={template.id} onSelect={() => applyTemplate(template)}>
                    <span className="truncate">{template.name}</span>
                  </DropdownMenuItem>
                )) : <DropdownMenuItem disabled>{t("No local templates", "暂无本地模板")}</DropdownMenuItem>)}
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuItem disabled={!serverTemplatesAvailable && !localTemplatesFallback || createTemplate.isPending || createTemplateCluster.isPending} onSelect={saveTemplate}>
                <BookmarkPlus data-icon="inline-start" />
                {serverTemplatesAvailable ? t("Save current fields as shared", "将当前字段保存为共享模板") : t("Save current fields locally", "将当前字段保存到本地")}
              </DropdownMenuItem>
              {localTemplatesFallback && <p className="px-2 pb-1 text-[11px] text-muted-foreground">{t("Server templates returned 404; this fallback is stored only on this device.", "服务端 templates 返回 404；兼容模板仅保存在本机。")}</p>}
            </DropdownMenuContent>
          </DropdownMenu>
          <span className="text-xs text-muted-foreground">{serverTemplatesAvailable ? t("Shared in Carbon", "已共享到 Carbon") : localTemplatesFallback ? t("Saved on this device", "保存在此设备") : ""}</span>
        </div>

        {!carbonAvailable && (
          <Alert>
            <AlertTitle>{t("Some task fields are not available yet", "部分任务属性暂不可用")}</AlertTitle>
            <AlertDescription>
              {t(
                "You can still create the task. Update Carbon to set its project, type, and importance here; a worker can take it over after creation.",
                "任务仍可正常创建。更新 Carbon 后即可在这里设置项目、类型和重要性；创建后可由智能体接手。",
              )}
            </AlertDescription>
          </Alert>
        )}

        <FieldGroup className="gap-4" data-testid="create-task-dialog-fields">
          <div className="grid gap-1.5">
            <Label htmlFor="ct-title">{t("Title", "标题")}</Label>
            <Input
              id="ct-title"
              autoFocus
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={t("Add idempotency keys to the webhook", "为 Webhook 添加幂等键")}
            />
          </div>

          <div className="grid gap-1.5">
            <Label>{t("Description", "描述")}</Label>
            <MarkdownEditor
              value={body}
              onChange={setBody}
              placeholder={t("Intent and constraints…", "目标与约束…")}
              minHeight="8rem"
            />
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label>{t("Priority", "优先级")}</Label>
              <Select value={priority || "none"} onValueChange={(v) => setPriority(v === "none" ? "" : v)}>
                <SelectTrigger className="h-8 w-full">
                  <span className="flex items-center gap-2">
                    <PriorityIcon priority={priority} />
                    {priorityLabel(priority)}
                  </span>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">
                    <span className="flex items-center gap-2">
                      <PriorityIcon /> {t("No priority", "无优先级")}
                    </span>
                  </SelectItem>
                  {PRIORITIES.map((p) => (
                    <SelectItem key={p} value={p}>
                      <span className="flex items-center gap-2">
                        <PriorityIcon priority={p} /> {priorityLabel(p)}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ct-labels">{t("Labels", "标签")}</Label>
              <Input
                id="ct-labels"
                value={labels}
                onChange={(e) => setLabels(e.target.value)}
                placeholder="backend, db"
                className="h-8 text-sm"
              />
            </div>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {clusterWideAvailable && (
              <div className="rounded-lg border p-3 sm:col-span-2">
                <div className="flex items-start justify-between gap-3">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="ct-cluster-wide">{t("Cluster-wide shared task", "集群共享任务")}</Label>
                    <p className="text-xs text-muted-foreground">
                      {t("Shared tasks belong to this cluster's task pool and are visible across all of its projects.", "共享任务属于此集群任务池，对其中所有项目可见。")}
                    </p>
                  </div>
                  <Switch
                    id="ct-cluster-wide"
                    checked={clusterWide}
                    disabled={!carbonAvailable}
                    onCheckedChange={(enabled) => {
                      setClusterWide(enabled);
                      setProjectId(enabled ? "" : defaultProjectId ?? "");
                    }}
                  />
                </div>
              </div>
            )}
            <Field>
              <FieldLabel htmlFor="ct-project-id">{t("Project ID", "项目 ID")}</FieldLabel>
              <Input
                id="ct-project-id"
                value={projectId}
                onChange={(event) => setProjectId(event.target.value)}
                placeholder="platform-web"
                disabled={!carbonAvailable || clusterWide}
              />
              <FieldDescription>
                {clusterWide
                  ? t("Shared task: an explicit empty projectId targets the cluster pool.", "共享任务使用明确为空的 projectId，目标为集群任务池。")
                  : t("Writes projectId through the Carbon API.", "通过 Carbon API 写入 projectId。")}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="ct-type">{t("Type", "类型")}</FieldLabel>
              <Input
                id="ct-type"
                list="carbon-task-types"
                value={taskType}
                onChange={(event) => setTaskType(event.target.value)}
                placeholder="foundation"
                disabled={!carbonAvailable}
              />
              <datalist id="carbon-task-types">
                {taskTypeOptions.map((value) => (
                  <option key={value} value={value}>{carbonTaskTypeLabel(value, t)}</option>
                ))}
              </datalist>
              {carbonAvailable && taskType.trim() && !taskTypeOptions.includes(taskType.trim()) && taskTypesResult.data?.available && (
                <Button type="button" variant="link" size="sm" className="h-auto w-fit px-0 text-xs" disabled={createTaskType.isPending} onClick={registerTaskType}>
                  {t("Register custom type “{type}”", "注册自定义类型“{type}”", { type: taskType.trim() })}
                </Button>
              )}
            </Field>
            <Field>
              <FieldLabel>{t("Importance", "重要性")}</FieldLabel>
              <Select
                value={importance || "none"}
                onValueChange={(value) => setImportance(value === "none" ? "" : value)}
                disabled={!carbonAvailable}
              >
                <SelectTrigger className="w-full">
                  {importance ? carbonImportanceLabel(importance, t) : t("No importance", "无重要性")}
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("No importance", "无重要性")}</SelectItem>
                  {CARBON_IMPORTANCE.map((value) => (
                    <SelectItem key={value} value={value}>{carbonImportanceLabel(value, t)}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>

          <div className="grid gap-1.5">
              <Label htmlFor="ct-deps">{t("Dependencies", "依赖")}</Label>
            <Input
              id="ct-deps"
              value={deps}
              onChange={(e) => setDeps(e.target.value)}
              placeholder="PROJ-001, PROJ-002"
              className="font-mono text-sm"
            />
            <p className="text-xs text-muted-foreground">{t("Must be closed before this can start.", "必须先关闭这些任务才能开始。")}</p>
          </div>

          <div className="grid gap-2">
            <div className="flex items-center justify-between">
              <Label>{t("Checks", "检查")}</Label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setChecks((cs) => [...cs, { desc: "", cmd: "", result: "pending" }])}
              >
                <Plus /> {t("Add check", "添加检查")}
              </Button>
            </div>
            {checks.map((c, i) => (
              <div key={i} className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <Input
                  value={c.desc}
                  onChange={(e) =>
                    setChecks((cs) => cs.map((x, j) => (j === i ? { ...x, desc: e.target.value } : x)))
                  }
                  placeholder={t("what it verifies", "验证的内容")}
                  className="w-full flex-1"
                />
                <Input
                  value={c.cmd ?? ""}
                  onChange={(e) =>
                    setChecks((cs) => cs.map((x, j) => (j === i ? { ...x, cmd: e.target.value } : x)))
                  }
                  placeholder={t("cmd (blank = manual)", "命令（留空即手动）")}
                  className="w-full flex-1 font-mono text-xs"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="self-end sm:self-auto"
                  aria-label={t("Remove check", "移除检查")}
                  onClick={() => setChecks((cs) => cs.filter((_, j) => j !== i))}
                >
                  <X />
                </Button>
              </div>
            ))}
          </div>
        </FieldGroup>
            </div>
          </div>

          <DialogFooter className="shrink-0 border-t bg-popover px-6 py-4" data-testid="create-task-dialog-footer">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
            {t("Cancel", "取消")}
          </Button>
          <Button
            type="submit"
            disabled={submitDisabled}
          >
            {isSubmitting && <Loader2 className="animate-spin" />}
            {t("Create task", "创建任务")}
          </Button>
        </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
