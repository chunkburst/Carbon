// Desktop (Tauri) integration, all lazily imported and isTauri()-guarded so the plain
// browser build never depends on @tauri-apps/* at load time and degrades to no-ops.
import { isTauri } from "@/lib/tauri";
import { registerWorkspace } from "@/lib/workspaces";

export { isTauri };

// --- Navigation / deep links --------------------------------------------------------

// navigateToTask registers the workspace (so the slug resolves) and routes to the task.
export function navigateToTask(path: string, id: string): void {
  window.location.hash = `#/${registerWorkspace(path)}/task/${id}`;
}

// Carbon owns generated deep links. Keep the historical scheme as a read-only
// migration alias: <scheme>://task/<id>?repo=<path> or <scheme>://open?repo=<path>.
const LEGACY_DEEP_LINK_PROTOCOL = "cairn:";
export function openDeepLink(urlStr: string): void {
  let u: URL;
  try {
    u = new URL(urlStr);
  } catch {
    return;
  }
  if (u.protocol !== "carbon:" && u.protocol !== LEGACY_DEEP_LINK_PROTOCOL) return;
  if (u.hostname === "task") {
    const id = u.pathname.replace(/^\//, "");
    if (!id) return;
    const project = u.searchParams.get("project")?.trim();
    if (project) {
      const cluster = u.searchParams.get("cluster")?.trim();
      const base = cluster
        ? `#carbon/${encodeURIComponent(cluster)}/${encodeURIComponent(project)}`
        : `#carbon/project/${encodeURIComponent(project)}`;
      const taskQuery = new URLSearchParams();
      if (cluster && u.searchParams.get("taskScope") === "cluster") {
        taskQuery.set("taskScope", "cluster");
      } else {
        const taskProject = u.searchParams.get("taskProject")?.trim();
        if (taskProject && taskProject !== project) taskQuery.set("taskProject", taskProject);
      }
      const suffix = taskQuery.toString();
      window.location.hash = `${base}/task/${encodeURIComponent(id)}${suffix ? `?${suffix}` : ""}`;
      return;
    }
    const repo = u.searchParams.get("repo");
    if (repo) navigateToTask(repo, id);
  } else if (u.hostname === "open") {
    const repo = u.searchParams.get("repo");
    if (!repo) return;
    window.location.hash = `#/${registerWorkspace(repo)}/all`;
  }
}

// onDeepLink subscribes to Carbon deep-link opens forwarded by the Rust shell.
export async function onDeepLink(handler: (url: string) => void): Promise<() => void> {
  if (!isTauri()) return () => {};
  const { listen } = await import("@tauri-apps/api/event");
  const un = await listen<string>("deep-link", (e) => handler(e.payload));
  return () => un();
}

// --- Live tray menu -----------------------------------------------------------------

export type TrayItem = { id: string; label: string; checked?: boolean; enabled?: boolean };
export type TrayMenuModel = { tooltip: string; title: string; sections: TrayItem[][] };

// updateTray pushes a full menu model to the native tray (Rust rebuilds it). Clicks come
// back via onTrayEvent. No-op outside the desktop app.
export async function updateTray(menu: TrayMenuModel): Promise<void> {
  if (!isTauri()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("update_tray", { menu });
  } catch {
    // best-effort; tray may not be ready yet
  }
}

// onTrayEvent subscribes to tray menu-item clicks; the payload is the item id. Returns an
// unsubscribe fn.
export async function onTrayEvent(handler: (id: string) => void): Promise<() => void> {
  if (!isTauri()) return () => {};
  const { listen } = await import("@tauri-apps/api/event");
  const un = await listen<string>("tray:menu", (e) => handler(e.payload));
  return () => un();
}

// --- Do Not Disturb -----------------------------------------------------------------

const DND_KEY = "carbon-dnd";
const LEGACY_DND_KEY = "cairn-dnd";

function readMigratedFlag(key: string, legacyKey: string): string | null {
  try {
    const current = localStorage.getItem(key);
    if (current !== null) return current;

    const legacy = localStorage.getItem(legacyKey);
    if (legacy === null) return null;
    try {
      localStorage.setItem(key, legacy);
      localStorage.removeItem(legacyKey);
    } catch {
      // Preserve the preference for this session when browser storage is restricted.
    }
    return legacy;
  } catch {
    return null;
  }
}

export function dndEnabled(): boolean {
  return readMigratedFlag(DND_KEY, LEGACY_DND_KEY) === "on";
}

export function setDnd(on: boolean): void {
  localStorage.setItem(DND_KEY, on ? "on" : "off");
}

// --- OS notifications ---------------------------------------------------------------

const OS_NOTIF_KEY = "carbon-os-notifs";
const LEGACY_OS_NOTIF_KEY = "cairn-os-notifs";

export function osNotifEnabled(): boolean {
  return readMigratedFlag(OS_NOTIF_KEY, LEGACY_OS_NOTIF_KEY) !== "off";
}

export function setOsNotifEnabled(on: boolean): void {
  localStorage.setItem(OS_NOTIF_KEY, on ? "on" : "off");
}

// Notifications carry their own 32-bit id. The plugin returns that id to onAction,
// avoiding the unsafe "last notification wins" behavior when several tasks alert.
// The short in-memory map handles ordinary clicks; `extra` preserves the Carbon
// hash target across a renderer reload without ever serializing a filesystem path.
export type NotificationTarget = { id: string; path?: string; hash?: string };
const NOTIFICATION_TARGET_EXTRA = "carbonNotificationTarget";
const MAX_NOTIFICATION_TARGETS = 128;
let nextNotificationId = 0;
const notificationTargets = new Map<number, NotificationTarget>();
let notifClicksWired = false;

function allocateNotificationId(): number {
  nextNotificationId = (nextNotificationId + 1) & 0x7fff_ffff;
  if (nextNotificationId === 0) nextNotificationId = 1;
  return nextNotificationId;
}

function rememberNotificationTarget(id: number, target: NotificationTarget): void {
  notificationTargets.set(id, target);
  while (notificationTargets.size > MAX_NOTIFICATION_TARGETS) {
    const oldest = notificationTargets.keys().next().value;
    if (oldest === undefined) break;
    notificationTargets.delete(oldest);
  }
}

function notificationTargetExtra(target: NotificationTarget): Record<string, unknown> | undefined {
  // A legacy target may only have a filesystem path. It stays in memory for a
  // live click but is deliberately not handed to the platform notification store.
  if (!target.hash?.startsWith("#")) return undefined;
  return { [NOTIFICATION_TARGET_EXTRA]: { id: target.id, hash: target.hash } };
}

function targetFromNotificationExtra(extra: unknown): NotificationTarget | undefined {
  if (!extra || typeof extra !== "object") return undefined;
  const value = (extra as Record<string, unknown>)[NOTIFICATION_TARGET_EXTRA];
  if (!value || typeof value !== "object") return undefined;
  const target = value as { id?: unknown; hash?: unknown };
  if (typeof target.id !== "string" || !target.id || typeof target.hash !== "string" || !target.hash.startsWith("#")) return undefined;
  return { id: target.id, hash: target.hash };
}

function openNotificationTarget(target: NotificationTarget): void {
  if (target.hash) {
    window.location.hash = target.hash;
    return;
  }
  if (target.path) navigateToTask(target.path, target.id);
}

async function wireNotificationClicks(): Promise<void> {
  if (notifClicksWired || !isTauri()) return;
  notifClicksWired = true;
  try {
    const n = await import("@tauri-apps/plugin-notification");
    await n.onAction((notification) => {
      const target = typeof notification.id === "number" ? notificationTargets.get(notification.id) : undefined;
      // `id` is supported by the current Tauri notification plugin. If an OS
      // fails to return it, a path-free hash in `extra` still identifies this
      // exact Carbon task; otherwise intentionally leave the app on its current
      // view instead of routing the user to a different task by mistake.
      const resolved = target ?? targetFromNotificationExtra(notification.extra);
      if (resolved) openNotificationTarget(resolved);
    });
  } catch {
    // click events unsupported on this platform — the notification still shows
  }
}

// notify shows an OS notification when running in the desktop app and the user hasn't turned
// them off. Permission is requested lazily on first use. `target` makes the alert clickable.
export async function notify(
  title: string,
  body: string,
  target?: NotificationTarget,
): Promise<void> {
  if (!isTauri() || !osNotifEnabled() || dndEnabled()) return;
  try {
    const n = await import("@tauri-apps/plugin-notification");
    let granted = await n.isPermissionGranted();
    if (!granted) granted = (await n.requestPermission()) === "granted";
    if (!granted) return;
    if (target) {
      const id = allocateNotificationId();
      rememberNotificationTarget(id, target);
      void wireNotificationClicks();
      n.sendNotification({ title, body, id, extra: notificationTargetExtra(target) });
      return;
    }
    n.sendNotification({ title, body });
  } catch {
    // notifications are best-effort
  }
}

// --- Autostart (launch at login) ----------------------------------------------------

// Windows uses native commands instead of tauri-plugin-autostart. The plugin does not
// quote executable paths containing spaces and cannot represent elevated scheduled tasks.
export type AutostartMode = "off" | "user" | "admin";
// Windows can display UAC and serialize this work behind a process-wide mutex. Keep the
// renderer responsive while still allowing that bounded native operation to finish.
const AUTOSTART_REQUEST_TIMEOUT_MS = 35_000;

export async function getAutostartMode(): Promise<AutostartMode> {
  if (!isTauri()) return "off";
  const { invoke } = await import("@tauri-apps/api/core");
  const mode = await withDesktopTimeout(
    invoke<string>("get_autostart_mode"),
    AUTOSTART_REQUEST_TIMEOUT_MS,
    "Reading the launch-at-login setting",
  );
  return mode === "user" || mode === "admin" ? mode : "off";
}

export async function setAutostartMode(mode: AutostartMode): Promise<void> {
  if (!isTauri()) return;
  const { invoke } = await import("@tauri-apps/api/core");
  await withDesktopTimeout(
    invoke("set_autostart_mode", { mode }),
    AUTOSTART_REQUEST_TIMEOUT_MS,
    "Saving the launch-at-login setting",
  );
}

// Keep the native application menu in step with the web UI language. Dynamic tray labels
// come from desktop-hooks.ts and are refreshed independently.
export async function syncNativeLanguage(language: "en" | "zh"): Promise<void> {
  if (!isTauri()) return;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("set_ui_language", { language });
  } catch {
    // Best-effort: the web UI remains translated even if the native shell is unavailable.
  }
}

// isPortable is true only for the Windows ZIP distribution. Rust reads the marker beside the
// executable so a website cannot opt itself into the less restricted installer update path.
export async function isPortable(): Promise<boolean> {
  if (!isTauri()) return false;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    return await invoke<boolean>("is_portable");
  } catch {
    return false;
  }
}

// --- Menu / tray events -------------------------------------------------------------

export type DesktopEvent =
  | "menu:new_task"
  | "menu:open_folder"
  | "menu:board"
  | "menu:graph"
  | "menu:settings";

const DESKTOP_EVENTS: DesktopEvent[] = [
  "menu:new_task",
  "menu:open_folder",
  "menu:board",
  "menu:graph",
  "menu:settings",
];

// onDesktopEvent subscribes to native menu/tray events; returns an unsubscribe fn.
export async function onDesktopEvent(handler: (e: DesktopEvent) => void): Promise<() => void> {
  if (!isTauri()) return () => {};
  const { listen } = await import("@tauri-apps/api/event");
  const unlistens = await Promise.all(
    DESKTOP_EVENTS.map((e) => listen(e, () => handler(e))),
  );
  return () => unlistens.forEach((u) => u());
}

// --- Capture window -----------------------------------------------------------------

export async function closeCaptureWindow(): Promise<void> {
  if (!isTauri()) return;
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    await getCurrentWindow().close();
  } catch {
    // ignore
  }
}

// --- System floating board ----------------------------------------------------------

// This grammar mirrors the Rust command boundary.  Floating-window routes are built from
// catalog/task IDs only: never a filesystem path, URL, or arbitrary hash fragment.
const FLOATING_METADATA = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/;

export type FloatingBoardTarget = {
  clusterId?: string;
  /** Omitted only for an explicit cluster-wide task feed. */
  projectId?: string;
  /** The project chrome used when a cluster-wide task opens back in the main window. */
  workspaceProjectId: string;
};

export type FloatingTaskTarget = FloatingBoardTarget & {
  taskId: string;
};

export type FloatingBoardOpenResult = "opened" | "unsupported" | "invalid" | "unavailable";

export function isSafeFloatingMetadata(value: unknown): value is string {
  return typeof value === "string" && FLOATING_METADATA.test(value);
}

export function isFloatingBoardTarget(value: unknown): value is FloatingBoardTarget {
  if (!value || typeof value !== "object") return false;
  const target = value as Partial<FloatingBoardTarget>;
  if (!isSafeFloatingMetadata(target.workspaceProjectId)) return false;
  if (target.clusterId !== undefined && !isSafeFloatingMetadata(target.clusterId)) return false;
  // A standalone scope must name its project. A cluster scope may intentionally omit it to
  // request its shared task feed, but its workspace project still anchors detail navigation.
  if (target.projectId === undefined) return target.clusterId !== undefined;
  return isSafeFloatingMetadata(target.projectId) && target.projectId === target.workspaceProjectId;
}

export function isFloatingTaskTarget(value: unknown): value is FloatingTaskTarget {
  return isFloatingBoardTarget(value) && isSafeFloatingMetadata((value as Partial<FloatingTaskTarget>).taskId);
}

/**
 * Parse only the native floating-window hash.  Unknown/duplicated parameters fail closed so a
 * manually pasted fragment cannot broaden the project scope or become an alternate route.
 */
export function floatingBoardTargetFromHash(hash?: string): FloatingBoardTarget | null {
  const raw = (hash ?? (typeof window !== "undefined" ? window.location.hash : "")).replace(/^#\/?/, "");
  const [route, query = ""] = raw.split("?", 2);
  if (route !== "floating-board") return null;

  const params = new URLSearchParams(query);
  for (const [key] of params) if (key !== "project" && key !== "cluster" && key !== "workspace") return null;
  const projects = params.getAll("project");
  const clusters = params.getAll("cluster");
  const workspaces = params.getAll("workspace");
  if (projects.length > 1 || clusters.length > 1 || workspaces.length !== 1) return null;
  const target: FloatingBoardTarget = {
    workspaceProjectId: workspaces[0],
    ...(projects.length === 1 ? { projectId: projects[0] } : {}),
    ...(clusters.length === 1 ? { clusterId: clusters[0] } : {}),
  };
  return isFloatingBoardTarget(target) ? target : null;
}

/**
 * Opens the true native picture-in-picture board in Tauri.  Browser callers receive an explicit
 * no-op signal and can keep their in-page fallback without loading desktop packages.
 */
export async function openFloatingBoard(target: FloatingBoardTarget): Promise<FloatingBoardOpenResult> {
  if (!isFloatingBoardTarget(target)) return "invalid";
  if (!isTauri()) return "unsupported";
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    // Startup can publish the loopback port a moment before the HTTP handler is ready. Retry
    // that narrow race instead of leaving a native webview on a permanently blank response.
    for (let attempt = 0; attempt < 4; attempt += 1) {
      try {
        await invoke("open_floating_board", { target });
        return "opened";
      } catch {
        if (attempt === 3) break;
        await new Promise<void>((resolve) => window.setTimeout(resolve, 220 * (attempt + 1)));
      }
    }
    return "unavailable";
  } catch {
    return "unavailable";
  }
}

/** Returns the main Carbon window to the selected task without accepting a caller-provided URL. */
export async function focusMainTaskFromFloatingBoard(target: FloatingTaskTarget): Promise<boolean> {
  if (!isFloatingTaskTarget(target) || !isTauri()) return false;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("focus_main_task", { target });
    return true;
  } catch {
    return false;
  }
}

/** Close the native board from the main workspace or its own window. Browser callers get false. */
export async function closeFloatingBoard(): Promise<boolean> {
  if (!isTauri()) return false;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    return await invoke<boolean>("close_floating_board");
  } catch {
    return false;
  }
}

/**
 * Native lifecycle signal emitted after the OS has actually destroyed the floating window.
 * The main workspace uses it to clear its open preference even when the user uses a title-bar X.
 */
export async function onFloatingBoardClosed(handler: () => void): Promise<() => void> {
  if (!isTauri()) return () => {};
  try {
    const { listen } = await import("@tauri-apps/api/event");
    return await listen("floating-board:closed", handler);
  } catch {
    return () => {};
  }
}

export async function closeFloatingBoardWindow(): Promise<void> {
  if (!isTauri()) {
    // A direct browser visit to #floating-board is safe and leaves the special route.
    window.location.hash = "#carbon";
    return;
  }

  // The floating webview can be visually unresponsive while its IPC queue is still blocked.
  // Never make the user wait forever on the app-level command: close the current native window
  // first, with a short deadline, then try the command and finally force-destroy the window.
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    const current = getCurrentWindow();
    await Promise.race([
      current.close(),
      new Promise<never>((_, reject) => window.setTimeout(() => reject(new Error("native close timed out")), 700)),
    ]);
    return;
  } catch {
    // Continue to the command/final destroy fallback below.
  }

  try {
    const closed = await Promise.race([
      closeFloatingBoard(),
      new Promise<boolean>((resolve) => window.setTimeout(() => resolve(false), 700)),
    ]);
    if (closed) return;
  } catch {
    // Continue to the force-destroy fallback.
  }

  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    await getCurrentWindow().destroy();
  } catch {
    // Closing an already-destroyed native window is intentionally harmless.
  }
}

const DESKTOP_REQUEST_TIMEOUT_MS = 8_000;

function withDesktopTimeout<T>(operation: Promise<T>, timeoutMs: number, action: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error(`${action} timed out after ${Math.ceil(timeoutMs / 1000)} seconds.`)), timeoutMs);
    void operation.then(
      (value) => {
        window.clearTimeout(timer);
        resolve(value);
      },
      (error: unknown) => {
        window.clearTimeout(timer);
        reject(error);
      },
    );
  });
}

// This is deliberately metadata, never a filesystem path. Rust owns the app-data
// copy, persists this exact pair, and resolves it again when it plays the sound.
export type ManagedNotificationSound = {
  name: string;
  extension: string;
};

export function isManagedNotificationSound(value: unknown): value is ManagedNotificationSound {
  if (!value || typeof value !== "object") return false;
  const sound = value as { name?: unknown; extension?: unknown };
  return typeof sound.name === "string"
    && sound.name.trim().length > 0
    && !/[\\/]/.test(sound.name)
    && typeof sound.extension === "string"
    && /^[a-z0-9]{1,8}$/i.test(sound.extension);
}

// `choose_notification_sound` returns Rust's persisted `{ name, extension }` metadata.
// Do not construct a path or a client-side reference: only Rust can resolve/play it.
export async function importNotificationWav(): Promise<ManagedNotificationSound | null> {
  if (!isTauri()) return null;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const result = await invoke<unknown>("choose_notification_sound");
    return isManagedNotificationSound(result) ? { name: result.name, extension: result.extension } : null;
  } catch {
    return null;
  }
}

export async function getManagedNotificationSound(): Promise<ManagedNotificationSound | null> {
  if (!isTauri()) return null;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const result = await invoke<unknown>("get_notification_sound");
    return isManagedNotificationSound(result) ? { name: result.name, extension: result.extension } : null;
  } catch {
    return null;
  }
}

export async function playManagedNotificationSound(reference: ManagedNotificationSound): Promise<void> {
  if (!isTauri()) throw new Error("Custom sound preview is available only in the desktop app.");
  if (!isManagedNotificationSound(reference)) throw new Error("The selected custom sound is invalid.");

  const { invoke } = await import("@tauri-apps/api/core");
  const saved = await invoke<unknown>("get_notification_sound");
  if (!isManagedNotificationSound(saved) || saved.name !== reference.name || saved.extension !== reference.extension) {
    throw new Error("The selected custom sound is no longer available. Choose it again.");
  }
  if ((await invoke<boolean>("play_notification_sound")) !== true) {
    throw new Error("Carbon could not play the selected custom sound.");
  }
}

// `path` is always the active home. When a change needs a restart, the requested
// destination is carried separately in `pendingPath` so callers do not present it
// as already active.
export type DataHome = {
  path: string;
  pendingPath?: string;
  isDefault: boolean;
  restartRequired: boolean;
};

function parseDataHome(result: unknown): DataHome | null {
  if (
    !result ||
    typeof result !== "object" ||
    !("path" in result) ||
    typeof result.path !== "string" ||
    !("isDefault" in result) ||
    typeof result.isDefault !== "boolean" ||
    !("restartRequired" in result) ||
    typeof result.restartRequired !== "boolean"
  ) {
    return null;
  }
  const source = result as Record<string, unknown>;
  if ("pendingPath" in source && source.pendingPath !== undefined && typeof source.pendingPath !== "string") {
    return null;
  }
  const pendingPath = typeof source.pendingPath === "string" ? source.pendingPath : undefined;
  return {
    path: source.path as string,
    pendingPath,
    isDefault: source.isDefault as boolean,
    restartRequired: source.restartRequired as boolean,
  };
}

export async function getMainDataHome(): Promise<DataHome | null> {
  if (!isTauri()) return null;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const result = await withDesktopTimeout(
      invoke<unknown>("get_data_home"),
      DESKTOP_REQUEST_TIMEOUT_MS,
      "Reading the active Carbon data home",
    );
    return parseDataHome(result);
  } catch {
    return null;
  }
}

export async function setMainDataHome(path: string): Promise<DataHome | null> {
  if (!isTauri()) return null;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const result = await invoke<unknown>("set_data_home", { path });
    return parseDataHome(result);
  } catch {
    return null;
  }
}
