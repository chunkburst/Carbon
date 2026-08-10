import { useCallback, useEffect, useRef, useState } from "react";
import { Check, ChevronsUpDown, FolderKanban, Settings2 } from "lucide-react";
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
import { CatalogIcon, catalogIconAssetURL, catalogIconFor, type CatalogPresentationIcons } from "@/components/CatalogIcon";
import type { CarbonHomeCluster, CarbonHomeProject } from "@/lib/carbon-api";
import { carbonProjectSearchText, carbonProjectSummary } from "@/lib/carbon-projects";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type CarbonProjectSwitcherProps = {
  clusters: CarbonHomeCluster[];
  standaloneProjects?: CarbonHomeProject[];
  cluster?: CarbonHomeCluster;
  project: CarbonHomeProject;
  home?: string;
  presentation?: CatalogPresentationIcons;
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
  catalogUpdatePending = false,
  onApplyCatalogUpdate,
  onSelectProject,
  onManage,
}: CarbonProjectSwitcherProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const openFrame = useRef<number | undefined>(undefined);
  const fullName = cluster ? `${cluster.name} / ${project.name}` : project.name;
  const selectedIcon = catalogIconFor(presentation, "project", project.id);

  useEffect(() => () => {
    if (openFrame.current !== undefined) window.cancelAnimationFrame(openFrame.current);
  }, []);

  const setPopoverOpen = useCallback((nextOpen: boolean) => {
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
  }, [catalogUpdatePending, onApplyCatalogUpdate]);

  const selectProject = (clusterId: string | undefined, projectId: string) => {
    onSelectProject(clusterId, projectId);
    setOpen(false);
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
        className="w-[min(32rem,calc(100vw-1rem))] gap-0 overflow-hidden rounded-2xl p-0"
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
                    <CommandItem key={candidate.id} value={searchValue} onSelect={() => selectProject(undefined, candidate.id)} className="items-start gap-3 py-2.5">
                      <span className={cn("mt-0.5 grid size-7 shrink-0 place-items-center overflow-hidden rounded-lg border bg-panel text-muted-foreground", current && "border-brand/40 bg-brand/10 text-brand")}>
                        <CatalogIcon icon={icon} assetURL={catalogIconAssetURL(home, "project", candidate.id)} fallback={FolderKanban} />
                      </span>
                      <span className="min-w-0 flex-1"><span title={candidate.name} className="block break-words font-medium leading-snug">{candidate.name}</span><span className="mt-0.5 block break-words text-xs leading-snug text-muted-foreground">{summary}</span></span>
                      {current && <Check className="mt-1 size-4 shrink-0 text-brand" />}
                    </CommandItem>
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
                    <CommandItem
                      key={candidate.id}
                      value={searchValue}
                      onSelect={() => selectProject(item.id, candidate.id)}
                      className="items-start gap-3 py-2.5"
                    >
                      <span
                        className={cn(
                          "mt-0.5 grid size-7 shrink-0 place-items-center rounded-lg border bg-panel text-muted-foreground",
                          current && "border-brand/40 bg-brand/10 text-brand",
                        )}
                      >
                        <CatalogIcon icon={icon} assetURL={catalogIconAssetURL(home, "project", candidate.id)} fallback={FolderKanban} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span title={candidate.name} className="block break-words font-medium leading-snug">
                          {candidate.name}
                        </span>
                        <span className="mt-0.5 block break-words text-xs leading-snug text-muted-foreground">
                          {summary}
                        </span>
                      </span>
                      {current && <Check className="mt-1 size-4 shrink-0 text-brand" />}
                    </CommandItem>
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
    </Popover>
  );
}
