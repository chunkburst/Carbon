import { useEffect, useMemo, useState } from "react";
import { Check, ChevronsUpDown, ListTree, Pencil, Plus, Save, ShieldCheck, Tags, UserRoundCog } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldTitle } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import type { CarbonScope, CarbonWorkerIdentity } from "@/lib/carbon-api";
import {
  useCarbonConfig,
  useCarbonTaskTypes,
  useCarbonWorkerIdentities,
  useCreateCarbonTaskType,
  useSaveCarbonConfig,
  useUpdateCarbonWorkerIdentity,
} from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, timeAgo } from "@/lib/utils";

const BUILT_IN_TYPES = ["foundation", "library", "patch", "extension", "plugin"] as const;

function clusterConfigScope(scope: CarbonScope): CarbonScope | undefined {
  if (!scope.home || !scope.clusterId) return undefined;
  return { home: scope.home, clusterId: scope.clusterId };
}

/** Resolve the physical task store without accidentally carrying a project into a cluster store. */
function storeConfigScope(scope: CarbonScope): CarbonScope | undefined {
  if (scope.home && scope.clusterId) return { home: scope.home, clusterId: scope.clusterId };
  if (scope.home && scope.projectId) return { home: scope.home, projectId: scope.projectId };
  if (scope.legacyPath) return { legacyPath: scope.legacyPath };
  return undefined;
}

export function CarbonManagementSettings({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const configScope = storeConfigScope(scope);
  const clusterScope = clusterConfigScope(scope);
  if (!configScope) {
    return (
      <Alert className="mt-4">
        <AlertTitle>{t("Project settings unavailable", "项目设置不可用")}</AlertTitle>
        <AlertDescription>{t("Open a Carbon project before changing its identity or type settings.", "请先打开 Carbon 项目，再修改身份或类型设置。")}</AlertDescription>
      </Alert>
    );
  }
  return (
    <section className="mt-4 grid gap-5 border-t pt-4">
      <CarbonIdentitySettings scope={configScope} />
      {clusterScope && <CarbonTrashRetention scope={clusterScope} />}
      <CarbonTypeManager scope={configScope} />
    </section>
  );
}

function CarbonIdentitySettings({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const config = useCarbonConfig(scope);
  const saveConfig = useSaveCarbonConfig(scope);
  const identities = useCarbonWorkerIdentities(scope);
  const updateIdentity = useUpdateCarbonWorkerIdentity(scope);
  const types = useCarbonTaskTypes(scope);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<CarbonWorkerIdentity | null>(null);
  const [actor, setActor] = useState("");
  const [role, setRole] = useState("");
  const [selectedTypes, setSelectedTypes] = useState<string[]>([]);
  const [reason, setReason] = useState("");

  const modeEnabled = identities.data?.available
    ? identities.data.data.modeEnabled
    : config.data?.available
      ? Boolean(config.data.data.identityMode)
      : false;
  const records = identities.data?.available ? identities.data.data.records ?? [] : [];
  const typeOptions = useMemo(() => {
    const custom = types.data?.available ? types.data.data.custom ?? [] : [];
    const registered = types.data?.available ? types.data.data.types ?? [] : [];
    const names = new Map(custom.map((item) => [item.key, item.display_name || item.key]));
    return [...new Set([...BUILT_IN_TYPES, ...registered])]
      .map((key) => ({ key, label: names.get(key) ?? carbonTaskTypeLabel(key, t) }))
      .sort((left, right) => left.label.localeCompare(right.label));
  }, [t, types.data]);

  const resetEditor = () => {
    setEditorOpen(false);
    setEditing(null);
    setActor("");
    setRole("");
    setSelectedTypes([]);
    setReason("");
  };
  const edit = (record?: CarbonWorkerIdentity) => {
    setEditing(record ?? null);
    setActor(record?.actor ?? "agent:");
    setRole(record?.role ?? "");
    setSelectedTypes(record?.types ?? []);
    setReason("");
    setEditorOpen(true);
  };
  const normalizedActor = actor.trim();
  const normalizedRole = role.trim();
  const normalizedTypes = [...new Set(selectedTypes)].sort();
  const originalTypes = [...(editing?.types ?? [])].sort();
  const changed = !editing
    || normalizedRole !== editing.role
    || normalizedTypes.join("\u0000") !== originalTypes.join("\u0000");
  const validActor = normalizedActor.startsWith("agent:") && !/\s/.test(normalizedActor.slice("agent:".length)) && normalizedActor.length > "agent:".length;
  const reasonRequired = Boolean(editing && changed);
  const canSave = modeEnabled
    && validActor
    && Boolean(normalizedRole)
    && normalizedTypes.length > 0
    && changed
    && (!reasonRequired || Boolean(reason.trim()))
    && !updateIdentity.isPending;

  const submit = () => {
    if (!canSave) return;
    updateIdentity.mutate({
      actor: normalizedActor,
      input: {
        role: normalizedRole,
        types: normalizedTypes,
        ...(reason.trim() ? { reason: reason.trim() } : {}),
      },
    }, { onSuccess: (result) => result.available && resetEditor() });
  };

  const unavailable = config.data?.available === false || identities.data?.available === false;
  return (
    <section className="grid gap-3" aria-labelledby="carbon-identity-settings-title">
      <FieldGroup className="gap-3">
        <Field orientation="horizontal" className="items-center rounded-xl border bg-muted/15 p-3">
          <FieldContent className="min-w-0">
            <FieldTitle id="carbon-identity-settings-title"><ShieldCheck className="size-4" />{t("Agent role mode", "智能体身份模式")}</FieldTitle>
            <FieldDescription>
              {t(
                "Off by default. When enabled, each agent chooses a stable role and the kinds of tasks it can take; Carbon checks that fit when work is taken over or handed off.",
                "默认关闭。启用后，每个智能体会设置稳定角色和可接任务类型；接手或转交任务时 Carbon 会检查是否匹配。",
              )}
            </FieldDescription>
          </FieldContent>
          <Switch
            checked={modeEnabled}
            disabled={unavailable || saveConfig.isPending || config.isLoading}
            onCheckedChange={(identityMode) => saveConfig.mutate({ identityMode })}
            aria-label={t("Enable agent role mode", "启用智能体身份模式")}
          />
        </Field>
      </FieldGroup>

      {unavailable && (
        <Alert>
          <AlertTitle>{t("Agent role mode needs an update", "智能体身份模式需要更新 Carbon")}</AlertTitle>
          <AlertDescription>{t("This Carbon installation does not provide the agent profile list yet.", "当前 Carbon 服务暂未提供智能体身份名单。")}</AlertDescription>
        </Alert>
      )}

      {!modeEnabled && !unavailable && (
        <div className="rounded-lg border border-dashed px-3 py-2 text-xs leading-relaxed text-muted-foreground">
          {t(
            "Free collaboration is active: existing agents and older Carbon clients can still take tasks without setting a role.",
            "当前为自由协作模式：已有智能体和旧版 Carbon 客户端无需设置身份，也能继续接手任务。",
          )}
        </div>
      )}

      {modeEnabled && (
        <div className="grid gap-3 rounded-xl border bg-panel p-3">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <p className="flex items-center gap-2 text-sm font-medium"><UserRoundCog className="size-4" />{t("Agent profiles", "已设置的智能体身份")}</p>
              <p className="text-xs text-muted-foreground">{t("Explain changes to an established profile so the team can understand the handoff.", "修改既有身份时请说明原因，方便团队了解分工变化。")}</p>
            </div>
            <Button size="sm" variant="outline" onClick={() => edit()}>
              <Plus data-icon="inline-start" />{t("Add agent profile", "新增智能体身份")}
            </Button>
          </div>

          {records.length === 0 ? (
            <div className="rounded-lg bg-muted/25 px-3 py-4 text-center text-xs text-muted-foreground">
              {t("No agent profile has been set for this project yet.", "这个项目还没有设置智能体身份。")}
            </div>
          ) : (
            <div className="grid gap-2">
              {records.map((record) => (
                <div key={record.actor} className="group flex flex-wrap items-center gap-2 rounded-lg border bg-background px-3 py-2 transition-colors duration-200 hover:bg-muted/25 motion-reduce:transition-none">
                  <WorkerIdentity actor={record.actor} compact />
                  <Badge variant="secondary">{record.role}</Badge>
                  <div className="flex min-w-0 flex-1 flex-wrap gap-1">
                    {record.types.map((type) => <Badge key={type} variant="outline">{typeOptions.find((item) => item.key === type)?.label ?? carbonTaskTypeLabel(type, t)}</Badge>)}
                  </div>
                  <span className="text-[11px] text-muted-foreground" title={`${record.changedBy} · ${record.updatedAt}`}>{timeAgo(record.updatedAt)}</span>
                  <Button size="icon-sm" variant="ghost" onClick={() => edit(record)} aria-label={t("Edit identity for {actor}", "编辑 {actor} 的身份", { actor: record.actor })}>
                    <Pencil />
                  </Button>
                  {record.reason && <p className="w-full truncate pl-0 text-[11px] text-muted-foreground sm:pl-8" title={record.reason}>{t("Latest change note", "最近修改说明")}：{record.reason}</p>}
                </div>
              ))}
            </div>
          )}

          {editorOpen && (
            <FieldGroup className="gap-3 rounded-lg border bg-muted/15 p-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <Field data-invalid={Boolean(actor && !validActor)}>
                  <FieldLabel htmlFor="worker-identity-actor">{t("Agent connection ID", "智能体连接标识")}</FieldLabel>
                  <Input id="worker-identity-actor" value={actor} disabled={Boolean(editing)} onChange={(event) => setActor(event.target.value)} placeholder="agent:frontend-1" />
                  <FieldDescription>{t("Use the same agent:… ID this agent uses when connecting to Carbon.", "请填写该智能体连接 Carbon 时使用的同一个 agent:… 标识。")}</FieldDescription>
                  {actor && !validActor && <FieldError>{t("Enter a connection ID such as agent:codex.", "请输入类似 agent:codex 的连接标识。")}</FieldError>}
                </Field>
                <Field>
                  <FieldLabel htmlFor="worker-identity-role">{t("Role", "身份角色")}</FieldLabel>
                  <Input id="worker-identity-role" value={role} maxLength={80} onChange={(event) => setRole(event.target.value)} placeholder={t("Architect, task publisher…", "架构师、任务发布者……")} />
                  <FieldDescription>{t("A stable human-readable responsibility, not a permission level.", "用于描述稳定职责，不代表系统权限等级。")}</FieldDescription>
                </Field>
              </div>
              <Field>
                <FieldLabel>{t("Task types this identity may claim", "此身份可认领的任务类型")}</FieldLabel>
                <IdentityTypePicker options={typeOptions} value={selectedTypes} onChange={setSelectedTypes} />
                  <FieldDescription>{t("Select more than one when this agent genuinely covers several disciplines.", "当该智能体确实横跨多个领域时，可以多选。")}</FieldDescription>
                {selectedTypes.length === 0 && <FieldError>{t("Select at least one task type.", "请至少选择一种任务类型。")}</FieldError>}
              </Field>
              {editing && (
                <Field data-invalid={reasonRequired && !reason.trim()}>
                  <FieldLabel htmlFor="worker-identity-reason">{t("Change reason", "变更原因")}</FieldLabel>
                  <Input id="worker-identity-reason" value={reason} maxLength={240} onChange={(event) => setReason(event.target.value)} placeholder={t("Why is this agent's profile changing?", "为什么要调整这个智能体的身份？")} />
                  {reasonRequired && !reason.trim() && <FieldError>{t("A reason is required when changing an existing identity.", "变更既有身份时必须填写原因。")}</FieldError>}
                </Field>
              )}
              <div className="flex justify-end gap-2">
                <Button size="sm" variant="ghost" disabled={updateIdentity.isPending} onClick={resetEditor}>{t("Cancel", "取消")}</Button>
                <Button size="sm" disabled={!canSave} onClick={submit}>{t("Save identity", "保存身份")}</Button>
              </div>
            </FieldGroup>
          )}
        </div>
      )}
    </section>
  );
}

function IdentityTypePicker({
  options,
  value,
  onChange,
}: {
  options: Array<{ key: string; label: string }>;
  value: string[];
  onChange: (types: string[]) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const selected = new Set(value);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" role="combobox" aria-expanded={open} className="h-auto min-h-9 w-full justify-between font-normal">
          <span className={cn("flex min-w-0 flex-wrap gap-1 text-left", value.length === 0 && "text-muted-foreground")}>
            {value.length === 0
              ? t("Search and select task types…", "搜索并选择任务类型……")
              : value.map((type) => <Badge key={type} variant="secondary">{options.find((item) => item.key === type)?.label ?? type}</Badge>)}
          </span>
          <ChevronsUpDown className="ml-2 size-4 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[--radix-popover-trigger-width] min-w-64 p-0">
        <Command>
          <CommandInput placeholder={t("Search task types…", "搜索任务类型……")} />
          <CommandList>
            <CommandEmpty>{t("No matching task type", "没有匹配的任务类型")}</CommandEmpty>
            <CommandGroup>
              {options.map((option) => (
                <CommandItem
                  key={option.key}
                  value={`${option.label} ${option.key}`}
                  onSelect={() => onChange(selected.has(option.key) ? value.filter((item) => item !== option.key) : [...value, option.key])}
                >
                  <Check className={cn("size-4", selected.has(option.key) ? "opacity-100" : "opacity-0")} />
                  <span className="flex-1">{option.label}</span>
                  <code className="text-[10px] text-muted-foreground">{option.key}</code>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function CarbonTrashRetention({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const config = useCarbonConfig(scope);
  const save = useSaveCarbonConfig(scope);
  const [days, setDays] = useState("30");

  useEffect(() => {
    if (config.data?.available) setDays(String(config.data.data.trashRetentionDays));
  }, [config.data]);

  const parsed = Number(days);
  const valid = Number.isInteger(parsed) && parsed >= 1 && parsed <= 3650;
  const current = config.data?.available ? config.data.data.trashRetentionDays : undefined;

  return (
    <div className="grid gap-2">
      <div><p className="flex items-center gap-2 text-sm font-medium"><Save className="size-4" />{t("Trash retention", "垃圾站保留期限")}</p><p className="text-xs text-muted-foreground">{t("Expired-item cleanup is evaluated only when a new task enters Trash.", "仅在新任务进入垃圾站时触发过期回收检查。")}</p></div>
      {config.data?.available === false ? <p className="text-xs text-muted-foreground">{t("Carbon settings are not available in this installation.", "当前 Carbon 服务暂未提供设置功能。")}</p> : <div className="flex items-center gap-2"><Input type="number" min={1} max={3650} value={days} onChange={(event) => setDays(event.target.value)} className="h-8 w-24" aria-label={t("Retention days", "保留天数")} /><span className="text-sm text-muted-foreground">{t("days", "天")}</span><Button size="sm" variant="outline" disabled={!valid || save.isPending || parsed === current} onClick={() => save.mutate({ trashRetentionDays: parsed })}>{t("Save", "保存")}</Button></div>}
      {!valid && days !== "" && <p className="text-xs text-destructive">{t("Enter a whole number from 1 to 3650.", "请输入 1 到 3650 之间的整数。")}</p>}
    </div>
  );
}

function CarbonTypeManager({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const types = useCarbonTaskTypes(scope);
  const create = useCreateCarbonTaskType(scope);
  const [key, setKey] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const custom = types.data?.available ? types.data.data.custom ?? [] : [];
  const allTypes = useMemo(() => {
    const registered = types.data?.available ? types.data.data.types ?? [] : [];
    return new Set([...BUILT_IN_TYPES, ...registered]);
  }, [types.data]);
  const normalizedKey = key.trim().toLowerCase();
  const duplicate = Boolean(normalizedKey && allTypes.has(normalizedKey));
  const canCreate = Boolean(normalizedKey && !duplicate && confirmed && !create.isPending && types.data?.available);

  return (
    <div className="grid gap-3">
      <div><p className="flex items-center gap-2 text-sm font-medium"><Tags className="size-4" />{t("Task type catalog", "任务类型目录")}</p><p className="text-xs text-muted-foreground">{t("Prefer the standard types. Add a custom type only when it will be reused across this project store.", "优先使用标准类型；仅在会在此项目存储中重复使用时添加自定义类型。")}</p></div>
      {types.data?.available === false ? <p className="text-xs text-muted-foreground">{t("Task types are not available in this Carbon installation.", "当前 Carbon 服务暂未提供任务类型目录。")}</p> : <>
        <div className="flex flex-wrap gap-1.5">
          {BUILT_IN_TYPES.map((type) => (
            <Badge key={type} variant="secondary" title={type}>
              {carbonTaskTypeLabel(type, t)}
            </Badge>
          ))}
        </div>
        {custom.length > 0 && <div className="grid gap-1"><p className="text-xs font-medium text-muted-foreground">{t("Custom types", "自定义类型")}</p>{custom.map((type) => <div key={type.key} className="flex items-center justify-between rounded border px-2 py-1.5 text-sm"><span>{type.display_name || type.key}</span><code className="text-xs text-muted-foreground">{type.key}</code></div>)}</div>}
        <p className="flex items-start gap-2 text-xs text-muted-foreground"><ListTree className="mt-0.5 size-3.5 shrink-0" />{t("Built-in types stay available for every project. Removing custom types is not supported here yet.", "内置类型会一直保留；目前暂不支持在这里删除自定义类型。")}</p>
        <div className="grid gap-2 rounded-lg border p-3 sm:grid-cols-2"><Input value={key} onChange={(event) => setKey(event.target.value)} placeholder={t("Reusable type key", "可复用类型键")} /><Input value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("Display name (optional)", "显示名称（可选）")} /><label className="flex items-center gap-2 text-xs sm:col-span-2"><Checkbox checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />{t("I confirm this is a reusable store-wide type, not a one-off task label.", "我确认这是可复用的项目存储级类型，而不是一次性任务标签。")}</label><Button className="w-fit sm:col-span-2" size="sm" disabled={!canCreate} onClick={() => create.mutate({ key: normalizedKey, displayName: displayName.trim() || undefined }, { onSuccess: (result) => { if (result.available) { setKey(""); setDisplayName(""); setConfirmed(false); } } })}>{t("Add custom type", "添加自定义类型")}</Button></div>
        {duplicate && <p className="text-xs text-destructive">{t("That type already exists; use the existing catalog entry.", "该类型已存在；请使用现有目录条目。")}</p>}
      </>}
    </div>
  );
}
