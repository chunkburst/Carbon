import type { MouseEvent, ReactNode } from "react";
import {
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
import type { BoardPresentation } from "@/lib/personalization";
import { cn } from "@/lib/utils";

type BoardBackgroundContextMenuProps = {
  children: ReactNode;
  className?: string;
  presentation: BoardPresentation;
  onNewTask: () => void;
  onSearch: () => void;
  onRefresh: () => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
  onClearFilters?: () => void;
  onPresentationChange: (presentation: BoardPresentation) => void;
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
  presentation,
  onNewTask,
  onSearch,
  onRefresh,
  onExpandAll,
  onCollapseAll,
  onClearFilters,
  onPresentationChange,
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
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuLabel>{t("Card presentation", "卡片样式")}</ContextMenuLabel>
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
        </ContextMenuGroup>
      </ContextMenuContent>
    </ContextMenu>
  );
}
