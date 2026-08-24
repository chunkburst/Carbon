import type { MouseEvent, ReactNode } from "react";
import {
  Bot,
  CandlestickChart,
  Check,
  ChevronsDown,
  ChevronsUp,
  FilePlus2,
  FilterX,
  PanelsTopLeft,
  RefreshCw,
  Rows3,
  Search,
} from "lucide-react";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { useI18n } from "@/lib/i18n";
import type { AnimationBoardStyle, BoardPresentation, TaskListPresentation } from "@/lib/personalization";
import { cn } from "@/lib/utils";

type BoardBackgroundContextMenuProps = {
  children: ReactNode;
  className?: string;
  surface: "tasks" | "agent-work" | "board";
  presentation: BoardPresentation;
  animationStyle: AnimationBoardStyle;
  onNewTask: () => void;
  onSearch: () => void;
  onRefresh: () => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
  onClearFilters?: () => void;
  onPresentationChange: (presentation: TaskListPresentation) => void;
  onAnimationStyleChange: (style: AnimationBoardStyle) => void;
};

/**
 * A board-level menu is intentionally attached to the list canvas, not every
 * task. Task rows/cards carry `data-carbon-task-surface`; their own Radix menu
 * receives the event first, and this trigger then opts out so mouse and
 * Shift+F10 task menus retain their exact task context.
 */
export function BoardBackgroundContextMenu({
  children,
  className,
  surface,
  presentation,
  animationStyle,
  onNewTask,
  onSearch,
  onRefresh,
  onExpandAll,
  onCollapseAll,
  onClearFilters,
  onPresentationChange,
  onAnimationStyleChange,
}: BoardBackgroundContextMenuProps) {
  const { t } = useI18n();
  const ignoreTaskSurface = (event: MouseEvent<HTMLDivElement>) => {
    const target = event.target;
    if (target instanceof Element && target.closest("[data-carbon-task-surface]")) {
      // Radix composes consumer handlers before its trigger handler. Preventing
      // default here leaves the already-handled nested task menu intact while
      // suppressing only this background menu.
      event.preventDefault();
    }
  };

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div
          data-carbon-context-surface
          className={cn("min-h-0", className)}
          onContextMenu={ignoreTaskSurface}
        >
          {children}
        </div>
      </ContextMenuTrigger>
      <ContextMenuContent>
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
            {t("Refresh tasks", "刷新任务")}
          </ContextMenuItem>
          {onClearFilters && (
            <ContextMenuItem onSelect={onClearFilters}>
              <FilterX />
              {t("Clear filters", "清除筛选")}
            </ContextMenuItem>
          )}
        </ContextMenuGroup>
        {surface !== "board" && (
          <>
            <ContextMenuSeparator />
            <ContextMenuGroup>
              <ContextMenuItem onSelect={onExpandAll}>
                <ChevronsDown />
                {t("Expand all sections", "展开全部分组")}
              </ContextMenuItem>
              <ContextMenuItem onSelect={onCollapseAll}>
                <ChevronsUp />
                {t("Collapse all sections", "收起全部分组")}
              </ContextMenuItem>
            </ContextMenuGroup>
          </>
        )}
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuLabel>{surface === "board" ? t("Board style", "看板风格") : t("Task presentation", "任务展示")}</ContextMenuLabel>
          {surface === "board" ? (
            <>
              <ContextMenuItem onSelect={() => onAnimationStyleChange("pixel-agents")}>
                <Bot />
                {t("Work floor", "工作风")}
                {animationStyle === "pixel-agents" && <Check className="ml-auto" />}
              </ContextMenuItem>
              <ContextMenuItem onSelect={() => onAnimationStyleChange("market-kline")}>
                <CandlestickChart />
                {t("Task K-line", "任务 K 线")}
                {animationStyle === "market-kline" && <Check className="ml-auto" />}
              </ContextMenuItem>
            </>
          ) : (
            <>
              <ContextMenuItem onSelect={() => onPresentationChange("row")}>
                <Rows3 />
                {t("Row mode", "行模式")}
                {presentation === "row" && <Check className="ml-auto" />}
              </ContextMenuItem>
              <ContextMenuItem onSelect={() => onPresentationChange("card")}>
                <PanelsTopLeft />
                {t("Card mode", "卡片模式")}
                {presentation === "card" && <Check className="ml-auto" />}
              </ContextMenuItem>
            </>
          )}
        </ContextMenuGroup>
      </ContextMenuContent>
    </ContextMenu>
  );
}
