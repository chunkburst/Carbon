import { useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowUpRight,
  Check,
  CircleDotDashed,
  Clock3,
  LoaderCircle,
  MessageCircle,
  MessagesSquare,
  Plus,
  Search,
  Send,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import type { Task } from "@/lib/api";
import type {
  CarbonIncident,
  CarbonIncidentKind,
  CarbonIncidentSeverity,
  CarbonIncidentStatus,
  CarbonScopeInput,
  CarbonWorkerIdentityAudit,
} from "@/lib/carbon-api";
import { displayName } from "@/lib/identity";
import { useI18n } from "@/lib/i18n";
import {
  useCarbonIncidents,
  useCarbonWorkerIdentityAudit,
  useCreateCarbonIncident,
  useReplyCarbonIncident,
  useUpdateCarbonIncident,
} from "@/lib/queries";
import { carbonTaskTypeLabel } from "@/lib/task-labels";
import { cn, timeAgo } from "@/lib/utils";
import { workerRoleLabel } from "@/lib/worker-roles";

const INCIDENT_KINDS: CarbonIncidentKind[] = ["sudden", "long_running", "investigation", "other"];
const INCIDENT_SEVERITIES: CarbonIncidentSeverity[] = ["info", "low", "normal", "high", "urgent"];

function kindLabel(kind: CarbonIncidentKind, t: ReturnType<typeof useI18n>["t"]): string {
  const labels: Record<string, [string, string]> = {
    sudden: ["Unexpected", "突发"],
    long_running: ["Long-running", "长期"],
    investigation: ["Investigation", "调查"],
    identity_change: ["Identity change", "身份调整"],
    other: ["Other", "其他"],
  };
  const label = labels[kind];
  return label ? t(label[0], label[1]) : kind;
}

function severityLabel(severity: CarbonIncidentSeverity, t: ReturnType<typeof useI18n>["t"]): string {
  const labels: Record<CarbonIncidentSeverity, [string, string]> = {
    info: ["Note", "记录"],
    low: ["Low", "较低"],
    normal: ["Normal", "普通"],
    high: ["High", "重要"],
    urgent: ["Urgent", "紧急"],
  };
  const label = labels[severity];
  return t(label[0], label[1]);
}

function statusLabel(status: CarbonIncidentStatus, t: ReturnType<typeof useI18n>["t"]): string {
  const labels: Record<CarbonIncidentStatus, [string, string]> = {
    open: ["Open", "待跟进"],
    investigating: ["Investigating", "研究中"],
    resolved: ["Resolved", "已有结论"],
    closed: ["Archived", "已归档"],
  };
  const label = labels[status];
  return t(label[0], label[1]);
}

function incidentTitle(incident: CarbonIncident, t: ReturnType<typeof useI18n>["t"]): string {
  if (incident.origin === "identity_change") return t("Worker responsibilities changed", "Worker 分工有调整");
  return incident.title;
}

type IdentityIncidentNarrative = {
  text: string;
  subjectActor?: string;
  changedBy: string;
};

function identityIncidentNarrative(
  incident: CarbonIncident,
  audit: CarbonWorkerIdentityAudit | undefined,
  currentActor: string | undefined,
  t: ReturnType<typeof useI18n>["t"],
): IdentityIncidentNarrative {
  if (incident.origin !== "identity_change") {
    return { text: incident.body || t("No description was added.", "暂时没有补充说明。"), changedBy: incident.createdBy };
  }

  // Older identity Incidents only carried a human-readable sentence. Keep them
  // useful while preferring the linked audit, which contains stable structured data.
  const legacy = incident.body?.match(/^(.+?) 的身份由 (.+?) (认领|调整)。?$/);
  const subjectActor = audit?.actor ?? legacy?.[1];
  const changedBy = audit?.changedBy ?? legacy?.[2] ?? incident.createdBy;
  const worker = displayName(subjectActor) || t("this Worker", "这位 Worker");
  const changedByCurrentActor = Boolean(currentActor && changedBy === currentActor);
  const changer = changedByCurrentActor
    ? t("You", "你")
    : displayName(changedBy) || t("A team member", "一位团队成员");

  if (!audit) {
    return {
      text: changedByCurrentActor
        ? t("You updated {worker}'s responsibilities.", "你调整了 {worker} 的分工。", { worker })
        : t("{changer} updated {worker}'s responsibilities.", "{changer} 调整了 {worker} 的分工。", { changer, worker }),
      subjectActor,
      changedBy,
    };
  }

  const roles = audit.afterRoles.map((role) => workerRoleLabel(role, t)).join(t(", ", "、"));
  const types = audit.afterTypes.map((type) => carbonTaskTypeLabel(type, t)).join(t(", ", "、"));
  const typeClause = types
    ? t("; eligible for {types} tasks", "；可接手{types}类任务", { types })
    : "";
  return {
    text: audit.operation === "claimed"
      ? changedByCurrentActor
        ? t(
          "You set {worker}'s responsibilities to {roles}{typeClause}.",
          "你为 {worker} 设置了分工：{roles}{typeClause}。",
          { worker, roles, typeClause },
        )
        : t(
          "{changer} set {worker}'s responsibilities to {roles}{typeClause}.",
          "{changer} 为 {worker} 设置了分工：{roles}{typeClause}。",
          { changer, worker, roles, typeClause },
        )
      : changedByCurrentActor
        ? t(
          "You changed {worker}'s responsibilities to {roles}{typeClause}.",
          "你将 {worker} 的分工调整为：{roles}{typeClause}。",
          { worker, roles, typeClause },
        )
        : t(
          "{changer} changed {worker}'s responsibilities to {roles}{typeClause}.",
          "{changer} 将 {worker} 的分工调整为：{roles}{typeClause}。",
          { changer, worker, roles, typeClause },
        ),
    subjectActor,
    changedBy,
  };
}

function severityTone(severity: CarbonIncidentSeverity): string {
  if (severity === "urgent") return "border-destructive/35 bg-destructive/8 text-destructive";
  if (severity === "high") return "border-amber-500/35 bg-amber-500/8 text-amber-700 dark:text-amber-300";
  return "";
}

export function Incidents({
  scope,
  tasks,
  currentActor,
  onOpenTask,
}: {
  scope: CarbonScopeInput;
  tasks: Task[];
  currentActor?: string;
  onOpenTask?: (task: Task) => void;
}) {
  const { t } = useI18n();
  const incidentsQuery = useCarbonIncidents(scope);
  const identityAuditQuery = useCarbonWorkerIdentityAudit(scope);
  const create = useCreateCarbonIncident(scope);
  const update = useUpdateCarbonIncident(scope);
  const reply = useReplyCarbonIncident(scope);
  const incidents = useMemo(
    () => incidentsQuery.data?.available ? incidentsQuery.data.data.incidents ?? [] : [],
    [incidentsQuery.data],
  );
  const identityAudits = useMemo(
    () => identityAuditQuery.data?.available ? identityAuditQuery.data.data.audits ?? [] : [],
    [identityAuditQuery.data],
  );
  const identityAuditByIncident = useMemo(
    () => new Map(identityAudits.filter((item) => item.relatedIncidentId).map((item) => [item.relatedIncidentId as string, item])),
    [identityAudits],
  );
  const [selectedId, setSelectedId] = useState<string>();
  const [query, setQuery] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [replyBody, setReplyBody] = useState("");

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return incidents;
    return incidents.filter((incident) => [
      incident.title,
      incident.body,
      incident.id,
      incident.kind,
      incident.createdBy,
      ...(incident.relatedTaskIds ?? []),
    ].filter(Boolean).join(" ").toLocaleLowerCase().includes(needle));
  }, [incidents, query]);
  const selected = incidents.find((incident) => incident.id === selectedId) ?? filtered[0];

  useEffect(() => {
    if (!selectedId && filtered[0]) setSelectedId(filtered[0].id);
    if (selectedId && !incidents.some((incident) => incident.id === selectedId)) setSelectedId(filtered[0]?.id);
  }, [filtered, incidents, selectedId]);

  const selectedNarrative = selected
    ? identityIncidentNarrative(selected, identityAuditByIncident.get(selected.id), currentActor, t)
    : undefined;

  const sendReply = () => {
    if (!selected || !replyBody.trim()) return;
    reply.mutate({ id: selected.id, body: replyBody.trim() }, {
      onSuccess: (result) => {
        if (result.available) setReplyBody("");
      },
    });
  };

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <MessagesSquare className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">{t("Incidents", "事件")}</h1>
            <p className="truncate text-xs text-muted-foreground">
              {t("Keep the investigation trail here; create a task when a result must be delivered.", "这里保留排查过程；确定必须交付结果时，再把它变成任务。")}
            </p>
          </div>
        </div>
        <div className="flex min-w-0 flex-1 items-center justify-end gap-2 sm:flex-none">
          <div className="relative min-w-40 flex-1 sm:w-64 sm:flex-none">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input value={query} onChange={(event) => setQuery(event.target.value)} className="h-8 pl-8" placeholder={t("Search incidents", "搜索事件")} />
          </div>
          <Button size="sm" onClick={() => setCreateOpen(true)}><Plus />{t("Record incident", "记录事件")}</Button>
        </div>
      </header>

      {incidentsQuery.isLoading ? (
        <Empty className="m-4 min-h-64"><EmptyHeader><EmptyMedia variant="icon"><LoaderCircle className="animate-spin motion-reduce:animate-none" /></EmptyMedia><EmptyTitle>{t("Loading incidents", "正在整理事件记录")}</EmptyTitle><EmptyDescription>{t("Restoring this project's investigation trail.", "正在读取这个项目之前留下的排查过程。")}</EmptyDescription></EmptyHeader></Empty>
      ) : !incidentsQuery.data?.available ? (
        <Empty className="m-4 min-h-64"><EmptyHeader><EmptyMedia variant="icon"><AlertTriangle /></EmptyMedia><EmptyTitle>{t("Incidents are unavailable", "事件功能暂不可用")}</EmptyTitle><EmptyDescription>{t("Update the local Carbon service and reopen this project.", "更新本地 Carbon 服务后重新打开项目。")}</EmptyDescription></EmptyHeader></Empty>
      ) : filtered.length === 0 ? (
        <Empty className="m-4 min-h-64"><EmptyHeader><EmptyMedia variant="icon"><MessagesSquare /></EmptyMedia><EmptyTitle>{query ? t("No matching incidents", "没有匹配的事件") : t("Nothing needs a discussion yet", "目前还没有需要追踪的事件")}</EmptyTitle><EmptyDescription>{query ? t("Try a task ID, title, or participant.", "可以换任务编号、标题或参与者再找找。") : t("When a strange bug or an unresolved question appears, keep the attempts and findings here.", "遇到古怪 Bug 或暂时无解的问题时，把尝试和结论留在这里。")}</EmptyDescription></EmptyHeader></Empty>
      ) : (
        <div className="grid min-h-0 flex-1 md:grid-cols-[minmax(17rem,0.82fr)_minmax(0,1.6fr)]">
          <ScrollArea className="min-h-0 border-b md:border-r md:border-b-0">
            <div className="grid gap-1 p-2">
              {filtered.map((incident) => {
                const narrative = identityIncidentNarrative(incident, identityAuditByIncident.get(incident.id), currentActor, t);
                return (
                <button
                  key={incident.id}
                  type="button"
                  onClick={() => setSelectedId(incident.id)}
                  className={cn("grid gap-1 rounded-xl border border-transparent px-3 py-2.5 text-left transition-colors hover:bg-muted/60", selected?.id === incident.id && "border-border bg-muted/70")}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{incidentTitle(incident, t)}</span>
                    <Badge variant="outline" className={cn("ml-auto shrink-0", severityTone(incident.severity))}>{severityLabel(incident.severity, t)}</Badge>
                  </div>
                  <p className="line-clamp-2 text-xs leading-5 text-muted-foreground">{narrative.text}</p>
                  <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
                    <span>{kindLabel(incident.kind, t)}</span><span>·</span><span>{statusLabel(incident.status, t)}</span><span>·</span><span>{timeAgo(incident.updatedAt)}</span>
                    <span className="ml-auto flex items-center gap-1"><MessageCircle className="size-3" />{incident.replies?.length ?? 0}</span>
                  </div>
                </button>
                );
              })}
            </div>
          </ScrollArea>

          {selected && selectedNarrative && (
            <div className="flex min-h-0 min-w-0 flex-col">
              <div className="shrink-0 border-b px-4 py-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="secondary">{kindLabel(selected.kind, t)}</Badge>
                      <Badge variant="outline" className={severityTone(selected.severity)}>{severityLabel(selected.severity, t)}</Badge>
                      {selected.origin === "identity_change" && <Badge variant="outline">{t("Recorded automatically", "由 Carbon 自动记录")}</Badge>}
                    </div>
                    <h2 className="mt-2 text-base font-semibold">{incidentTitle(selected, t)}</h2>
                    <p className="mt-1 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{selectedNarrative.text}</p>
                  </div>
                  <Select value={selected.status} onValueChange={(status) => update.mutate({ id: selected.id, status: status as CarbonIncidentStatus })} disabled={update.isPending}>
                    <SelectTrigger size="sm" className="w-28"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {(["open", "investigating", "resolved", "closed"] as CarbonIncidentStatus[]).map((status) => <SelectItem key={status} value={status}>{statusLabel(status, t)}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
                <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <span>{selectedNarrative.changedBy === currentActor ? t("You", "你") : displayName(selectedNarrative.changedBy)}</span>
                  <code className="rounded bg-muted px-1 py-0.5 text-[10px]">{selectedNarrative.changedBy}</code>
                  {selectedNarrative.subjectActor && <><span>·</span><span>{t("Worker", "Worker")}</span><code className="rounded bg-muted px-1 py-0.5 text-[10px]">{selectedNarrative.subjectActor}</code></>}
                  <span>·</span><Clock3 className="size-3" /><span>{timeAgo(selected.createdAt)}</span>
                  {(selected.relatedTaskIds ?? []).map((id) => {
                    const task = tasks.find((item) => item.id === id);
                    return <Button key={id} type="button" variant="outline" size="xs" disabled={!task || !onOpenTask} onClick={() => task && onOpenTask?.(task)}>{id}<ArrowUpRight /></Button>;
                  })}
                </div>
              </div>

              <ScrollArea className="min-h-0 flex-1">
                <ol className="grid gap-3 p-4">
                  {(selected.replies ?? []).length === 0 ? (
                    <li className="rounded-xl border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">{t("No one has added a finding yet. Questions, failed attempts, and partial clues all belong here.", "还没人留下研究结果；问题、失败尝试和零散线索都可以写在这里。")}</li>
                  ) : selected.replies?.map((item) => (
                    <li key={item.id} className="rounded-xl border bg-background px-3 py-2.5">
                      <div className="flex items-center gap-2 text-xs"><span className="font-medium">{displayName(item.author)}</span><code className="text-[10px] text-muted-foreground">{item.author}</code><time className="ml-auto text-muted-foreground">{timeAgo(item.createdAt)}</time></div>
                      <p className="mt-1.5 whitespace-pre-wrap text-sm leading-6">{item.body}</p>
                    </li>
                  ))}
                </ol>
              </ScrollArea>

              <div className="shrink-0 border-t p-3">
                <div className="flex items-end gap-2">
                  <Textarea value={replyBody} onChange={(event) => setReplyBody(event.target.value)} rows={2} className="min-h-16 resize-y" placeholder={t("Add what you tried, what changed, or what is still unclear…", "补充刚做过什么、发现了什么，或者还有哪里想不通……")} />
                  <Button size="icon" disabled={!replyBody.trim() || reply.isPending} onClick={sendReply} aria-label={t("Send reply", "发送回复")}><Send /></Button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      <CreateIncidentDialog open={createOpen} onOpenChange={setCreateOpen} tasks={tasks} pending={create.isPending} onCreate={(input) => create.mutate(input, { onSuccess: (result) => { if (result.available) { setSelectedId(result.data.id); setCreateOpen(false); } } })} />
    </div>
  );
}

function CreateIncidentDialog({
  open,
  onOpenChange,
  tasks,
  pending,
  onCreate,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tasks: Task[];
  pending: boolean;
  onCreate: (input: { kind: CarbonIncidentKind; severity: CarbonIncidentSeverity; title: string; body?: string; relatedTaskIds?: string[] }) => void;
}) {
  const { t } = useI18n();
  const [kind, setKind] = useState<CarbonIncidentKind>("sudden");
  const [severity, setSeverity] = useState<CarbonIncidentSeverity>("normal");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [taskIds, setTaskIds] = useState<string[]>([]);
  const canCreate = Boolean(title.trim() && body.trim() && !pending);

  const reset = () => {
    setKind("sudden");
    setSeverity("normal");
    setTitle("");
    setBody("");
    setTaskIds([]);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) reset(); }}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader><DialogTitle>{t("Record an incident", "记录一个事件")}</DialogTitle><DialogDescription>{t("Capture the situation and the clues you already have. This does not create a delivery obligation.", "先把现象和已有线索记下来；它不会自动变成必须交付的任务。")}</DialogDescription></DialogHeader>
        <FieldGroup className="gap-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field><FieldLabel>{t("Kind", "类型")}</FieldLabel><Select value={kind} onValueChange={(value) => setKind(value as CarbonIncidentKind)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{INCIDENT_KINDS.map((item) => <SelectItem key={item} value={item}>{kindLabel(item, t)}</SelectItem>)}</SelectContent></Select></Field>
            <Field><FieldLabel>{t("Attention", "关注度")}</FieldLabel><Select value={severity} onValueChange={(value) => setSeverity(value as CarbonIncidentSeverity)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{INCIDENT_SEVERITIES.map((item) => <SelectItem key={item} value={item}>{severityLabel(item, t)}</SelectItem>)}</SelectContent></Select></Field>
          </div>
          <Field><FieldLabel htmlFor="incident-title">{t("What happened?", "发生了什么？")}</FieldLabel><Input id="incident-title" value={title} maxLength={240} onChange={(event) => setTitle(event.target.value)} placeholder={t("For example: requests intermittently return 429", "例如：请求偶发返回 429")} /></Field>
          <Field><FieldLabel htmlFor="incident-body">{t("What do we know so far?", "目前知道些什么？")}</FieldLabel><Textarea id="incident-body" value={body} onChange={(event) => setBody(event.target.value)} rows={5} placeholder={t("Symptoms, attempts, observations, and questions…", "现象、尝试过的方法、观察结果，以及还没想通的问题……")} /><FieldDescription>{t("Write enough context for another Worker—or your future self—to continue.", "写到另一个 Worker（或者之后的自己）能够接着查就够了。")}</FieldDescription></Field>
          {tasks.length > 0 && <Field><FieldLabel>{t("Related tasks (optional)", "关联任务（可选）")}</FieldLabel><ScrollArea className="max-h-36 rounded-lg border"><div className="grid gap-1 p-2">{tasks.slice(0, 80).map((task) => { const checked = taskIds.includes(task.id); return <label key={task.id} className="flex cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 hover:bg-muted"><Checkbox checked={checked} onCheckedChange={(value) => setTaskIds(value === true ? [...taskIds, task.id] : taskIds.filter((id) => id !== task.id))} /><span className="min-w-0"><span className="block truncate text-xs font-medium">{task.title}</span><span className="font-mono text-[10px] text-muted-foreground">{task.id}</span></span>{checked && <Check className="ml-auto size-3.5 text-brand" />}</label>; })}</div></ScrollArea></Field>}
        </FieldGroup>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t("Cancel", "取消")}</Button><Button disabled={!canCreate} onClick={() => onCreate({ kind, severity, title: title.trim(), body: body.trim(), ...(taskIds.length ? { relatedTaskIds: taskIds } : {}) })}><CircleDotDashed />{t("Save incident", "保存事件")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
