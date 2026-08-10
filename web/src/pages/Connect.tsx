import { useMemo, useState } from "react";
import { Check, ChevronDown, ExternalLink, Loader2, Plug, Unplug } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { CodeBlock } from "@/components/CodeBlock";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { useAgentManual, useConnectAgent, useDisconnectAgent, useIntegrations } from "@/lib/queries";
import type { AgentStatus, Status } from "@/lib/api";
import type { CarbonAgentStatus, CarbonHomeProject, CarbonMCPRouting, CarbonScope } from "@/lib/carbon-api";
import { useCarbonAgentGuide, useCarbonIntegrations, useConnectCarbonAgent, useDisconnectCarbonAgent } from "@/lib/queries";

// Connect is the integrations page: it detects which AI agents are installed and wires them
// to this project's MCP server in one click (the Carbon process writes each agent's config),
// with a copy-paste manual guide for everything else.
export function Connect({ path }: { path: string; status: Status }) {
  const { t } = useI18n();
  const { data: agents, isLoading } = useIntegrations(path);

  const installed = (agents ?? []).filter((a) => a.installed);
  const others = (agents ?? []).filter((a) => !a.installed);

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b px-4">
        <Plug className="size-4 text-muted-foreground" />
        <h1 className="text-[13px] font-medium">{t("Connect an agent", "连接智能体")}</h1>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-6 p-4 sm:p-6">
          <p className="text-sm text-muted-foreground">
            {t(
              "Give an AI agent the same task tools this UI uses. One click writes its MCP config for this project — or open the manual setup to wire it up by hand. Each agent connects under its own identity (e.g. agent:cursor) so Carbon attributes its work correctly; edit it on a card to run more than one instance.",
              "让智能体使用与此界面相同的任务工具。单击即可为此项目写入 MCP 配置，也可以打开手动设置自行配置。每个智能体都以自己的身份（例如 agent:cursor）连接，Carbon 会正确归属其工作；你可以在卡片中编辑身份以运行多个实例。",
            )}
          </p>

          {isLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> {t("Detecting agents…", "正在检测智能体…")}
            </div>
          ) : (
            <>
              {installed.length > 0 && (
                <Section title={t("Installed on this machine", "已安装在此设备")}>
                  {installed.map((a) => (
                    <AgentCard key={a.id} path={path} agent={a} />
                  ))}
                </Section>
              )}
              <Section title={installed.length ? t("All integrations", "全部集成") : t("Integrations", "集成")}>
                {others.map((a) => (
                  <AgentCard key={a.id} path={path} agent={a} />
                ))}
              </Section>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <h2 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {title}
      </h2>
      <div className="grid gap-2">{children}</div>
    </section>
  );
}

function AgentCard({ path, agent }: { path: string; agent: AgentStatus }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  // Each agent connects as itself by default (agent:cursor); editable for multiple instances.
  const [actor, setActor] = useState(`agent:${agent.id}`);
  const connect = useConnectAgent(path);
  const disconnect = useDisconnectAgent(path);
  const manual = useAgentManual(path, agent.id, actor, open);
  const canAuto = agent.mode === "auto";
  const busy = connect.isPending || disconnect.isPending;

  return (
    <div className="flex flex-col rounded-lg border bg-card p-3">
      <div className="flex items-center gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-md bg-foreground/[0.06] text-[13px] font-semibold">
          {agent.name.slice(0, 1)}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-[13px] font-medium">{agent.name}</span>
            {agent.connected && (
              <span className="flex items-center gap-0.5 text-[11px] font-medium text-success">
                <Check className="size-3" /> {t("Connected", "已连接")}
              </span>
            )}
          </div>
          {/* Identity is the agent's own (agent:<id>) and editable inline for extra instances. */}
          <input
            value={actor}
            onChange={(e) => setActor(e.target.value)}
            spellCheck={false}
            aria-label={t("{name} identity", "{name} 的身份", { name: agent.name })}
            className="w-full truncate rounded bg-transparent font-mono text-[11px] text-muted-foreground outline-none hover:text-foreground focus:text-foreground"
          />
        </div>
        {canAuto ? (
          <div className="flex shrink-0 items-center gap-1">
            <Button
              size="sm"
              variant={agent.connected ? "outline" : "default"}
              className="h-7"
              disabled={busy}
              onClick={() => connect.mutate({ agent: agent.id, actor })}
            >
              {connect.isPending && <Loader2 className="size-3 animate-spin" />}
              {agent.connected ? t("Reconnect", "重新连接") : t("Connect", "连接")}
            </Button>
            {agent.connected && (
              <Button
                size="icon"
                variant="ghost"
                className="size-7 text-muted-foreground hover:text-destructive"
                title={t("Disconnect (removes this agent's Carbon MCP entry)", "断开连接（会移除此智能体的 Carbon MCP 条目）")}
                aria-label={t("Disconnect {name}", "断开 {name} 的连接", { name: agent.name })}
                disabled={busy}
                onClick={() => disconnect.mutate(agent.id)}
              >
                <Unplug className="size-3.5" />
              </Button>
            )}
          </div>
        ) : (
          <Badge variant="outline" className="h-5 shrink-0 text-[10px] text-muted-foreground">
            {t("Manual", "手动")}
          </Badge>
        )}
      </div>

      <Collapsible open={open} onOpenChange={setOpen}>
        <div className="mt-2 flex items-center gap-3">
          <CollapsibleTrigger className="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground">
            <ChevronDown className={cn("size-3 transition-transform", open && "rotate-180")} />
            {t("Manual setup", "手动设置")}
          </CollapsibleTrigger>
          {agent.docsURL && (
            <a
              href={agent.docsURL}
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
            >
              <ExternalLink className="size-3" /> {t("Docs", "文档")}
            </a>
          )}
        </div>
        <CollapsibleContent className="pt-2">
          {manual.isLoading ? (
            <p className="text-[11px] text-muted-foreground">{t("Loading…", "正在加载…")}</p>
          ) : manual.data ? (
            <div className="space-y-1.5">
              {manual.data.path && (
                <p className="font-mono text-[11px] text-muted-foreground">
                  {t("Add to {path}", "添加到 {path}", { path: prettyPath(manual.data.path) })}
                </p>
              )}
              <CodeBlock label={manual.data.lang} text={manual.data.config} />
            </div>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              {t("No snippet — see this agent's docs for MCP setup.", "没有可用的配置片段，请查看此智能体的文档以设置 MCP。")}
            </p>
          )}
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}

// prettyPath collapses the user's home dir for compact display.
function prettyPath(p: string): string {
  return p.replace(/^\/(?:Users|home)\/[^/]+/, "~");
}

type CarbonConnectScopeMode = "session" | "cluster" | "project";

const MANUAL_GUIDE_AGENTS = [
  { id: "claude", name: "Claude Code" },
  { id: "cursor", name: "Cursor" },
  { id: "codex", name: "Codex" },
  { id: "windsurf", name: "Windsurf" },
  { id: "opencode", name: "OpenCode" },
  { id: "kilo", name: "Kilo Code" },
  { id: "pi", name: "Pi" },
  { id: "antigravity", name: "Antigravity" },
] as const;

/**
 * CarbonConnectPanel deliberately has no filesystem-path prop. Its MCP command is
 * scoped by the selected stable Home/cluster/project IDs; a cluster-only connection
 * may select a project solely to decide where an agent config file is written.
 */
export function CarbonConnectPanel({
 home,
 clusterId,
  projects,
  defaultProjectId,
  defaultScope = "session",
}: {
  home: string;
  clusterId?: string;
  projects: CarbonHomeProject[];
  defaultProjectId?: string;
  defaultScope?: CarbonConnectScopeMode;
}) {
  const { t } = useI18n();
  const [scopeMode, setScopeMode] = useState<CarbonConnectScopeMode>(defaultScope);
  const [projectId, setProjectId] = useState(defaultProjectId ?? projects[0]?.id ?? "");
  const [configProjectId, setConfigProjectId] = useState(defaultProjectId ?? "");
  const [manualAgent, setManualAgent] = useState<(typeof MANUAL_GUIDE_AGENTS)[number]["id"]>("codex");
  const [actor, setActor] = useState("agent:codex");
  const effectiveScopeMode: CarbonConnectScopeMode = !clusterId && scopeMode === "cluster" ? "session" : scopeMode;

  const scope = useMemo<CarbonScope>(() => (
    effectiveScopeMode === "session"
      ? { home }
      : effectiveScopeMode === "project"
        ? { home, clusterId, projectId }
        : { home, clusterId }
  ), [clusterId, effectiveScopeMode, home, projectId]);
  const routing = effectiveScopeMode === "session" ? "session" : "pinned";
  const scopeIsReady = effectiveScopeMode !== "project" || Boolean(projectId);
  const configTargetId = effectiveScopeMode === "project" ? projectId : configProjectId || undefined;
  const noConfigTarget = effectiveScopeMode !== "project" && !configTargetId;
  const integrations = useCarbonIntegrations(scope, configTargetId, routing);
  const manualGuide = useCarbonAgentGuide(scope, { agent: manualAgent, actor, configProjectId: configTargetId, routing }, noConfigTarget && scopeIsReady);
  const selectedProject = projects.find((project) => project.id === projectId);

  const changeManualAgent = (agent: (typeof MANUAL_GUIDE_AGENTS)[number]["id"]) => {
    setManualAgent(agent);
    setActor(`agent:${agent}`);
  };

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        {t(
          "Connect an MCP agent using stable Carbon IDs. The source folder is used only to write that agent's local configuration.",
          "使用稳定的 Carbon ID 连接 MCP 智能体。源目录仅用于写入该智能体的本地配置。",
        )}
      </p>

      <div className="rounded-lg border p-3">
        <p className="mb-2 text-sm font-medium">{t("MCP scope", "MCP 范围")}</p>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant={effectiveScopeMode === "session" ? "secondary" : "outline"} onClick={() => setScopeMode("session")}>
            {t("Project session · Recommended", "项目会话 · 推荐")}
          </Button>
          <Button size="sm" variant={effectiveScopeMode === "project" ? "secondary" : "outline"} onClick={() => setScopeMode("project")} disabled={!projects.length}>
            {t("Pinned project", "固定项目")}
          </Button>
          {clusterId && <Button size="sm" variant={effectiveScopeMode === "cluster" ? "secondary" : "outline"} onClick={() => setScopeMode("cluster")}>
            {t("Entire cluster · Advanced", "整个集群 · 高级")}
          </Button>}
        </div>
        {effectiveScopeMode === "session" ? (
          <div className="mt-3 grid gap-3">
            <Alert>
              <AlertTitle>{t("Sticky active project", "活动项目会话")}</AlertTitle>
              <AlertDescription>{t(
                "The MCP process binds only to this Carbon Home. The agent creates or selects one active project, keeps using it, and can explicitly switch projects later without reconnecting.",
                "MCP 进程只绑定此 Carbon 主目录。AI 创建或选择一个活动项目后会持续使用它，需要时可显式切换，无需重新连接。",
              )}</AlertDescription>
            </Alert>
            <div className="grid gap-1.5">
              <label className="text-xs font-medium">{t("Configuration project (write location only)", "配置写入位置（不绑定 MCP 项目）")}</label>
              <Select value={configTargetId || "manual"} onValueChange={(value) => setConfigProjectId(value === "manual" ? "" : value)}>
                <SelectTrigger className="w-full">{projects.find((project) => project.id === configTargetId)?.name ?? t("None — show manual guide", "无 — 显示手动指南")}</SelectTrigger>
                <SelectContent>
                  <SelectItem value="manual">{t("None — show manual guide", "无 — 显示手动指南")}</SelectItem>
                  {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">{t(
                "This project only tells Carbon where to write the agent configuration. It is not baked into the MCP command.",
                "这里只决定把 AI 配置写到哪个源码目录，不会把该项目写死在 MCP 启动命令中。",
              )}</p>
            </div>
          </div>
        ) : effectiveScopeMode === "project" ? (
          <div className="mt-3 grid gap-1.5">
            <Alert>
              <AlertTitle>{t("Compatibility mode", "兼容固定模式")}</AlertTitle>
              <AlertDescription>{t(
                "The selected project ID is written into the MCP command. This preserves the previous strict behavior and requires reconnecting to use another project.",
                "所选项目 ID 会写入 MCP 启动命令。该模式保留旧版严格边界，切换项目时需要重新连接。",
              )}</AlertDescription>
            </Alert>
            <label className="text-xs font-medium">{t("Project bound into the MCP command", "写入 MCP 命令的项目")}</label>
            <Select value={projectId || "unset"} onValueChange={(value) => { const next = value === "unset" ? "" : value; setProjectId(next); setConfigProjectId(next); }}>
              <SelectTrigger className="w-full">{selectedProject?.name ?? t("Choose project", "选择项目")}</SelectTrigger>
              <SelectContent>
                <SelectItem value="unset">{t("Choose project", "选择项目")}</SelectItem>
                {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
        ) : (
          <div className="mt-3 grid gap-1.5">
            <Alert>
              <AlertTitle>{t("Explicit cluster-wide scope", "显式集群范围")}</AlertTitle>
              <AlertDescription>{t(
                "This agent can read the shared task pool across every project in this cluster. Ordinary task work should use a project-bound connection.",
                "该 AI 可读取此集群内所有项目共享的任务池。普通任务建议使用项目会话。",
              )}</AlertDescription>
            </Alert>
            <label className="text-xs font-medium">{t("Configuration project (write location only)", "配置项目（仅写入位置）")}</label>
            <Select value={configProjectId || "manual"} onValueChange={(value) => setConfigProjectId(value === "manual" ? "" : value)}>
              <SelectTrigger className="w-full">{projects.find((project) => project.id === configProjectId)?.name ?? t("None — show manual guide", "无 — 显示手动指南")}</SelectTrigger>
              <SelectContent>
                <SelectItem value="manual">{t("None — show manual guide", "无 — 显示手动指南")}</SelectItem>
                {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">{t("This choice only locates an agent config file. The generated MCP command remains scoped to the entire cluster.", "此选择仅定位智能体配置文件。生成的 MCP 命令仍保持整个集群范围。")}</p>
          </div>
        )}
      </div>

      {!scopeIsReady ? (
        <Alert><AlertTitle>{t("Choose a project", "请选择项目")}</AlertTitle><AlertDescription>{t("A project-scoped MCP connection requires a project ID.", "项目范围的 MCP 连接需要项目 ID。")}</AlertDescription></Alert>
      ) : noConfigTarget ? (
        <ManualCarbonGuide
          actor={actor}
          manualAgent={manualAgent}
          onAgentChange={changeManualAgent}
          query={manualGuide}
          routing={routing}
        />
      ) : integrations.isLoading ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t("Detecting agents…", "正在检测智能体…")}</div>
      ) : integrations.data?.available ? (
        <div className="space-y-2">
          {integrations.data.data.reason && <Alert><AlertDescription>{integrations.data.data.reason}</AlertDescription></Alert>}
          {integrations.data.data.agents.length ? integrations.data.data.agents.map((agent) => (
            <CarbonAgentCard key={agent.id} agent={agent} scope={scope} configProjectId={configTargetId} routing={routing} />
          )) : <p className="text-sm text-muted-foreground">{t("No agent configurations are available for this selected source.", "此选定源中没有可用的智能体配置。")}</p>}
        </div>
      ) : (
        <Alert><AlertTitle>{t("Carbon Connect API unavailable", "Carbon Connect API 不可用")}</AlertTitle><AlertDescription>{t("This server does not advertise the scoped MCP connection API.", "此服务端未声明带范围的 MCP 连接 API。")}</AlertDescription></Alert>
      )}
    </div>
  );
}

function ManualCarbonGuide({
  actor,
  manualAgent,
  onAgentChange,
  query,
  routing,
}: {
  actor: string;
  manualAgent: (typeof MANUAL_GUIDE_AGENTS)[number]["id"];
  onAgentChange: (agent: (typeof MANUAL_GUIDE_AGENTS)[number]["id"]) => void;
  query: ReturnType<typeof useCarbonAgentGuide>;
  routing: CarbonMCPRouting;
}) {
  const { t } = useI18n();
  const guide = query.data?.available ? query.data.data : undefined;
  return (
    <Alert>
      <AlertTitle>{t("Choose a configuration project for one-click setup", "请选择配置项目以一键设置")}</AlertTitle>
      <AlertDescription className="space-y-3">
        <p>{routing === "session"
          ? t("No source directory was guessed. This guide keeps the MCP command Home-scoped with an explicit active-project session.", "未猜测任何源目录。此指南让 MCP 命令绑定 Carbon 主目录，并显式使用活动项目会话。")
          : t("No source directory was guessed. This guide preserves the selected fixed scope and lets you place the configuration manually.", "未猜测任何源目录。此指南保留所选固定范围，并由你手动放置配置。")}</p>
        <div className="grid gap-1.5">
          <label className="text-xs font-medium">{t("Agent", "智能体")}</label>
          <Select value={manualAgent} onValueChange={(value) => onAgentChange(value as (typeof MANUAL_GUIDE_AGENTS)[number]["id"])}>
            <SelectTrigger className="w-full">{MANUAL_GUIDE_AGENTS.find((agent) => agent.id === manualAgent)?.name}</SelectTrigger>
            <SelectContent>{MANUAL_GUIDE_AGENTS.map((agent) => <SelectItem key={agent.id} value={agent.id}>{agent.name}</SelectItem>)}</SelectContent>
          </Select>
        </div>
        <label className="grid gap-1.5 text-xs font-medium">{t("Agent identity", "智能体身份")}<input value={actor} readOnly className="rounded border bg-muted px-2 py-1 font-mono text-xs text-muted-foreground" /></label>
        {query.isLoading ? <p className="text-xs text-muted-foreground">{t("Loading guide…", "正在加载指南…")}</p> : guide ? <CodeBlock label={guide.lang} text={guide.config} /> : <p className="text-xs text-muted-foreground">{t("No manual guide is available from this server.", "此服务端未提供手动指南。")}</p>}
      </AlertDescription>
    </Alert>
  );
}

function CarbonAgentCard({ agent, scope, configProjectId, routing }: { agent: CarbonAgentStatus; scope: CarbonScope; configProjectId?: string; routing: CarbonMCPRouting }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [actor, setActor] = useState(`agent:${agent.id}`);
  const connect = useConnectCarbonAgent(scope);
  const disconnect = useDisconnectCarbonAgent(scope);
  const manual = useCarbonAgentGuide(scope, { agent: agent.id, actor, configProjectId, routing }, open);
  const busy = connect.isPending || disconnect.isPending;
  const guide = manual.data?.available ? manual.data.data : undefined;

  return (
    <div className="rounded-lg border bg-card p-3">
      <div className="flex items-center gap-3">
        <span className="grid size-8 shrink-0 place-items-center rounded-md bg-foreground/[0.06] text-[13px] font-semibold">{agent.name.slice(0, 1)}</span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5"><span className="truncate text-[13px] font-medium">{agent.name}</span>{agent.connected && <span className="flex items-center gap-0.5 text-[11px] font-medium text-success"><Check className="size-3" />{t("Connected", "已连接")}</span>}</div>
          <input value={actor} onChange={(event) => setActor(event.target.value)} spellCheck={false} aria-label={t("{name} identity", "{name} 的身份", { name: agent.name })} className="w-full truncate rounded bg-transparent font-mono text-[11px] text-muted-foreground outline-none hover:text-foreground focus:text-foreground" />
        </div>
        {agent.mode === "auto" ? <div className="flex shrink-0 items-center gap-1"><Button size="sm" variant={agent.connected ? "outline" : "default"} className="h-7" disabled={busy} onClick={() => connect.mutate({ agent: agent.id, actor, configProjectId, routing })}>{connect.isPending && <Loader2 className="size-3 animate-spin" />}{agent.connected ? t("Reconnect", "重新连接") : t("Connect", "连接")}</Button>{agent.connected && <Button size="icon" variant="ghost" className="size-7 text-muted-foreground hover:text-destructive" aria-label={t("Disconnect {name}", "断开 {name} 的连接", { name: agent.name })} disabled={busy} onClick={() => disconnect.mutate({ agent: agent.id, configProjectId, routing })}><Unplug className="size-3.5" /></Button>}</div> : <Badge variant="outline" className="h-5 shrink-0 text-[10px] text-muted-foreground">{t("Manual", "手动")}</Badge>}
      </div>
      <Collapsible open={open} onOpenChange={setOpen}>
        <div className="mt-2 flex items-center gap-3"><CollapsibleTrigger className="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"><ChevronDown className={cn("size-3 transition-transform", open && "rotate-180")} />{t("Manual setup", "手动设置")}</CollapsibleTrigger>{agent.docsURL && <a href={agent.docsURL} target="_blank" rel="noreferrer" className="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"><ExternalLink className="size-3" />{t("Docs", "文档")}</a>}</div>
        <CollapsibleContent className="pt-2">{manual.isLoading ? <p className="text-[11px] text-muted-foreground">{t("Loading…", "正在加载…")}</p> : guide ? <div className="space-y-1.5">{guide.path && <p className="font-mono text-[11px] text-muted-foreground">{t("Add to {path}", "添加到 {path}", { path: prettyPath(guide.path) })}</p>}<CodeBlock label={guide.lang} text={guide.config} /></div> : <p className="text-[11px] text-muted-foreground">{t("No guide is available for this agent.", "此智能体没有可用指南。")}</p>}</CollapsibleContent>
      </Collapsible>
    </div>
  );
}
