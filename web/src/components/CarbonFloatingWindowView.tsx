import { useEffect, useMemo, useState } from "react";
import { CircleAlert, ExternalLink, Loader2, Radio, RefreshCw, X } from "lucide-react";
import { toast } from "sonner";
import { BrandLogo } from "@/components/BrandLogo";
import { CarbonAnimationBoard } from "@/components/CarbonAnimationBoard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { CarbonHome, CarbonScope } from "@/lib/carbon-api";
import { closeFloatingBoardWindow, floatingBoardTargetFromHash, focusMainTaskFromFloatingBoard, type FloatingBoardTarget } from "@/lib/desktop";
import { useI18n } from "@/lib/i18n";
import { PERSONALIZATION_EVENT, getAnimationBoardStyle, type AnimationBoardStyle } from "@/lib/personalization";
import { useCarbonHome, useCarbonTaskEvents, useCarbonTasks } from "@/lib/queries";
import { carbonStorageKey } from "@/lib/storage-identity";
import type { Status, Task } from "@/lib/api";

const CLOSED_STATES = new Set(["done", "closed", "completed", "cancelled", "canceled"]);

function workflowStatus(home: string, tasks: Task[]): Status {
  const defaults = ["backlog", "ready", "in_progress", "review", "done"];
  const states = [...new Set([...defaults, ...tasks.map((task) => task.status).filter(Boolean)])];
  return {
    initialized: true,
    root: home,
    suggestedPrefix: "CARBON",
    prefix: "CARBON",
    initial: "backlog",
    states,
    closed: states.filter((state) => CLOSED_STATES.has(state.toLowerCase())),
  };
}

function projectForTarget(home: CarbonHome | undefined, target: FloatingBoardTarget | null) {
  if (!home || !target) return undefined;
  if (target.clusterId) {
    return home.manifest?.clusters
      ?.find((cluster) => cluster.id === target.clusterId)
      ?.projects.find((project) => project.id === target.workspaceProjectId);
  }
  return home.manifest?.projects?.find((project) => project.id === target.workspaceProjectId);
}

function useFloatingTarget(): FloatingBoardTarget | null {
  const [target, setTarget] = useState<FloatingBoardTarget | null>(() => floatingBoardTargetFromHash());

  useEffect(() => {
    const sync = () => setTarget(floatingBoardTargetFromHash());
    window.addEventListener("hashchange", sync);
    return () => window.removeEventListener("hashchange", sync);
  }, []);

  return target;
}

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const sync = () => setReduced(media.matches);
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);

  return reduced;
}

function styleLabel(style: AnimationBoardStyle, language: "en" | "zh"): string {
  if (style === "market-kline") return language === "zh" ? "行情 K 线" : "Market K-line";
  return language === "zh" ? "工作风" : "Work floor";
}

function WindowNotice({
  title,
  message,
  closeLabel,
}: {
  title: string;
  message: string;
  closeLabel: string;
}) {
  return (
    <div className="flex h-screen items-center justify-center bg-[radial-gradient(circle_at_18%_0%,color-mix(in_oklch,var(--brand)_18%,transparent),transparent_38%),var(--background)] p-5 text-foreground">
      <section className="w-full max-w-sm rounded-2xl border bg-card/95 p-5 shadow-xl">
        <div className="mb-3 flex size-9 items-center justify-center rounded-xl bg-destructive/10 text-destructive">
          <CircleAlert className="size-4" />
        </div>
        <h1 className="text-sm font-semibold tracking-tight">{title}</h1>
        <p className="mt-1.5 text-sm leading-6 text-muted-foreground">{message}</p>
        <Button className="mt-5" size="sm" onClick={() => void closeFloatingBoardWindow()}>
          <X data-icon="inline-start" />
          {closeLabel}
        </Button>
      </section>
    </div>
  );
}

/**
 * The body of Tauri's native, always-on-top task window.  It independently discovers the
 * active Carbon Home, validates the opaque route against its manifest, then attaches its own
 * scoped task query + SSE stream.  It never trusts route data as a filesystem path or URL.
 */
export function CarbonFloatingWindowView() {
  const { language, t } = useI18n();
  const target = useFloatingTarget();
  const reducedMotion = useReducedMotion();
  const homeQuery = useCarbonHome();
  const homeResult = homeQuery.data;
  const home = homeResult?.available ? homeResult.data : undefined;
  const project = useMemo(() => projectForTarget(home, target), [home, target]);
  const scope = useMemo<CarbonScope | null>(() => {
    if (!home || !target || !project) return null;
    return {
      home: home.root,
      ...(target.clusterId ? { clusterId: target.clusterId } : {}),
      ...(target.projectId ? { projectId: target.projectId } : {}),
    };
  }, [home, project, target]);
  const taskQuery = useCarbonTasks(scope ?? {}, false, Boolean(scope), true);
  useCarbonTaskEvents(scope ?? {}, Boolean(scope));
  const tasks = useMemo(() => taskQuery.data?.available ? taskQuery.data.data.tasks ?? [] : [], [taskQuery.data]);
  const status = useMemo(() => workflowStatus(home?.root ?? "", tasks), [home?.root, tasks]);
  const [style, setStyle] = useState<AnimationBoardStyle>(getAnimationBoardStyle);
  const projectKey = useMemo(
    () => home && target
      ? carbonStorageKey({
        home: home.root,
        homeId: home.manifest?.id,
        clusterId: target.clusterId,
        projectId: target.workspaceProjectId,
      })
      : "",
    [home, target],
  );

  useEffect(() => {
    const syncStyle = () => setStyle(getAnimationBoardStyle());
    window.addEventListener(PERSONALIZATION_EVENT, syncStyle);
    window.addEventListener("storage", syncStyle);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncStyle);
      window.removeEventListener("storage", syncStyle);
    };
  }, []);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") void closeFloatingBoardWindow();
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, []);

  const openTask = async (task: Task) => {
    if (!target) return;
    const opened = await focusMainTaskFromFloatingBoard({ ...target, taskId: task.id });
    if (!opened) toast.error(t("Couldn't open this task in the main Carbon window.", "无法在 Carbon 主窗口中打开此任务。"));
  };

  if (!target) {
    return (
      <WindowNotice
        title={t("This floating board has no valid scope", "此悬浮看板没有有效范围")}
        message={t("Close it and open a project board from Carbon again.", "请关闭此窗口，然后从 Carbon 的项目看板重新打开。")}
        closeLabel={t("Close", "关闭")}
      />
    );
  }

  if (homeQuery.isLoading) {
    return (
      <div className="grid h-screen place-items-center bg-background text-muted-foreground">
        <Loader2 className="size-5 animate-spin" aria-label={t("Loading Carbon Home", "正在加载 Carbon 主目录")} />
      </div>
    );
  }

  if (!home || !home.initialized) {
    return (
      <WindowNotice
        title={t("Carbon Home is unavailable", "Carbon 主目录不可用")}
        message={t("The floating board could not reach this Carbon Home. Keep the main app open, then try again.", "悬浮看板无法连接当前 Carbon 主目录。请保持主程序运行后重试。")}
        closeLabel={t("Close", "关闭")}
      />
    );
  }

  if (!project || !scope) {
    return (
      <WindowNotice
        title={t("This project is no longer available", "此项目已不可用")}
        message={t("The project behind this floating board no longer belongs to the current Carbon Home.", "此悬浮看板对应的项目已不属于当前 Carbon 主目录。")}
        closeLabel={t("Close", "关闭")}
      />
    );
  }

  return (
    <div className="flex h-screen min-h-0 flex-col overflow-hidden bg-[radial-gradient(circle_at_12%_0%,color-mix(in_oklch,var(--brand)_13%,transparent),transparent_36%),var(--background)] text-foreground">
      <header className="flex shrink-0 items-center gap-2 border-b bg-card/86 px-3 py-2.5 supports-backdrop-filter:backdrop-blur-xl">
        <span className="grid size-8 shrink-0 place-items-center rounded-xl bg-brand/10 text-brand ring-1 ring-brand/15">
          <BrandLogo className="size-5" title="Carbon" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <p className="truncate text-sm font-semibold tracking-tight">{project.name}</p>
            <Badge variant="outline" className="h-5 max-w-32 truncate px-1.5 text-[10px]">
              {styleLabel(style, language)}
            </Badge>
          </div>
          <p className="flex items-center gap-1 truncate text-[11px] text-muted-foreground">
            <Radio className="size-3 text-emerald-500" />
            {t("Live project board", "项目实时看板")}
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t("Refresh tasks", "刷新任务")}
          title={t("Refresh tasks", "刷新任务")}
          onClick={() => { void taskQuery.refetch(); }}
        >
          <RefreshCw />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t("Close floating board", "关闭悬浮看板")}
          title={t("Close floating board", "关闭悬浮看板")}
          onClick={() => void closeFloatingBoardWindow()}
        >
          <X />
        </Button>
      </header>

      <div className="flex shrink-0 items-center justify-between gap-2 border-b bg-background/72 px-3 py-1.5 text-xs text-muted-foreground">
        <span className="truncate">
          {taskQuery.isFetching ? t("Syncing task activity…", "正在同步任务动态…") : t("Updates stay in sync with Carbon", "任务变化会与 Carbon 保持同步")}
        </span>
        <span className="shrink-0 tabular-nums">{tasks.length} {t("tasks", "个任务")}</span>
      </div>

      {taskQuery.data?.available === false ? (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
          <CircleAlert className="size-5 text-warning" />
          <p className="max-w-sm text-sm text-muted-foreground">
            {t("The current Carbon server does not provide task data for this floating board.", "当前 Carbon 服务未提供此悬浮看板所需的任务数据。")}
          </p>
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-hidden">
          <CarbonAnimationBoard
            projectKey={projectKey}
            tasks={tasks}
            status={status}
            style={style}
            onOpenTask={(task) => { void openTask(task); }}
            prefersReducedMotion={reducedMotion}
            compact
          />
        </div>
      )}

      <footer className="flex shrink-0 items-center justify-between gap-2 border-t bg-card/72 px-3 py-1.5 text-[11px] text-muted-foreground">
        <span className="truncate">{t("Select a task to open its detail in Carbon", "选择任务即可在 Carbon 主窗口中打开详情")}</span>
        <ExternalLink className="size-3 shrink-0" aria-hidden />
      </footer>
    </div>
  );
}
