import {
  Activity,
  Check,
  ChevronsUpDown,
  CircleDashed,
  ClockAlert,
  FolderOpen,
  FolderPlus,
  HelpCircle,
  ListTodo,
  Moon,
  Network,
  Pencil,
  PenSquare,
  Plug,
  ScanEye,
  Sparkles,
  SquareKanban,
  Sun,
} from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { getTheme, toggleTheme, type Theme } from "@/lib/theme";
import { NotificationBell } from "@/components/NotificationBell";
import { HelpDialog } from "@/components/HelpDialog";
import { Assignee } from "@/components/Assignee";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useIdentity, displayName } from "@/lib/identity";
import { useTasks } from "@/lib/queries";
import type { Cluster, ClusterProject, Status } from "@/lib/api";
import { workspaceBasename } from "@/lib/workspaces";
import { useI18n } from "@/lib/i18n";

export type Filter = "all" | "active" | "stalled" | "review" | "backlog" | "ready";

export function AppSidebar({
  path,
  status,
  cluster,
  currentProject,
  active,
  graphActive,
  boardActive,
  connectActive,
  onFilter,
  onGraph,
  onBoard,
  onConnect,
  onSwitchCluster,
  onAddProject,
  onSelectProject,
  onNewTask,
  onOpenTask,
  onOpenSettings,
}: {
  path: string;
  status: Status;
  cluster: Cluster | null;
  currentProject: ClusterProject | null;
  active: Filter | null;
  graphActive: boolean;
  boardActive: boolean;
  connectActive: boolean;
  onFilter: (filter: Filter) => void;
  onGraph: () => void;
  onBoard: () => void;
  onConnect: () => void;
  onSwitchCluster: () => void;
  onAddProject: () => void;
  onSelectProject: (project: ClusterProject) => void;
  onNewTask: () => void;
  onOpenTask: (id: string) => void;
  onOpenSettings: () => void;
}) {
  const [theme, setTheme] = useState<Theme>(getTheme());
  const [helpOpen, setHelpOpen] = useState(false);
  const { actor, setName } = useIdentity(status.suggestedActor);
  const { data: tasks } = useTasks(path);
  const { t } = useI18n();
  const taskNav: { key: Filter; label: string; icon: typeof ListTodo }[] = [
    { key: "all", label: t("All tasks", "所有任务"), icon: ListTodo },
    { key: "backlog", label: t("Backlog", "待办"), icon: CircleDashed },
    { key: "ready", label: t("Ready", "就绪"), icon: Sparkles },
  ];
  const sessionNav: { key: Filter; label: string; icon: typeof ListTodo }[] = [
    { key: "active", label: t("Active", "进行中"), icon: Activity },
    { key: "stalled", label: t("Stagnant", "停滞"), icon: ClockAlert },
    { key: "review", label: t("Awaiting review", "等待审核"), icon: ScanEye },
  ];
  const folderName = currentProject?.name || workspaceBasename(status.root);
  const clusterName = cluster?.name || t("Project cluster", "项目集群");
  const counts: Partial<Record<Filter, number>> = {
    active: tasks?.filter((task) => task.executionState === "active").length,
    stalled: tasks?.filter((task) => task.activityHealth === "stagnant").length,
    review: tasks?.filter((task) => task.executionState === "awaiting_review").length,
  };

  return (
    <aside className="flex w-[15rem] shrink-0 flex-col">
      <div className="flex items-center gap-1 px-2 py-2.5">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              title={`${clusterName} — ${folderName}`}
              className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-foreground/5"
            >
              <span className="grid size-5 shrink-0 place-items-center rounded bg-primary text-[10px] font-semibold text-primary-foreground">
                {clusterName.slice(0, 1).toUpperCase() || "C"}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block break-words text-[13px] font-medium leading-tight">{clusterName}</span>
                <span className="block break-words text-[11px] leading-tight text-muted-foreground">{folderName}</span>
              </span>
              <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="start"
            className="max-h-[min(32rem,calc(100vh-1rem))] w-[min(22rem,calc(100vw-1rem))] overflow-y-auto"
          >
            <DropdownMenuLabel
              title={cluster?.root || status.root}
              className="break-words font-mono text-xs font-normal leading-relaxed text-muted-foreground"
            >
              {cluster?.root || status.root}
            </DropdownMenuLabel>
            {cluster && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuGroup>
                  <DropdownMenuLabel>{t("Projects", "项目")}</DropdownMenuLabel>
                  {cluster.projects.map((project) => (
                    <ProjectMenuItem
                      key={`${project.id}:${project.path}`}
                      project={project}
                      current={project.path === path}
                      onSelect={() => onSelectProject(project)}
                    />
                  ))}
                </DropdownMenuGroup>
              </>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              {cluster && (
                <DropdownMenuItem onClick={onAddProject}>
                  <FolderPlus data-icon="inline-start" />
                  {t("Add project…", "添加项目…")}
                </DropdownMenuItem>
              )}
              {cluster && (
                <DropdownMenuItem onClick={onSwitchCluster}>
                  <FolderOpen data-icon="inline-start" />
                  {t("Switch project cluster…", "切换项目集群…")}
                </DropdownMenuItem>
              )}
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem onClick={onConnect}>
                <Plug data-icon="inline-start" />
                {t("Connect an agent…", "连接智能体…")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onOpenSettings}>
                {t("Settings…", "设置…")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setTheme(toggleTheme())}>
                {theme === "dark" ? t("Light theme", "浅色主题") : t("Dark theme", "深色主题")}
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <button
          onClick={onNewTask}
          aria-label={t("New task", "新建任务")}
          className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
        >
          <PenSquare className="size-4" />
        </button>
        <NotificationBell path={path} actor={actor} onOpenTask={onOpenTask} />
      </div>

      <nav className="flex-1 space-y-px px-2">
        {taskNav.map(({ key, label, icon: Icon }) => (
          <button
            key={key}
            onClick={() => onFilter(key)}
            aria-current={active === key ? "page" : undefined}
            className={cn(
              "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
              active === key
                ? "bg-foreground/[0.07] font-medium text-foreground"
                : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
            )}
          >
            <Icon className="size-4 shrink-0" />
            {label}
          </button>
        ))}

        <div className="my-1.5 border-t" />
        <p className="px-2 pb-1 pt-1.5 text-[11px] font-medium text-muted-foreground">
          {t("Agent work", "智能体工作")}
        </p>
        {sessionNav.map(({ key, label, icon: Icon }) => (
          <button
            key={key}
            onClick={() => onFilter(key)}
            aria-current={active === key ? "page" : undefined}
            className={cn(
              "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
              active === key
                ? "bg-foreground/[0.07] font-medium text-foreground"
                : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
            )}
          >
            <Icon className="size-4 shrink-0" />
            <span>{label}</span>
            {!!counts[key] && (
              <span className="ml-auto text-xs tabular-nums text-muted-foreground">{counts[key]}</span>
            )}
          </button>
        ))}

        <div className="my-1.5 border-t" />
        <button
          onClick={onBoard}
          aria-current={boardActive ? "page" : undefined}
          className={cn(
            "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
            boardActive
              ? "bg-foreground/[0.07] font-medium text-foreground"
              : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
          )}
        >
          <SquareKanban className="size-4 shrink-0" />
          {t("Board", "看板")}
        </button>
        <button
          onClick={onGraph}
          aria-current={graphActive ? "page" : undefined}
          className={cn(
            "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
            graphActive
              ? "bg-foreground/[0.07] font-medium text-foreground"
              : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
          )}
        >
          <Network className="size-4 shrink-0" />
          {t("Graph", "关系图")}
        </button>
        <button
          onClick={onConnect}
          aria-current={connectActive ? "page" : undefined}
          className={cn(
            "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
            connectActive
              ? "bg-foreground/[0.07] font-medium text-foreground"
              : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
          )}
        >
          <Plug className="size-4 shrink-0" />
          {t("Connect", "连接")}
        </button>
      </nav>

      <div className="flex flex-col gap-1 px-3 py-2">
        <IdentityChip key={actor} actor={actor} onRename={setName} />
        <div className="flex items-center justify-between">
          <span className="truncate text-xs text-muted-foreground">{folderName}</span>
          <div className="flex items-center gap-0.5">
            <button
              onClick={() => setHelpOpen(true)}
              aria-label={t("How Carbon works", "Carbon 使用说明")}
              className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
            >
              <HelpCircle className="size-4" />
            </button>
            <button
              onClick={() => setTheme(toggleTheme())}
              aria-label={t("Toggle theme", "切换主题")}
              className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
            >
              {theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
            </button>
          </div>
        </div>
      </div>
      <HelpDialog open={helpOpen} onOpenChange={setHelpOpen} />
    </aside>
  );
}

function ProjectMenuItem({
  project,
  current,
  onSelect,
}: {
  project: ClusterProject;
  current: boolean;
  onSelect: () => void;
}) {
  const { t } = useI18n();
  const details = [
    project.offline ? { label: t("Offline", "离线"), variant: "destructive" as const } : null,
    !project.initialized
      ? { label: t("Not initialized", "未初始化"), variant: "outline" as const }
      : null,
    project.active ? { label: `${project.active} ${t("active", "进行中")}`, variant: "secondary" as const } : null,
    project.stagnant
      ? { label: `${project.stagnant} ${t("stagnant", "停滞")}`, variant: "destructive" as const }
      : null,
    project.stalled
      ? { label: `${project.stalled} ${t("unresponsive sessions", "会话无响应")}`, variant: "outline" as const }
      : null,
    project.review ? { label: `${project.review} ${t("review", "待审核")}`, variant: "secondary" as const } : null,
    project.liveAgents
      ? { label: `${project.liveAgents} ${t("agents", "智能体")}`, variant: "secondary" as const }
      : null,
  ].filter((detail): detail is NonNullable<typeof detail> => detail !== null);

  if (details.length === 0) {
    details.push({ label: `${project.tasks} ${t("tasks", "任务")}`, variant: "outline" });
  }

  return (
    <DropdownMenuItem disabled={project.offline} onClick={onSelect} className="items-start gap-2 py-2">
      <FolderOpen data-icon="inline-start" className="mt-0.5 text-muted-foreground" />
      <span className="min-w-0 flex-1">
        <span className="flex items-start gap-2">
          <span title={project.name} className="min-w-0 break-words font-medium leading-snug">
            {project.name}
          </span>
          {current && <Check data-icon="inline-end" className="ml-auto text-brand" />}
        </span>
        <span title={project.path} className="mt-0.5 block break-words font-mono text-[11px] leading-relaxed text-muted-foreground">
          {workspaceBasename(project.path)}
        </span>
        <span className="mt-1 flex flex-wrap gap-1">
          {details.map((detail) => (
            <Badge key={detail.label} variant={detail.variant}>
              {detail.label}
            </Badge>
          ))}
        </span>
      </span>
    </DropdownMenuItem>
  );
}

function IdentityChip({ actor, onRename }: { actor: string; onRename: (name: string) => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(displayName(actor));
  const { t } = useI18n();

  const save = () => {
    const nextName = name.trim();
    if (nextName) onRename(nextName);
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button className="group flex items-center gap-2 rounded-md px-1 py-1 text-left hover:bg-foreground/5">
          <Assignee actor={actor || "human:?"} />
          <span className="min-w-0 flex-1 truncate text-[13px]">
            {displayName(actor) || t("Set your name", "设置你的名称")}
          </span>
          <Pencil className="size-3 text-muted-foreground opacity-0 group-hover:opacity-100" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-60">
        <p className="mb-2 text-xs text-muted-foreground">
          {t("Your name — stamped on everything you do here.", "你的名称会标记在这里的所有操作上。")}
        </p>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            save();
          }}
          className="flex gap-2"
        >
          <Input
            autoFocus
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t("e.g. shahram", "例如：shahram")}
            className="h-8 text-sm"
          />
          <Button type="submit" size="sm" className="h-8">
            {t("Save", "保存")}
          </Button>
        </form>
      </PopoverContent>
    </Popover>
  );
}
