import type { MouseEvent, ReactNode } from "react";
import {
  Activity,
  Check,
  FilePlus2,
  KanbanSquare,
  ListChecks,
  Network,
  RefreshCw,
  Search,
  Settings2,
  Trash2,
  UsersRound,
} from "lucide-react";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export type WorkspaceContextView = "board" | "graph" | "workers" | "work-logs" | "owner-logs" | "trash";

type WorkspaceBackgroundContextMenuProps = {
  children: ReactNode;
  className?: string;
  activeView: WorkspaceContextView;
  projectName: string;
  onNewTask: () => void;
  onSearch: () => void;
  onRefresh: () => void;
  onNavigate: (view: WorkspaceContextView) => void;
  onSettings: () => void;
};

const VIEW_ITEMS: Array<{
  id: WorkspaceContextView;
  label: [string, string];
  icon: typeof KanbanSquare;
}> = [
  { id: "board", label: ["Board", "看板"], icon: KanbanSquare },
  { id: "graph", label: ["Dependency graph", "依赖图"], icon: Network },
  { id: "workers", label: ["Workers", "工作者"], icon: UsersRound },
  { id: "work-logs", label: ["Work Logs", "工作日志"], icon: ListChecks },
  { id: "owner-logs", label: ["Owner Logs", "所有者日志"], icon: Activity },
  { id: "trash", label: ["Trash", "回收站"], icon: Trash2 },
];

/**
 * App-wide background actions for every Carbon workspace surface. Nested task,
 * board, and graph menus mark themselves with `data-carbon-context-surface` so
 * their object-specific actions win over this fallback menu.
 */
export function WorkspaceBackgroundContextMenu({
  children,
  className,
  activeView,
  projectName,
  onNewTask,
  onSearch,
  onRefresh,
  onNavigate,
  onSettings,
}: WorkspaceBackgroundContextMenuProps) {
  const { t } = useI18n();
  const ignoreNestedSurface = (event: MouseEvent<HTMLDivElement>) => {
    const target = event.target;
    if (target instanceof Element && target.closest("[data-carbon-context-surface]")) {
      event.preventDefault();
    }
  };
  const preserveNativeTextMenu = (event: MouseEvent<HTMLDivElement>) => {
    const target = event.target;
    if (
      target instanceof Element
      && target.closest('input, textarea, [contenteditable="true"], [role="textbox"]')
    ) {
      // Keep the platform spelling/copy/paste menu inside editors and inputs.
      event.stopPropagation();
    }
  };

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div
          className={cn("min-h-0", className)}
          onContextMenuCapture={preserveNativeTextMenu}
          onContextMenu={ignoreNestedSurface}
        >
          {children}
        </div>
      </ContextMenuTrigger>
      <ContextMenuContent className="min-w-56">
        <ContextMenuLabel className="max-w-52 truncate">{projectName}</ContextMenuLabel>
        <ContextMenuGroup>
          <ContextMenuItem onSelect={onNewTask}>
            <FilePlus2 />
            {t("New task", "新建任务")}
            <ContextMenuShortcut>C</ContextMenuShortcut>
          </ContextMenuItem>
          <ContextMenuItem onSelect={onSearch}>
            <Search />
            {t("Search tasks", "搜索任务")}
            <ContextMenuShortcut>/</ContextMenuShortcut>
          </ContextMenuItem>
          <ContextMenuItem onSelect={onRefresh}>
            <RefreshCw />
            {t("Refresh workspace data", "刷新工作区数据")}
          </ContextMenuItem>
        </ContextMenuGroup>
        <ContextMenuSeparator />
        <ContextMenuSub>
          <ContextMenuSubTrigger>
            <KanbanSquare />
            {t("Go to view", "切换视图")}
          </ContextMenuSubTrigger>
          <ContextMenuSubContent className="min-w-48">
            {VIEW_ITEMS.map(({ id, label, icon: Icon }) => (
              <ContextMenuItem key={id} onSelect={() => onNavigate(id)}>
                <Icon />
                {t(label[0], label[1])}
                {activeView === id && <Check className="ml-auto" />}
              </ContextMenuItem>
            ))}
          </ContextMenuSubContent>
        </ContextMenuSub>
        <ContextMenuSeparator />
        <ContextMenuItem onSelect={onSettings}>
          <Settings2 />
          {t("Settings", "设置")}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
}
