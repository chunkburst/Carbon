import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronsUpDown, ClockAlert, EyeOff, History, ListTree, Pencil, Plus, Save, ShieldCheck, Tags, UserRoundCog } from "lucide-react";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { WorkerIdentity } from "@/components/WorkerIdentity";
import type { CarbonScope, CarbonWorkerIdentity } from "@/lib/carbon-api";
import {
  useCarbonConfig,
  useCarbonTaskTypes,
  useCarbonWorkerIdentityAudit,
  useCarbonWorkerIdentities,
  useCreateCarbonTaskType,
  useSaveCarbonConfig,
  useUpdateCarbonWorkerIdentity,
} from "@/lib/queries";
import { displayName } from "@/lib/identity";
import { useI18n } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, timeAgo } from "@/lib/utils";
import { CARBON_BUILT_IN_ROLES, workerRoleLabel } from "@/lib/worker-roles";

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

function projectIdentityScope(scope: CarbonScope): CarbonScope | undefined {
  if (!scope.home || !scope.projectId) return undefined;
  return { home: scope.home, clusterId: scope.clusterId, projectId: scope.projectId };
}

export function CarbonManagementSettings({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const configScope = storeConfigScope(scope);
  const clusterScope = clusterConfigScope(scope);
  const identityScope = projectIdentityScope(scope);
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
      <CarbonTaskStagnationSettings scope={configScope} />
      {identityScope && <CarbonIdentitySettings scope={identityScope} />}
      {clusterScope && <CarbonTrashRetention scope={clusterScope} />}
      <CarbonTypeManager scope={configScope} />
    </section>
  );
}

type StagnationUnit = "minutes" | "hours" | "days";

function stagnationInput(seconds: number): { amount: string; unit: StagnationUnit } {
  if (seconds > 0 && seconds % 86_400 === 0) return { amount: String(seconds / 86_400), unit: "days" };
  if (seconds > 0 && seconds % 3_600 === 0) return { amount: String(seconds / 3_600), unit: "hours" };
  return { amount: String(Math.max(1, Math.round(seconds / 60))), unit: "minutes" };
}

function CarbonTaskStagnationSettings({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const config = useCarbonConfig(scope);
  const save = useSaveCarbonConfig(scope);
  const [amount, setAmount] = useState("24");
  const [unit, setUnit] = useState<StagnationUnit>("hours");

  useEffect(() => {
    if (!config.data?.available) return;
    const next = stagnationInput(config.data.data.taskStagnationAfterSeconds || 86_400);
    setAmount(next.amount);
    setUnit(next.unit);
  }, [config.data]);

  const multiplier = unit === "days" ? 86_400 : unit === "hours" ? 3_600 : 60;
  const parsed = Number(amount);
  const seconds = Math.round(parsed * multiplier);
  const valid = Number.isFinite(parsed) && parsed > 0 && seconds >= 60 && seconds <= 31_536_000;
  const current = config.data?.available ? config.data.data.taskStagnationAfterSeconds : undefined;
  const unitLabel = unit === "days" ? t("days", "天") : unit === "hours" ? t("hours", "小时") : t("minutes", "分钟");

  return (
    <div className="grid gap-3">
      <div>
        <p className="flex items-center gap-2 text-sm font-medium"><ClockAlert className="size-4" />{t("Task stagnation", "任务停滞周期")}</p>
        <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
          {t(
            "An open task is marked stagnant after this long without a meaningful action. Its workflow status does not change; reads, polling, heartbeats, and automatic renewals do not reset the timer.",
            "开放任务超过这个周期没有有效动作后，会附加“停滞”标记；原任务状态不会改变。读取、轮询、心跳和自动续期不会重置计时。",
          )}
        </p>
      </div>
      {config.data?.available === false ? (
        <p className="text-xs text-muted-foreground">{t("Task stagnation settings are unavailable in this installation.", "当前 Carbon 服务暂不支持停滞周期设置。")}</p>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="number"
            min={unit === "minutes" ? 1 : 0.02}
            step={unit === "minutes" ? 1 : 0.5}
            value={amount}
            onChange={(event) => setAmount(event.target.value)}
            className="h-8 w-24"
            aria-label={t("Stagnation period", "停滞周期")}
          />
          <Select value={unit} onValueChange={(value) => setUnit(value as StagnationUnit)}>
            <SelectTrigger size="sm" className="w-24" aria-label={t("Time unit", "时间单位")}><SelectValue>{unitLabel}</SelectValue></SelectTrigger>
            <SelectContent>
              <SelectItem value="minutes">{t("Minutes", "分钟")}</SelectItem>
              <SelectItem value="hours">{t("Hours", "小时")}</SelectItem>
              <SelectItem value="days">{t("Days", "天")}</SelectItem>
            </SelectContent>
          </Select>
          <Button size="sm" variant="outline" disabled={!valid || save.isPending || seconds === current} onClick={() => save.mutate({ taskStagnationAfterSeconds: seconds })}>
            {t("Save", "保存")}
          </Button>
          <Button size="sm" variant="ghost" disabled={save.isPending || current === 86_400} onClick={() => save.mutate({ taskStagnationAfterSeconds: 0 })}>
            {t("Use 24h default", "恢复 24 小时")}
          </Button>
        </div>
      )}
      {!valid && amount !== "" && <p className="text-xs text-destructive">{t("Choose a period from 1 minute to 365 days.", "请输入 1 分钟到 365 天之间的周期。")}</p>}
    </div>
  );
}

export function CarbonIdentitySettings({ scope, initialActor }: { scope: CarbonScope; initialActor?: string }) {
  const { t } = useI18n();
  const config = useCarbonConfig(scope);
  const saveConfig = useSaveCarbonConfig(scope);
  const identities = useCarbonWorkerIdentities(scope);
  const audit = useCarbonWorkerIdentityAudit(scope);
  const updateIdentity = useUpdateCarbonWorkerIdentity(scope);
  const types = useCarbonTaskTypes(scope);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<CarbonWorkerIdentity | null>(null);
  const [actor, setActor] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [selectedTypes, setSelectedTypes] = useState<string[]>([]);
  const [reason, setReason] = useState("");
  const [saveOutcome, setSaveOutcome] = useState<"traced" | "no_trace" | null>(null);
  const hydratedActor = useRef<string | undefined>(undefined);

  const modeEnabled = identities.data?.available
    ? identities.data.data.modeEnabled
    : config.data?.available
      ? Boolean(config.data.data.identityMode)
      : false;
  const records = useMemo(
    () => (identities.data?.available ? identities.data.data.records ?? [] : []),
    [identities.data],
  );
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
    setRoles([]);
    setSelectedTypes([]);
    setReason("");
  };
  const edit = (record?: CarbonWorkerIdentity, actorHint?: string) => {
    setSaveOutcome(null);
    setEditing(record ?? null);
    setActor(record?.actor ?? actorHint ?? "agent:");
    setRoles(record?.roles?.length ? record.roles : record?.role ? [record.role] : []);
    setSelectedTypes(record?.types ?? []);
    setReason("");
    setEditorOpen(true);
  };
  useEffect(() => {
    if (!initialActor || hydratedActor.current === initialActor) return;
    const record = records.find((item) => item.actor === initialActor);
    if (!identities.isLoading) {
      hydratedActor.current = initialActor;
      edit(record, initialActor);
    }
  }, [identities.isLoading, initialActor, records]);
  const normalizedActor = actor.trim();
  const normalizedRoles = [...new Set(roles.map((role) => role.trim()).filter(Boolean))].sort();
  const normalizedTypes = [...new Set(selectedTypes)].sort();
  const originalTypes = [...(editing?.types ?? [])].sort();
  const changed = !editing
    || normalizedRoles.join("\u0000") !== [...(editing.roles?.length ? editing.roles : [editing.role])].sort().join("\u0000")
    || normalizedTypes.join("\u0000") !== originalTypes.join("\u0000");
  const validActor = normalizedActor.startsWith("agent:") && !/\s/.test(normalizedActor.slice("agent:".length)) && normalizedActor.length > "agent:".length;
  const reasonRequired = Boolean(editing && changed);
  const canSave = validActor
    && normalizedRoles.length > 0
    && normalizedTypes.length > 0
    && changed
    && (!reasonRequired || Boolean(reason.trim()))
    && !updateIdentity.isPending;

  const submit = () => {
    if (!canSave) return;
    updateIdentity.mutate({
      actor: normalizedActor,
      input: {
        roles: normalizedRoles,
        types: normalizedTypes,
        ...(reason.trim() ? { reason: reason.trim() } : {}),
      },
    }, {
      onSuccess: (result) => {
        if (!result.available) return;
        setSaveOutcome(config.data?.available && config.data.data.noTraceMode ? "no_trace" : "traced");
        resetEditor();
      },
    });
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

      <Field orientation="horizontal" className="items-center rounded-xl border bg-muted/15 p-3">
        <FieldContent className="min-w-0">
          <FieldTitle><EyeOff className="size-4" />{t("No-trace mode", "无修模式")}</FieldTitle>
          <FieldDescription>{t("When enabled, a human identity change still enters the permanent identity history, but Carbon does not create a matching Incident. Manually recorded Incidents are never hidden.", "启用后，人类调整身份仍会进入永久身份历史，但 Carbon 不再为这次调整自动创建事件；手工记录的事件不会被隐藏。")}</FieldDescription>
        </FieldContent>
        <Switch checked={config.data?.available ? config.data.data.noTraceMode : false} disabled={unavailable || saveConfig.isPending || config.isLoading} onCheckedChange={(noTraceMode) => saveConfig.mutate({ noTraceMode })} aria-label={t("Enable no-trace mode", "启用无修模式")} />
      </Field>

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
            "当前为自由协作模式：身份分工仍会保留，也可以提前设置；只是接手任务时暂不限制角色和任务类型，旧版客户端也能照常工作。",
          )}
        </div>
      )}

      {saveOutcome && (
        <Alert>
          <History />
          <AlertTitle>{t("Responsibilities updated", "分工已更新")}</AlertTitle>
          <AlertDescription>
            {saveOutcome === "no_trace"
              ? t("The permanent identity history was recorded. No automatic Incident was created because no-trace mode is enabled.", "永久身份历史已经记录；当前启用了无修模式，因此没有自动创建事件。")
              : t("The permanent identity history was recorded, and Carbon also created an Incident so the team can follow the change.", "永久身份历史已经记录，Carbon 也创建了一条事件，团队可以继续跟进这次调整。")}
          </AlertDescription>
        </Alert>
      )}

      {!unavailable && (
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
                  {(record.roles?.length ? record.roles : [record.role]).map((role) => <Badge key={role} variant="secondary">{workerRoleLabel(role, t)}</Badge>)}
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
                  <FieldLabel>{t("Roles", "身份角色")}</FieldLabel>
                  <IdentityRolePicker value={roles} onChange={setRoles} />
                  <FieldDescription>{t("A Worker may carry several stable responsibilities. Reviewer marks who can handle explicit plan and manual-check reviews.", "一个 Worker 可以承担多个稳定职责；“审核者”用于处理明确的计划审核和人工检查审核。")}</FieldDescription>
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

          {(audit.data?.available ? audit.data.data.audits ?? [] : []).length > 0 && (
            <div className="grid gap-2 border-t pt-3">
              <p className="flex items-center gap-2 text-sm font-medium"><History className="size-4" />{t("Identity history", "身份变更历史")}</p>
              <div className="grid gap-1.5">
                {(audit.data?.available ? audit.data.data.audits ?? [] : []).slice().reverse().slice(0, 12).map((item) => (
                  <div key={item.id} className="flex flex-wrap items-center gap-1.5 rounded-lg bg-muted/30 px-3 py-2 text-xs">
                    <WorkerIdentity actor={item.actor} compact />
                    <span className="text-muted-foreground">{item.operation === "claimed" ? t("set responsibilities", "设置了分工") : t("changed responsibilities", "调整了分工")}</span>
                    <div className="flex flex-wrap gap-1">{item.afterRoles.map((role) => <Badge key={role} variant="outline">{workerRoleLabel(role, t)}</Badge>)}</div>
                    <span className="ml-auto text-[10px] text-muted-foreground">{displayName(item.changedBy)} · {timeAgo(item.at)}</span>
                    <code className="rounded bg-muted px-1 py-0.5 text-[9px] text-muted-foreground">{item.changedBy}</code>
                    {item.reason && <p className="w-full text-muted-foreground">{item.reason}</p>}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function IdentityRolePicker({ value, onChange }: { value: string[]; onChange: (roles: string[]) => void }) {
  const { t } = useI18n();
  const [custom, setCustom] = useState("");
  const selected = new Set(value);
  const normalizedCustom = custom.trim().toLowerCase();
  const customValid = /^[a-z][a-z0-9_-]{0,79}$/.test(normalizedCustom) && !selected.has(normalizedCustom);
  const toggle = (role: string) => onChange(selected.has(role) ? value.filter((item) => item !== role) : [...value, role]);
  return (
    <div className="grid gap-2 rounded-lg border bg-background p-2">
      <div className="flex flex-wrap gap-1.5">
        {CARBON_BUILT_IN_ROLES.map((role) => <Button key={role} type="button" size="xs" variant={selected.has(role) ? "secondary" : "outline"} onClick={() => toggle(role)}>{selected.has(role) && <Check />}{workerRoleLabel(role, t)}</Button>)}
        {value.filter((role) => !CARBON_BUILT_IN_ROLES.includes(role as typeof CARBON_BUILT_IN_ROLES[number])).map((role) => <Button key={role} type="button" size="xs" variant="secondary" onClick={() => toggle(role)}><Check />{role}</Button>)}
      </div>
      <div className="flex gap-2">
        <Input value={custom} maxLength={80} onChange={(event) => setCustom(event.target.value)} className="h-8" placeholder={t("Custom role key, e.g. qa_lead", "自定义角色键，例如 qa_lead")} />
        <Button type="button" size="sm" variant="outline" disabled={!customValid} onClick={() => { onChange([...value, normalizedCustom]); setCustom(""); }}><Plus />{t("Add", "添加")}</Button>
      </div>
    </div>
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
