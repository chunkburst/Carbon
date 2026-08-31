import { useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { Check, ChevronsUpDown, ClipboardCopy, ExternalLink, FolderKanban, Pencil, Settings2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { ProjectDeleteDialog, ProjectEditorDialog } from "@/components/ProjectManagerDialog";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuGroup,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { CatalogIcon, catalogIconAssetURL, catalogIconFor, type CatalogIconMutation, type CatalogIconToken, type CatalogPresentationIcons } from "@/components/CatalogIcon";
import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import { carbonProjectSearchText, carbonProjectSummary } from "@/lib/carbon-projects";
import { useI18n } from "@/lib/i18n";
import { useDeleteHomeProject } from "@/lib/queries";
import { cn } from "@/lib/utils";

type CarbonProjectSwitcherProps = {
  clusters: CarbonHomeCluster[];
  standaloneProjects?: CarbonHomeProject[];
  cluster?: CarbonHomeCluster;
  project: CarbonHomeProject;
  home?: string;
  presentation?: CatalogPresentationIcons;
  onSetIcon?: (input: CatalogIconMutation) => Promise<void>;
  catalogUpdatePending?: boolean;
  onApplyCatalogUpdate?: () => void;
  onSelectProject: (clusterId: string | undefined, projectId: string) => void;
  onManage: () => void;
};

export function CarbonProjectSwitcher({
  clusters,
  standaloneProjects = [],
  cluster,
  project,
  home,
  presentation,
  onSetIcon,
  catalogUpdatePending = false,
  onApplyCatalogUpdate,
  onSelectProject,
  onManage,
}: CarbonProjectSwitcherProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<{ project: CarbonHomeProject; clusterId?: string } | null>(null);
  const [deleteStage, setDeleteStage] = useState<1 | 2 | null>(null);
  const [deleteConfirmationName, setDeleteConfirmationName] = useState("");
  const [deleteData, setDeleteData] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [editorTarget, setEditorTarget] = useState<{ project: CarbonHomeProject; clusterId?: string } | null>(null);
  const openFrame = useRef<number | undefined>(undefined);
  const selectFrame = useRef<number | undefined>(undefined);
  const selectTimer = useRef<number | undefined>(undefined);
  const deleteProject = useDeleteHomeProject(home);
  const fullName = cluster ? `${cluster.name} / ${project.name}` : project.name;
  const selectedIcon = catalogIconFor(presentation, "project", project.id);

  useEffect(() => () => {
    if (openFrame.current !== undefined) window.cancelAnimationFrame(openFrame.current);
    if (selectFrame.current !== undefined) window.cancelAnimationFrame(selectFrame.current);
    if (selectTimer.current !== undefined) window.clearTimeout(selectTimer.current);
  }, []);

  const setPopoverOpen = (nextOpen: boolean) => {
    if (!nextOpen) {
      if (openFrame.current !== undefined) {
        window.cancelAnimationFrame(openFrame.current);
        openFrame.current = undefined;
      }
      setOpen(false);
      return;
    }
    if (catalogUpdatePending) {
      // Commit before the command list mounts; the one-frame deferral prevents a
      // newly created project from popping into an already-open list.
      onApplyCatalogUpdate?.();
      if (openFrame.current !== undefined) window.cancelAnimationFrame(openFrame.current);
      openFrame.current = window.requestAnimationFrame(() => {
        openFrame.current = undefined;
        setOpen(true);
      });
      return;
    }
    setOpen(true);
  };

  const selectProject = (clusterId: string | undefined, projectId: string) => {
    // Close first, then change the scoped data on the next frame. This prevents
    // React's event batching from keeping the old list visible while Home applies
    // catalog updates or starts the next project's queries.
    // Commit the portal close synchronously. The route change below can start a
    // relatively expensive scoped workspace render, so leaving this update in
    // React's event batch makes the picker look frozen until that render finishes.
    flushSync(() => setPopoverOpen(false));
    if (selectFrame.current !== undefined) window.cancelAnimationFrame(selectFrame.current);
    if (selectTimer.current !== undefined) window.clearTimeout(selectTimer.current);
    selectFrame.current = window.requestAnimationFrame(() => {
      selectFrame.current = undefined;
      // Queue navigation after the closed picker has reached a paint boundary.
      // requestAnimationFrame itself runs before paint, so the following task is
      // intentional rather than calling onSelectProject directly in this frame.
      selectTimer.current = window.setTimeout(() => {
        selectTimer.current = undefined;
        onSelectProject(clusterId, projectId);
      }, 0);
    });
  };
  const beginProjectEdit = (target: CarbonHomeProject, clusterId?: string) => {
    setOpen(false);
    setEditorTarget({ project: target, clusterId });
  };
  const beginProjectDelete = (target: CarbonHomeProject, clusterId?: string) => {
    setDeleteTarget({ project: target, clusterId });
    setDeleteStage(1);
    setDeleteConfirmationName("");
    setDeleteData(false);
    setDeleteError(null);
  };
  const closeProjectDelete = () => {
    if (deleteProject.isPending) return;
    setDeleteTarget(null);
    setDeleteStage(null);
    setDeleteConfirmationName("");
    setDeleteData(false);
    setDeleteError(null);
  };
  const confirmProjectDelete = async () => {
    if (!deleteTarget || deleteConfirmationName !== deleteTarget.project.name || deleteProject.isPending) return;
    setDeleteError(null);
    try {
      const result = await deleteProject.mutateAsync({
        // `clusterId` is cache invalidation metadata only. The project deletion
        // request remains project-targeted even when the item belongs to a cluster.
        clusterId: deleteTarget.clusterId,
        projectId: deleteTarget.project.id,
        name: deleteConfirmationName,
        deleteData,
      });
      if (!result.available) {
        setDeleteError(t("This Carbon version cannot delete projects. Update Carbon and try again.", "当前 Carbon 版本暂不支持删除项目，请更新后重试。"));
        return;
      }
      setDeleteTarget(null);
      setDeleteStage(null);
      setDeleteConfirmationName("");
      setDeleteData(false);
      setDeleteError(null);
      setOpen(false);
    } catch (cause) {
      setDeleteError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  return (
    <Popover open={open} onOpenChange={setPopoverOpen}>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <Button
              variant="ghost"
              className="h-auto min-w-0 max-w-[min(34rem,48vw)] justify-start gap-2 px-2 py-1.5 text-left"
              aria-label={t("Switch project: {name}", "切换项目：{name}", { name: fullName })}
            >
              <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-brand/12 text-brand">
                <CatalogIcon icon={selectedIcon} assetURL={catalogIconAssetURL(home, "project", project.id)} fallback={FolderKanban} />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block break-words text-sm font-semibold leading-tight">{project.name}</span>
                <span className="block break-words text-xs leading-tight text-muted-foreground">{cluster?.name ?? t("Independent project", "独立项目")}</span>
              </span>
              <span className="grid size-2 shrink-0 place-items-center" aria-hidden="true">
                <span className={cn("size-1.5 rounded-full bg-muted-foreground/70 transition-opacity duration-200 motion-reduce:transition-none", catalogUpdatePending ? "opacity-100" : "opacity-0")} />
              </span>
              <span className="sr-only" aria-live="polite">
                {catalogUpdatePending ? t("Catalog update available", "项目目录有更新") : ""}
              </span>
              <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
            </Button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent side="bottom">
          {catalogUpdatePending ? `${fullName} — ${t("Catalog update available", "项目目录有更新")}` : fullName}
        </TooltipContent>
      </Tooltip>

      <PopoverContent
        align="start"
        className="w-[min(32rem,calc(100vw-1rem))] gap-0 overflow-hidden rounded-2xl p-0 data-closed:hidden"
      >
        <Command>
          <CommandInput placeholder={t("Search projects or clusters…", "搜索项目或集群…")} />
          <CommandList className="max-h-[min(28rem,65vh)]">
            <CommandEmpty>{t("No matching project", "没有匹配的项目")}</CommandEmpty>
            {standaloneProjects.length > 0 && (
              <CommandGroup heading={t("Projects", "项目")}>
                {standaloneProjects.map((candidate) => {
                  const current = !cluster && candidate.id === project.id;
                  const summary = carbonProjectSummary(candidate, t);
                  const searchValue = [candidate.name, candidate.slug, candidate.description, candidate.kind].filter(Boolean).join(" ");
                  const icon = catalogIconFor(presentation, "project", candidate.id);
                  return (
                    <ProjectCommandItem
                      key={candidate.id}
                      candidate={candidate}
                      searchValue={searchValue}
                      summary={summary}
                      icon={icon}
                      home={home}
                      clusterId={undefined}
                      current={current}
                      onSelect={() => selectProject(undefined, candidate.id)}
                      onEdit={() => beginProjectEdit(candidate)}
                      onManage={() => {
                        setOpen(false);
                        onManage();
                      }}
                      onDelete={() => {
                        setOpen(false);
                        beginProjectDelete(candidate);
                      }}
                    />
                  );
                })}
              </CommandGroup>
            )}
            {clusters.map((item) => (
              <CommandGroup key={item.id} heading={item.name}>
                {item.projects.map((candidate) => {
                  const current = item.id === cluster?.id && candidate.id === project.id;
                  const summary = carbonProjectSummary(candidate, t);
                  const searchValue = carbonProjectSearchText(candidate, item);
                  const icon = catalogIconFor(presentation, "project", candidate.id);
                  return (
                    <ProjectCommandItem
                      key={candidate.id}
                      candidate={candidate}
                      searchValue={searchValue}
                      summary={summary}
                      icon={icon}
                      home={home}
                      clusterId={item.id}
                      current={current}
                      onSelect={() => selectProject(item.id, candidate.id)}
                      onEdit={() => beginProjectEdit(candidate, item.id)}
                      onManage={() => {
                        setOpen(false);
                        onManage();
                      }}
                      onDelete={() => {
                        setOpen(false);
                        beginProjectDelete(candidate, item.id);
                      }}
                    />
                  );
                })}
              </CommandGroup>
            ))}
            <CommandSeparator />
            <CommandGroup>
              <CommandItem
                value={t("Manage projects and clusters", "管理项目和集群")}
                onSelect={() => {
                  setOpen(false);
                  onManage();
                }}
              >
                <Settings2 />
                {t("Manage projects and clusters…", "管理项目和集群…")}
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
      {deleteTarget && (
        <ProjectDeleteDialog
          project={deleteTarget.project}
          stage={deleteStage}
          confirmationName={deleteConfirmationName}
          deleteData={deleteData}
          error={deleteError}
          pending={deleteProject.isPending}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) closeProjectDelete();
          }}
          onContinue={() => setDeleteStage(2)}
          onConfirmationNameChange={(value) => {
            setDeleteConfirmationName(value);
            setDeleteError(null);
          }}
          onDeleteDataChange={(value) => {
            setDeleteData(value);
            setDeleteError(null);
          }}
          onConfirm={() => void confirmProjectDelete()}
        />
      )}
      {editorTarget && (
        <ProjectEditorDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setEditorTarget(null);
          }}
          home={home ?? ""}
          clusters={clusters}
          initialClusterId={editorTarget.clusterId}
          project={editorTarget.project}
          icon={catalogIconFor(presentation, "project", editorTarget.project.id)}
          onSetIcon={onSetIcon}
          onCreated={(selection) => {
            selectProject(selection.clusterId, selection.projectId);
          }}
        />
      )}
    </Popover>
  );
}

async function copyText(value: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return;
    }
  } catch {
    // A WebView may expose the Clipboard API but still reject the write. Fall
    // through to the focus-safe legacy path before reporting a failure.
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Clipboard access is unavailable");
}

function projectLink(clusterId: string | undefined, projectId: string): string {
  const hash = clusterId
    ? `#carbon/${encodeURIComponent(clusterId)}/${encodeURIComponent(projectId)}`
    : `#carbon/project/${encodeURIComponent(projectId)}`;
  const url = new URL(window.location.href);
  url.hash = hash;
  return url.toString();
}

function ProjectCommandItem({
  candidate,
  searchValue,
  summary,
  icon,
  home,
  clusterId,
  current,
  onSelect,
  onEdit,
  onManage,
  onDelete,
}: {
  candidate: CarbonHomeProject;
  searchValue: string;
  summary: string;
  icon: CatalogIconToken | null;
  home?: string;
  clusterId?: string;
  current: boolean;
  onSelect: () => void;
  onEdit: () => void;
  onManage: () => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const copy = async (value: string, success: string) => {
    try {
      await copyText(value);
      toast.success(success);
    } catch {
      toast.error(t("Could not copy to the clipboard", "无法复制到剪贴板"));
    }
  };
  const sourcePath = candidate.source?.path?.trim();
  const link = projectLink(clusterId, candidate.id);
  return (
    <ContextMenu modal={false}>
      <ContextMenuTrigger asChild>
        <CommandItem
          data-carbon-context-surface
          value={searchValue}
          onSelect={onSelect}
          // cmdk observes pointer input for selection. Stop its right-button path
          // while letting the Radix contextmenu event reach this same trigger.
          onPointerDown={(event) => {
            if (event.button === 2) event.stopPropagation();
          }}
          className="items-start gap-3 py-2.5"
        >
          <span className={cn("mt-0.5 grid size-7 shrink-0 place-items-center overflow-hidden rounded-lg border bg-panel text-muted-foreground", current && "border-brand/40 bg-brand/10 text-brand")}>
            <CatalogIcon icon={icon} assetURL={catalogIconAssetURL(home, "project", candidate.id)} fallback={FolderKanban} />
          </span>
          <span className="min-w-0 flex-1">
            <span title={candidate.name} className="block break-words font-medium leading-snug">{candidate.name}</span>
            <span className="mt-0.5 block break-words text-xs leading-snug text-muted-foreground">{summary}</span>
          </span>
          {current && <Check className="mt-1 size-4 shrink-0 text-brand" />}
        </CommandItem>
      </ContextMenuTrigger>
      <ContextMenuContent className="min-w-56">
        <ContextMenuLabel className="max-w-64 truncate">{candidate.name}</ContextMenuLabel>
        <ContextMenuGroup>
          <ContextMenuItem
            onSelect={(event) => {
              event.stopPropagation();
              onSelect();
            }}
          >
            <ExternalLink />
            {t("Open project", "打开项目")}
          </ContextMenuItem>
          <ContextMenuItem
            onSelect={(event) => {
              event.stopPropagation();
              onEdit();
            }}
          >
            <Pencil />
            {t("Edit project", "编辑项目")}
          </ContextMenuItem>
        </ContextMenuGroup>
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuSub>
            <ContextMenuSubTrigger className="gap-2">
              <ClipboardCopy />
              {t("Copy", "复制")}
            </ContextMenuSubTrigger>
            <ContextMenuSubContent className="min-w-52">
              <ContextMenuGroup>
                <ContextMenuItem
                  onSelect={(event) => {
                    event.stopPropagation();
                    void copy(link, t("Project link copied", "项目链接已复制"));
                  }}
                >
                  <ClipboardCopy />
                  {t("Copy project link", "复制项目链接")}
                </ContextMenuItem>
                <ContextMenuItem
                  onSelect={(event) => {
                    event.stopPropagation();
                    void copy(candidate.id, t("Project ID copied", "项目 ID 已复制"));
                  }}
                >
                  <ClipboardCopy />
                  {t("Copy project ID", "复制项目 ID")}
                </ContextMenuItem>
                <ContextMenuItem
                  disabled={!sourcePath}
                  onSelect={(event) => {
                    event.stopPropagation();
                    if (sourcePath) void copy(sourcePath, t("Source path copied", "源码路径已复制"));
                  }}
                >
                  <ClipboardCopy />
                  {t("Copy source path", "复制源码路径")}
                </ContextMenuItem>
              </ContextMenuGroup>
            </ContextMenuSubContent>
          </ContextMenuSub>
        </ContextMenuGroup>
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuItem
            onSelect={(event) => {
              event.stopPropagation();
              onManage();
            }}
          >
            <Settings2 />
            {t("Open project management", "打开项目管理")}
          </ContextMenuItem>
        </ContextMenuGroup>
        <ContextMenuSeparator />
        <ContextMenuGroup>
          <ContextMenuItem
            variant="destructive"
            onSelect={(event) => {
              event.stopPropagation();
              onDelete();
            }}
          >
            <Trash2 />
            {t("Delete project", "删除项目")}
          </ContextMenuItem>
        </ContextMenuGroup>
      </ContextMenuContent>
    </ContextMenu>
  );
}
