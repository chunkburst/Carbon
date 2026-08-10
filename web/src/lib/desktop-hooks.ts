// React hooks bridging the desktop shell to the UI. All no-op in the browser (the
// underlying calls in lib/desktop.ts are isTauri()-guarded).
import { useEffect, useMemo, useRef, useState } from "react";
import { useSessions, useTasks } from "@/lib/queries";
import { useWorkspaces } from "@/lib/workspaces";
import { timeAgo } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import type { ClusterProject } from "@/lib/api";
import {
  dndEnabled,
  onDeepLink,
  onDesktopEvent,
  onTrayEvent,
  openDeepLink,
  setDnd,
  updateTray,
  type DesktopEvent,
  type TrayItem,
  type TrayMenuModel,
} from "@/lib/desktop";

// useDeepLinks routes carbon:// opens (from the OS) to the right task/project.
export function useDeepLinks() {
  useEffect(() => {
    let off = () => {};
    void onDeepLink(openDeepLink).then((fn) => {
      off = fn;
    });
    return () => off();
  }, []);
}

export type TrayHandlers = {
  openTask: (id: string) => void;
  openFilter: (filter: string) => void;
  switchProject: (slug: string) => void;
  newTask: () => void;
  openSettings: () => void;
};

const trunc = (s: string, n = 42) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);
const shortActor = (a?: string) => (a || "").replace(/^(agent|human):/, "");

// useTrayMenu pushes a live menu model (status counts + awaiting-review tasks + active agent
// sessions + actions) to the native tray, debounced/diffed, and dispatches tray clicks. The
// hidden window keeps SSE alive, so this stays current even when the app isn't visible.
export function useTrayMenu(path: string, projects: ClusterProject[], handlers: TrayHandlers) {
  const { data: tasks } = useTasks(path);
  const { data: sessions } = useSessions(path);
  const { language, t } = useI18n();
  const registry = useWorkspaces();
  const [dnd, setDndState] = useState(dndEnabled());
  const lastJson = useRef("");
  const hRef = useRef(handlers);
  useEffect(() => {
    hRef.current = handlers;
  }, [handlers]);

  // Keep native project switching scoped to the selected cluster. Historical local
  // workspaces are still available for legacy URLs, but must not leak into this menu.
  const workspaces = useMemo(
    () =>
      projects.flatMap((project) => {
        const workspace = registry.find((entry) => entry.path === project.path);
        return workspace ? [{ ...workspace, id: project.id, name: project.name }] : [];
      }),
    [projects, registry],
  );
  const currentSlug = useMemo(
    () => workspaces.find((w) => w.path === path)?.slug,
    [workspaces, path],
  );

  useEffect(() => {
    if (!tasks) return;
    const review = tasks.filter((t) => t.executionState === "awaiting_review");
    const active = tasks.filter((t) => t.executionState === "active");
    const stalled = tasks.filter((t) => t.executionState === "stalled");
    const ready = tasks.filter((t) => t.ready && !t.executionState).length;

    const counts: TrayItem[] = [];
    if (review.length) counts.push({ id: "filter:review", label: `● ${review.length} ${t("Awaiting review", "等待审核")}` });
    if (active.length) counts.push({ id: "filter:active", label: `▶ ${active.length} ${t("Active", "进行中")}` });
    if (stalled.length) counts.push({ id: "filter:stalled", label: `■ ${stalled.length} ${t("Stalled", "已停滞")}` });
    if (ready) counts.push({ id: "filter:ready", label: `✦ ${ready} ${t("Ready", "就绪")}` });
    if (!counts.length) counts.push({ id: "noop", label: t("No active work", "没有进行中的工作"), enabled: false });

    const sections: TrayItem[][] = [counts];

    if (review.length) {
      const sec: TrayItem[] = [{ id: "hdr:review", label: t("Awaiting review", "等待审核"), enabled: false }];
      review.slice(0, 5).forEach((t) => sec.push({ id: `task:${t.id}`, label: trunc(`${t.id}  ${t.title}`) }));
      sections.push(sec);
    }

    const live = (sessions ?? []).filter((s) => s.health === "active" || s.health === "stalled");
    if (live.length) {
      const sec: TrayItem[] = [{ id: "hdr:agents", label: t("Active agents", "活跃智能体"), enabled: false }];
      live.slice(0, 5).forEach((s) => {
        const prog = s.live?.progress || (s.health === "stalled" ? t("stalled", "已停滞") : t("working", "工作中"));
        const when = s.live?.heartbeatAt ? ` · ${timeAgo(s.live.heartbeatAt)}` : "";
        const flag = s.health === "stalled" ? "⚠ " : "";
        sec.push({ id: `task:${s.task}`, label: trunc(`${flag}${shortActor(s.actor)} · ${prog}${when}`, 46) });
      });
      sections.push(sec);
    }

    const actions: TrayItem[] = [{ id: "new_task", label: t("New task…", "新建任务…") }];
    if (workspaces.length > 1) {
      actions.push({ id: "hdr:projects", label: t("Projects", "项目"), enabled: false });
      workspaces.forEach((w) =>
        actions.push({ id: `project:${w.slug}`, label: w.name, checked: w.slug === currentSlug }),
      );
    }
    actions.push({ id: "toggle:dnd", label: t("Do Not Disturb", "勿扰模式"), checked: dnd });
    sections.push(actions);

    sections.push([
      { id: "tray_open", label: t("Open Carbon", "打开 Carbon") },
      { id: "settings", label: t("Settings…", "设置…") },
      { id: "tray_quit", label: t("Quit Carbon", "退出 Carbon") },
    ]);

    const model: TrayMenuModel = {
      tooltip: review.length
        ? t("Carbon — {count} awaiting review", "Carbon — {count} 项待审核", { count: review.length })
        : "Carbon",
      title: review.length ? String(review.length) : "",
      sections,
    };

    const json = JSON.stringify(model);
    if (json === lastJson.current) return;
    const timer = setTimeout(() => {
      lastJson.current = json;
      void updateTray(model);
    }, 250);
    return () => clearTimeout(timer);
  }, [tasks, sessions, dnd, workspaces, currentSlug, language, t]);

  useEffect(() => {
    let off = () => {};
    void onTrayEvent((id) => {
      const h = hRef.current;
      if (id.startsWith("task:")) h.openTask(id.slice(5));
      else if (id.startsWith("filter:")) h.openFilter(id.slice(7));
      else if (id.startsWith("project:")) h.switchProject(id.slice(8));
      else if (id === "new_task") h.newTask();
      else if (id === "settings") h.openSettings();
      else if (id === "toggle:dnd")
        setDndState((d) => {
          const next = !d;
          setDnd(next);
          return next;
        });
    }).then((fn) => {
      off = fn;
    });
    return () => off();
  }, []);
}

type MenuHandlers = Partial<Record<DesktopEvent, () => void>>;

// useDesktopMenu dispatches native menu/tray events to the matching UI action.
export function useDesktopMenu(handlers: MenuHandlers) {
  const ref = useRef(handlers);
  useEffect(() => {
    ref.current = handlers;
  }, [handlers]);
  useEffect(() => {
    let off = () => {};
    void onDesktopEvent((e) => ref.current[e]?.()).then((fn) => {
      off = fn;
    });
    return () => off();
  }, []);
}
