import { useEffect, useMemo, useState } from "react";
import {
  Activity,
  CircleDashed,
  ClockAlert,
  FolderOpen,
  ListTodo,
  Moon,
  Network,
  Plus,
  ScanEye,
  Search,
  Sparkles,
  SquareKanban,
} from "lucide-react";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { StatusIcon } from "@/components/StatusIcon";
import { useCarbonSearch, useTasks } from "@/lib/queries";
import { toggleTheme } from "@/lib/theme";
import type { Filter } from "@/components/AppSidebar";
import type { Status } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

type PaletteTask = {
  id: string;
  title: string;
  status?: string;
  projectName?: string;
  projectPath?: string;
};

export function CommandPalette({
  path,
  status,
  onView,
  onOpenTask,
  onNewTask,
  onChangeCluster,
  onGraph,
  onBoard,
}: {
  path: string;
  status: Status;
  onView: (f: Filter) => void;
  onOpenTask: (id: string, taskPath?: string) => void;
  onNewTask: () => void;
  onChangeCluster: () => void;
  onGraph: () => void;
  onBoard: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data: tasks } = useTasks(path);
  // A legacy cluster root is a filesystem path, not a Carbon cluster ID.
  const carbonSearch = useCarbonSearch(path, query);
  const { t } = useI18n();
  const views: { key: Filter; label: string; icon: typeof ListTodo }[] = [
    { key: "all", label: t("All tasks", "所有任务"), icon: ListTodo },
    { key: "backlog", label: t("Backlog", "待办"), icon: CircleDashed },
    { key: "ready", label: t("Ready", "就绪"), icon: Sparkles },
    { key: "active", label: t("Active agent work", "进行中的智能体工作"), icon: Activity },
    { key: "stalled", label: t("Stalled agent work", "停滞的智能体工作"), icon: ClockAlert },
    { key: "review", label: t("Awaiting review", "等待审核"), icon: ScanEye },
  ];

  const results = useMemo<PaletteTask[]>(() => {
    if (carbonSearch.data?.available) {
      return (carbonSearch.data.data.results ?? []).map((result) => ({
        id: result.task.id,
        title: result.task.title,
        status: result.task.status,
        projectName: result.task.projectId,
      }));
    }
    const needle = query.trim().toLowerCase();
    return (tasks ?? [])
      .filter((task) => !needle || task.id.toLowerCase().includes(needle) || task.title.toLowerCase().includes(needle))
      .map((task) => ({ id: task.id, title: task.title, status: task.status }));
  }, [carbonSearch.data, query, tasks]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen((value) => !value);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const run = (fn: () => void) => {
    setOpen(false);
    fn();
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={setOpen}
      title={t("Global search", "全局搜索")}
      description={t("Search the current cluster or run a command.", "搜索当前集群或运行命令。")}
    >
      <Command>
        <CommandInput
          value={query}
          onValueChange={setQuery}
          placeholder={t("Search across the cluster or run a command…", "搜索整个集群或运行命令…")}
        />
        <CommandList>
          <CommandEmpty>{t("No results.", "没有结果。")}</CommandEmpty>

          <CommandGroup heading={t("Actions", "操作")}>
            <CommandItem onSelect={() => run(onNewTask)}><Plus /> {t("New task", "新建任务")}</CommandItem>
            <CommandItem onSelect={() => run(onBoard)}><SquareKanban /> {t("Board", "看板")}</CommandItem>
            <CommandItem onSelect={() => run(onGraph)}><Network /> {t("Dependency graph", "依赖关系图")}</CommandItem>
            <CommandItem onSelect={() => run(() => toggleTheme())}><Moon /> {t("Toggle theme", "切换主题")}</CommandItem>
            <CommandItem onSelect={() => run(onChangeCluster)}><FolderOpen /> {t("Switch project cluster…", "切换项目集群…")}</CommandItem>
          </CommandGroup>

          <CommandGroup heading={t("Views", "视图")}>
            {views.map(({ key, label, icon: Icon }) => (
              <CommandItem key={key} onSelect={() => run(() => onView(key))}><Icon /> {label}</CommandItem>
            ))}
          </CommandGroup>

          <CommandSeparator />
          <CommandGroup heading={carbonSearch.data?.available ? t("Cluster results", "集群结果") : t("This project", "当前项目")}>
            {results.map((task) => (
              <CommandItem key={`${task.projectPath ?? path}:${task.id}`} value={`${task.id} ${task.title} ${task.projectName ?? ""}`} onSelect={() => run(() => onOpenTask(task.id, task.projectPath))}>
                {task.status ? <StatusIcon status={task.status} closed={status.closed} initial={status.initial} /> : <Search />}
                <span className="font-mono text-xs text-muted-foreground">{task.id}</span>
                <span className="truncate">{task.title}</span>
                {task.projectName && <span className="ml-auto truncate text-xs text-muted-foreground">{task.projectName}</span>}
              </CommandItem>
            ))}
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
