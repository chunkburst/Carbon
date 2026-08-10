// In-app notification inbox plus local, schedule-aware delivery preferences. The
// inbox derives from real task query changes; preferences only decide whether to
// surface a new event, never synthesize tasks or notifications.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useTasks } from "@/lib/queries";
import {
  carbonScopeKey,
  getCarbonHome,
  listCarbonTasks,
  type CarbonScope,
  type CarbonScopeInput,
} from "@/lib/carbon-api";
import {
  isTauri,
  isManagedNotificationSound,
  notify,
  playManagedNotificationSound,
  setDnd,
  type ManagedNotificationSound,
  type NotificationTarget,
} from "@/lib/desktop";
import type { Task } from "@/lib/api";
import { currentLanguage, translate, useI18n } from "@/lib/i18n";

export type NotifKind = "ready" | "blocked" | "failed" | "assigned" | "review" | "lease_expiring";

/**
 * A persisted, path-free destination for an inbox item. The shell can resolve
 * this against its current Carbon home when the user opens an older event.
 */
export type NotificationRoute = {
  id: string;
  clusterId?: string;
  projectId?: string;
  hash?: string;
};

export type Notif = {
  key: string;
  kind: NotifKind;
  taskId: string;
  /** Stable source metadata for cross-cluster inbox routing. */
  clusterId?: string;
  projectId?: string;
  text: string;
  at: number;
  read: boolean;
  target?: NotificationRoute;
};

export type NotificationPreferences = {
  events: Record<"ready" | "review" | "check_failed" | "lease_expiring", boolean>;
  activeDays: number[]; // Date.getDay(), Sunday = 0
  start: string; // HH:mm, inclusive
  end: string; // HH:mm, exclusive; supports cross-midnight windows
  // This mirrors the desktop-wide Carbon DND switch. It is retained in the
  // namespaced preference payload for compatibility, but never wins over it.
  doNotDisturb: boolean;
  sound: "off" | "default" | "custom";
  customSoundRef?: ManagedNotificationSound;
};

/**
 * Identifies notification data without ever using a home path as a localStorage
 * key. `homeId` is the manifest's stable Carbon home id and is preferred. Until
 * a server returns it, `home` is converted to a per-install opaque token.
 */
export type NotificationPreferenceScope = {
  homeId?: string;
  home?: string;
  clusterId?: string;
  projectId?: string;
  legacyPath?: string;
};

export type CarbonNotificationAggregation = "project" | "cluster" | "home";

/**
 * Existing callers can keep passing only `(scope, actor)`: that observes the
 * whole current cluster by default. Home aggregation deliberately needs one
 * query scope per cluster because the current task-query API has no home-wide
 * list endpoint. A parent with the home manifest can pass `homeScopes` later
 * without changing the legacy call signature.
 */
export type CarbonNotificationOptions = {
  aggregation?: CarbonNotificationAggregation;
  homeId?: string;
  /** Duplicate project scopes are collapsed to one cluster poll. */
  homeScopes?: readonly CarbonScopeInput[];
  /** Bounds tray-resident background work; values are clamped to 1..64. */
  homeScopeLimit?: number;
};

const LEGACY_PREFERENCES_KEY = "carbon-notification-preferences";
const PREFERENCES_KEY_PREFIX = "carbon-notification-preferences:v2";
const INBOX_KEY_PREFIX = "carbon-notifs:v2";
const STORAGE_SALT_KEY = "carbon-notification-storage-salt:v1";
const DND_KEY = "carbon-dnd";
const LEGACY_DND_KEY = "cairn-dnd";
const ACTIVE_SCOPE_EVENT = "carbon-notification-scope";
const DEFAULT_HOME_NOTIFICATION_SCOPE_LIMIT = 24;
const MAX_HOME_NOTIFICATION_SCOPE_LIMIT = 64;

const defaults: NotificationPreferences = {
  events: { ready: true, review: true, check_failed: true, lease_expiring: true },
  activeDays: [0, 1, 2, 3, 4, 5, 6],
  start: "00:00",
  end: "00:00",
  doNotDisturb: false,
  sound: "default",
};

let volatileStorageSalt = "";
let activePreferenceScope: NotificationPreferenceScope | undefined;

function storageGet(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function storageSet(key: string, value: string): boolean {
  try {
    window.localStorage.setItem(key, value);
    return true;
  } catch {
    return false;
  }
}

function storageRemove(key: string): void {
  try {
    window.localStorage.removeItem(key);
  } catch {
    // A disabled or full storage area should not prevent inbox delivery.
  }
}

function storageJSON(key: string): unknown | undefined {
  const raw = storageGet(key);
  if (raw === null) return undefined;
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return undefined;
  }
}

function newStorageSalt(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `${Date.now()}-${Math.random()}`;
  }
}

function storageSalt(): string {
  const stored = storageGet(STORAGE_SALT_KEY);
  if (stored && /^[a-z0-9-]{8,}$/i.test(stored)) return stored;
  if (!volatileStorageSalt) volatileStorageSalt = newStorageSalt();
  storageSet(STORAGE_SALT_KEY, volatileStorageSalt);
  return volatileStorageSalt;
}

// A keyed, short, non-reversible-for-storage token. The salt keeps commonplace
// local paths out of dictionary-style localStorage inspection while retaining a
// stable key for this portable installation.
function opaqueToken(kind: string, value: string): string {
  const input = `${storageSalt()}\u001f${kind}\u001f${value}`;
  let left = 0x811c9dc5;
  let right = 0x9e3779b9;
  for (let index = 0; index < input.length; index += 1) {
    const code = input.charCodeAt(index);
    left = Math.imul(left ^ code, 0x01000193);
    right = Math.imul(right ^ (code + index), 0x85ebca6b);
  }
  return `${kind}-${(left >>> 0).toString(36)}${(right >>> 0).toString(36)}`;
}

function nonEmpty(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed || undefined;
}

function normalizedScope(scope?: NotificationPreferenceScope): NotificationPreferenceScope | undefined {
  if (!scope) return undefined;
  const normalized: NotificationPreferenceScope = {
    homeId: nonEmpty(scope.homeId),
    home: nonEmpty(scope.home),
    clusterId: nonEmpty(scope.clusterId),
    projectId: nonEmpty(scope.projectId),
    legacyPath: nonEmpty(scope.legacyPath),
  };
  return Object.values(normalized).some(Boolean) ? normalized : undefined;
}

function effectivePreferenceScope(scope?: NotificationPreferenceScope): NotificationPreferenceScope | undefined {
  return normalizedScope(scope) ?? activePreferenceScope;
}

function homeScopeToken(scope?: NotificationPreferenceScope): string {
  const identity = normalizedScope(scope);
  if (identity?.homeId) return opaqueToken("home", `id:${identity.homeId}`);
  if (identity?.home) return opaqueToken("home", `path:${identity.home}`);
  if (identity?.legacyPath) return opaqueToken("legacy", identity.legacyPath);
  return opaqueToken("home", "default");
}

export function notificationPreferenceStorageKey(scope?: NotificationPreferenceScope): string {
  return `${PREFERENCES_KEY_PREFIX}:${homeScopeToken(effectivePreferenceScope(scope))}`;
}

function notificationInboxStorageKey(scope: NotificationPreferenceScope | undefined, aggregation: CarbonNotificationAggregation): string {
  const identity = normalizedScope(scope);
  const parts = [homeScopeToken(identity)];
  if (aggregation !== "home" && identity?.clusterId) parts.push(opaqueToken("cluster", identity.clusterId));
  if (aggregation === "project" && identity?.projectId) parts.push(opaqueToken("project", identity.projectId));
  return `${INBOX_KEY_PREFIX}:${parts.join(":")}`;
}

function fallbackPreferenceKey(scope?: NotificationPreferenceScope): string | undefined {
  const identity = normalizedScope(scope);
  if (!identity?.homeId || !identity.home) return undefined;
  return `${PREFERENCES_KEY_PREFIX}:${homeScopeToken({ ...identity, homeId: undefined })}`;
}

function fallbackInboxKey(scope: NotificationPreferenceScope | undefined, aggregation: CarbonNotificationAggregation): string | undefined {
  const identity = normalizedScope(scope);
  if (!identity?.homeId || !identity.home) return undefined;
  return notificationInboxStorageKey({ ...identity, homeId: undefined }, aggregation);
}

function parsePreferences(raw: unknown): NotificationPreferences {
  if (!raw || typeof raw !== "object") return { ...defaults, events: { ...defaults.events }, activeDays: [...defaults.activeDays] };
  const value = raw as Partial<NotificationPreferences>;
  const events: Partial<NotificationPreferences["events"]> =
    value.events && typeof value.events === "object"
      ? value.events as Partial<NotificationPreferences["events"]>
      : {};
  return {
    events: {
      ready: events.ready !== false,
      review: events.review !== false,
      check_failed: events.check_failed !== false,
      lease_expiring: events.lease_expiring !== false,
    },
    activeDays: Array.isArray(value.activeDays)
      ? value.activeDays.filter((day): day is number => typeof day === "number" && day >= 0 && day <= 6)
      : [...defaults.activeDays],
    start: validTime(value.start) ? value.start : defaults.start,
    end: validTime(value.end) ? value.end : defaults.end,
    doNotDisturb: value.doNotDisturb === true,
    sound: value.sound === "off" || value.sound === "custom" || value.sound === "default" ? value.sound : defaults.sound,
    customSoundRef: isManagedNotificationSound(value.customSoundRef)
      ? { name: value.customSoundRef.name, extension: value.customSoundRef.extension }
      : undefined,
  };
}

function dndOverride(): boolean | undefined {
  const stored = storageGet(DND_KEY);
  if (stored === "on") return true;
  if (stored === "off") return false;
  if (stored !== null) return undefined;

  const legacy = storageGet(LEGACY_DND_KEY);
  if (legacy !== "on" && legacy !== "off") return undefined;
  if (storageSet(DND_KEY, legacy)) storageRemove(LEGACY_DND_KEY);
  return legacy === "on";
}

function synchronizeDnd(preferences: NotificationPreferences, migrate: boolean): NotificationPreferences {
  const desktopValue = dndOverride();
  if (desktopValue === undefined && migrate) {
    try {
      setDnd(preferences.doNotDisturb);
    } catch {
      // Browser-only / storage-disabled mode still follows the preference payload.
    }
  }
  return { ...preferences, doNotDisturb: desktopValue ?? preferences.doNotDisturb };
}

/** The desktop `notify()` helper reads the same key, so sounds and OS alerts share this gate. */
export function notificationDoNotDisturb(preferences: NotificationPreferences): boolean {
  return dndOverride() ?? preferences.doNotDisturb;
}

export function loadNotificationPreferences(scope?: NotificationPreferenceScope): NotificationPreferences {
  const resolvedScope = effectivePreferenceScope(scope);
  const key = notificationPreferenceStorageKey(resolvedScope);
  const fallback = fallbackPreferenceKey(resolvedScope);
  let stored = storageJSON(key);
  let migrated = false;

  if (stored === undefined && fallback && fallback !== key) {
    stored = storageJSON(fallback);
    migrated = stored !== undefined;
  }
  if (stored === undefined) {
    // Kept read-only for backward compatibility. We only ever write the v2 key.
    stored = storageJSON(LEGACY_PREFERENCES_KEY);
    migrated = stored !== undefined;
  }

  const preferences = synchronizeDnd(parsePreferences(stored), stored !== undefined);
  if (migrated) storageSet(key, JSON.stringify(preferences));
  return preferences;
}

export function saveNotificationPreferences(
  preferences: NotificationPreferences,
  scope?: NotificationPreferenceScope,
  options: { syncDoNotDisturb?: boolean } = {},
): NotificationPreferences {
  const clean = parsePreferences(preferences);
  if (options.syncDoNotDisturb !== false) {
    try {
      setDnd(clean.doNotDisturb);
    } catch {
      // The normal preference payload remains usable outside Tauri.
    }
  }
  const synchronized = synchronizeDnd(clean, false);
  storageSet(notificationPreferenceStorageKey(scope), JSON.stringify(synchronized));
  return synchronized;
}

export function getActiveNotificationPreferenceScope(): NotificationPreferenceScope | undefined {
  return activePreferenceScope;
}

function activateNotificationPreferenceScope(scope?: NotificationPreferenceScope): void {
  const next = normalizedScope(scope);
  if (notificationPreferenceStorageKey(next) === notificationPreferenceStorageKey(activePreferenceScope)) return;
  activePreferenceScope = next;
  try {
    window.dispatchEvent(new Event(ACTIVE_SCOPE_EVENT));
  } catch {
    // The calling inbox can still deliver with its explicit scope.
  }
}

export function onActiveNotificationPreferenceScopeChange(listener: () => void): () => void {
  window.addEventListener(ACTIVE_SCOPE_EVENT, listener);
  return () => window.removeEventListener(ACTIVE_SCOPE_EVENT, listener);
}

function validTime(value: unknown): value is string {
  return typeof value === "string" && /^([01]\d|2[0-3]):[0-5]\d$/.test(value);
}

function minute(value: string): number {
  const [hours, minutes] = value.split(":").map(Number);
  return hours * 60 + minutes;
}

export function isWithinNotificationWindow(now: Date, preferences: NotificationPreferences): boolean {
  if (notificationDoNotDisturb(preferences)) return false;
  const start = minute(preferences.start);
  const end = minute(preferences.end);
  const current = now.getHours() * 60 + now.getMinutes();
  const today = now.getDay();
  if (start === end) return preferences.activeDays.includes(today); // 24 hours on each selected start day
  if (start < end) return preferences.activeDays.includes(today) && current >= start && current < end;
  if (current >= start) return preferences.activeDays.includes(today);
  if (current < end) return preferences.activeDays.includes((today + 6) % 7);
  return false;
}

function eventEnabled(kind: NotifKind, preferences: NotificationPreferences): boolean {
  switch (kind) {
    case "ready":
      return preferences.events.ready;
    case "review":
      return preferences.events.review;
    case "failed":
      return preferences.events.check_failed;
    case "lease_expiring":
      return preferences.events.lease_expiring;
    // Assigned/blocked remain inbox-only by default; they are not part of the configured
    // signal set and therefore cannot create surprise audible/OS alerts.
    default:
      return false;
  }
}

const OWNER_KEY = "carbon-notification-owner";
const OWNER_TTL = 15_000;
let ownerId: string | null = null;

function isCaptureWindow(): boolean {
  return window.location.hash.startsWith("#capture");
}

/**
 * Tauri owns the authoritative answer: labels are not forgeable from a webview
 * and the capture window must never become the delivery owner. The
 * localStorage lease is only a browser fallback for multi-tab development.
 */
async function notificationOwner(): Promise<boolean> {
  if (isTauri()) {
    try {
      const { invoke } = await import("@tauri-apps/api/core");
      return (await invoke<unknown>("is_notification_owner")) === true;
    } catch {
      // A shell missing the command must fail closed, rather than letting a
      // secondary webview emit duplicate OS/audio notifications.
      return false;
    }
  }

  if (isCaptureWindow()) return false;
  try {
    ownerId ??= sessionStorage.getItem("carbon-notification-owner-id") ?? crypto.randomUUID();
    sessionStorage.setItem("carbon-notification-owner-id", ownerId);
    const now = Date.now();
    const stored: unknown = JSON.parse(localStorage.getItem(OWNER_KEY) ?? "null");
    const current = stored && typeof stored === "object" ? (stored as { id?: unknown; until?: unknown }) : {};
    if (current.id === ownerId || typeof current.until !== "number" || current.until < now) {
      localStorage.setItem(OWNER_KEY, JSON.stringify({ id: ownerId, until: now + OWNER_TTL }));
      return true;
    }
    return false;
  } catch {
    // Storage can be disabled; the capture guard still applies in that browser.
    return !isCaptureWindow();
  }
}

async function playSound(preferences: NotificationPreferences): Promise<void> {
  if (preferences.sound === "off") return;
  if (preferences.sound === "custom" && preferences.customSoundRef) {
    try {
      await playManagedNotificationSound(preferences.customSoundRef);
      return;
    } catch {
      // Keep automatic alerts best-effort and use the browser fallback below.
    }
  }
  try {
    const Audio = window.AudioContext ?? window.webkitAudioContext;
    if (!Audio) return;
    const context = new Audio();
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.frequency.value = 660;
    gain.gain.setValueAtTime(0.035, context.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.12);
    oscillator.connect(gain).connect(context.destination);
    oscillator.start();
    oscillator.stop(context.currentTime + 0.12);
  } catch {
    // Autoplay policy may reject a background sound; inbox/OS notification still work.
  }
}

function useDocumentVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === "undefined" || !document.hidden);

  useEffect(() => {
    const update = () => setVisible(!document.hidden);
    update();
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  return visible;
}

type StoredNotif = Omit<Notif, "text" | "target" | "clusterId" | "projectId"> & {
  text?: unknown;
  target?: unknown;
  clusterId?: unknown;
  projectId?: unknown;
};
type Snap = { ready: boolean; status: string; assignee?: string; failed: number; execution?: string; leaseExpiresAt?: string };
type ObservedTask = { task: Task; source?: NotificationPreferenceScope };
type NotificationTargetWithSource = NotificationTarget & Pick<NotificationRoute, "clusterId" | "projectId">;
const MAX = 50;
let seq = 0;

function notificationText(kind: NotifKind, taskId: string, fallback = "", language = currentLanguage()) {
  const labels: Record<NotifKind, [string, string]> = {
    ready: ["{id} is ready to start", "{id} 已就绪，可以开始"],
    blocked: ["{id} is blocked by dependencies", "{id} 被依赖项阻塞"],
    failed: ["{id} — a check failed", "{id} — 有一项检查失败"],
    assigned: ["You were assigned {id}", "你被分配到 {id}"],
    review: ["{id} is awaiting review", "{id} 正在等待审核"],
    lease_expiring: ["{id} lease is expiring", "{id} 的租约即将到期"],
  };
  const label = labels[kind];
  return label ? translate(label[0], label[1], { id: taskId }, language) : fallback;
}

function isNotifKind(value: unknown): value is NotifKind {
  return typeof value === "string" && ["ready", "blocked", "failed", "assigned", "review", "lease_expiring"].includes(value);
}

function persistedNotificationRoute(target: NotificationTargetWithSource | undefined, taskId: string): NotificationRoute | undefined {
  if (!target || target.id !== taskId) return undefined;
  const clusterId = nonEmpty(target.clusterId);
  const projectId = nonEmpty(target.projectId);
  const hash = typeof target.hash === "string" && target.hash.startsWith("#") ? target.hash : undefined;
  // `path` deliberately never crosses this boundary: the legacy path is only
  // useful for an immediate desktop alert and must not enter inbox persistence.
  if (!clusterId && !projectId && !hash) return undefined;
  return { id: taskId, clusterId, projectId, hash };
}

function restoreNotificationRoute(raw: unknown, taskId: string): NotificationRoute | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  return persistedNotificationRoute(raw as NotificationTargetWithSource, taskId);
}

function restoreNotification(raw: unknown): Notif | null {
  if (!raw || typeof raw !== "object") return null;
  const stored = raw as Partial<StoredNotif>;
  if (typeof stored.key !== "string" || typeof stored.taskId !== "string" || !isNotifKind(stored.kind)) return null;
  const target = restoreNotificationRoute(stored.target, stored.taskId)
    ?? persistedNotificationRoute({
      id: stored.taskId,
      clusterId: typeof stored.clusterId === "string" ? stored.clusterId : undefined,
      projectId: typeof stored.projectId === "string" ? stored.projectId : undefined,
    }, stored.taskId);
  return {
    key: stored.key,
    kind: stored.kind,
    taskId: stored.taskId,
    clusterId: target?.clusterId,
    projectId: target?.projectId,
    text: notificationText(stored.kind, stored.taskId, typeof stored.text === "string" ? stored.text : ""),
    at: typeof stored.at === "number" ? stored.at : Date.now(),
    read: stored.read === true,
    target,
  };
}

function load(sourceKey: string, legacySourceKeys: readonly string[] = []): Notif[] {
  let stored = storageJSON(sourceKey);
  let migratedFrom: string | undefined;
  if (stored === undefined) {
    for (const key of legacySourceKeys) {
      if (!key || key === sourceKey) continue;
      const candidate = storageJSON(key);
      if (candidate !== undefined) {
        stored = candidate;
        migratedFrom = key;
        break;
      }
    }
  }
  const items = Array.isArray(stored) ? stored.map(restoreNotification).filter((item): item is Notif => item !== null) : [];
  if (migratedFrom && storageSet(sourceKey, JSON.stringify(items))) {
    // The legacy inbox key can contain an absolute path. Delete it only after its
    // contents were copied to the opaque v2 key; the global preference key remains
    // deliberately read-only.
    storageRemove(migratedFrom);
  }
  return items;
}

function snapshot(task: Task): Snap {
  return {
    ready: task.ready,
    status: task.status,
    assignee: task.assignee,
    failed: (task.checks ?? []).filter((check) => check.result === "fail").length,
    execution: task.executionState,
    leaseExpiresAt: task.lease?.expiresAt,
  };
}

function leaseExpiresSoon(value?: string): boolean {
  if (!value) return false;
  const expires = new Date(value).getTime();
  return !Number.isNaN(expires) && expires > Date.now() && expires - Date.now() <= 10 * 60_000;
}

function observedTaskKey(observed: ObservedTask): string {
  return `${homeScopeToken(observed.source)}:${observed.source?.clusterId ? opaqueToken("cluster", observed.source.clusterId) : "cluster-default"}:${observed.task.id}`;
}

function useNotificationInbox(
  tasks: ObservedTask[] | undefined,
  sourceKey: string,
  actor?: string,
  notificationTarget?: (observed: ObservedTask) => NotificationTargetWithSource | undefined,
  preferenceScope?: NotificationPreferenceScope,
  legacySourceKeys: readonly string[] = [],
) {
  const { language } = useI18n();
  const preferenceKey = notificationPreferenceStorageKey(preferenceScope);
  const prev = useRef<Map<string, Snap> | null>(null);
  const [items, setItems] = useState<Notif[]>(() => load(sourceKey, legacySourceKeys));
  const itemsRef = useRef(items);
  useEffect(() => { itemsRef.current = items; }, [items]);

  useEffect(() => {
    activateNotificationPreferenceScope(preferenceScope);
  }, [preferenceKey, preferenceScope]);

  const persist = (next: Notif[]) => {
    itemsRef.current = next;
    storageSet(sourceKey, JSON.stringify(next));
    setItems(next);
  };

  useEffect(() => {
    prev.current = null;
    const loaded = load(sourceKey, legacySourceKeys);
    itemsRef.current = loaded;
    setItems(loaded);
  }, [sourceKey, legacySourceKeys]);

  useEffect(() => {
    if (!tasks) return;
    const snap = new Map(tasks.map((observed) => [observedTaskKey(observed), snapshot(observed.task)]));
    const fresh: Array<{ item: Notif; observed: ObservedTask; target?: NotificationTargetWithSource }> = [];
    const now = Date.now();
    const add = (kind: NotifKind, observed: ObservedTask) => {
      const target = notificationTarget?.(observed);
      const persistedTarget = persistedNotificationRoute(target, observed.task.id);
      fresh.push({
        item: {
          key: `${observedTaskKey(observed)}-${kind}-${now}-${seq++}`,
          kind,
          taskId: observed.task.id,
          clusterId: persistedTarget?.clusterId,
          projectId: persistedTarget?.projectId,
          text: notificationText(kind, observed.task.id),
          at: now,
          read: false,
          target: persistedTarget,
        },
        observed,
        target,
      });
    };

    if (prev.current) {
      for (const observed of tasks) {
        const task = observed.task;
        const previous = prev.current.get(observedTaskKey(observed));
        if (!previous) continue;
        const next = snapshot(task);
        if (next.ready && !previous.ready) add("ready", observed);
        else if (!next.ready && previous.ready) add("blocked", observed);
        if (next.failed > previous.failed) add("failed", observed);
        if (next.execution === "awaiting_review" && previous.execution !== "awaiting_review") add("review", observed);
        if (actor && next.assignee === actor && previous.assignee !== actor) add("assigned", observed);
        if (leaseExpiresSoon(next.leaseExpiresAt) && next.leaseExpiresAt !== previous.leaseExpiresAt) add("lease_expiring", observed);
      }
    }
    prev.current = snap;
    if (!fresh.length) return;
    persist([...fresh.map(({ item }) => item), ...itemsRef.current].slice(0, MAX));

    const preferences = loadNotificationPreferences(preferenceScope);
    const deliverable = fresh.filter(({ item }) => eventEnabled(item.kind, preferences) && isWithinNotificationWindow(new Date(item.at), preferences));
    if (deliverable.length) {
      void (async () => {
        if (!(await notificationOwner())) return;
        // DND can change while the native owner command is in flight. Re-read the
        // shared preference/key immediately before both delivery paths so audio
        // never escapes an OS notification that desktop.notify() would suppress.
        const currentPreferences = loadNotificationPreferences(preferenceScope);
        const currentDeliverable = fresh.filter(({ item }) => eventEnabled(item.kind, currentPreferences)
          && isWithinNotificationWindow(new Date(), currentPreferences));
        if (!currentDeliverable.length) return;
        for (const { item, target } of currentDeliverable) {
          if (!document.hasFocus()) void notify("Carbon", item.text, target);
        }
        // `isWithinNotificationWindow` includes the canonical DND value, which is
        // exactly what desktop.notify() reads before sending an OS notification.
        void playSound(currentPreferences);
      })();
    }
    // Query updates are the source of truth; persist intentionally has stable identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tasks, actor, sourceKey, notificationTarget, preferenceKey]);

  const markAllRead = () => persist(items.map((item) => ({ ...item, read: true })));
  const markRead = (key: string) => persist(items.map((item) => (item.key === key ? { ...item, read: true } : item)));
  const clear = () => persist([]);
  const displayedItems = items.map((item) => ({ ...item, text: notificationText(item.kind, item.taskId, item.text, language) }));
  return { items: displayedItems, unread: displayedItems.filter((item) => !item.read).length, markAllRead, markRead, clear };
}

export function useNotifications(path: string, actor?: string) {
  const { data: tasks } = useTasks(path);
  const scope = useMemo<NotificationPreferenceScope>(() => ({ legacyPath: path }), [path]);
  const sourceKey = useMemo(() => notificationInboxStorageKey(scope, "project"), [scope]);
  const legacySourceKeys = useMemo(() => [`cairn-notifs:${path}`], [path]); // one-time legacy inbox migration
  const observed = useMemo(() => tasks?.map((task) => ({ task, source: scope })), [scope, tasks]);
  const route = useCallback((task: ObservedTask): NotificationTarget => ({ path, id: task.task.id }), [path]);
  return useNotificationInbox(observed, sourceKey, actor, route, scope, legacySourceKeys);
}

function carbonScope(scope: CarbonScopeInput): CarbonScope {
  return typeof scope === "string" ? { legacyPath: scope } : scope;
}

function scopeIdentity(scope: CarbonScopeInput, homeId?: string): NotificationPreferenceScope {
  const current = carbonScope(scope);
  return {
    homeId: nonEmpty(homeId),
    home: nonEmpty(current.home),
    clusterId: nonEmpty(current.clusterId),
    projectId: nonEmpty(current.projectId),
    legacyPath: nonEmpty(current.legacyPath),
  };
}

function aggregateIdentity(scope: NotificationPreferenceScope, aggregation: CarbonNotificationAggregation): NotificationPreferenceScope {
  if (aggregation === "home") return { homeId: scope.homeId, home: scope.home, legacyPath: scope.legacyPath };
  if (aggregation === "cluster") return { ...scope, projectId: undefined };
  return scope;
}

function useCarbonHomeID(scope: CarbonScopeInput, preferredHomeId?: string): string | undefined {
  const home = carbonScope(scope).home;
  const [homeId, setHomeId] = useState<string | undefined>(preferredHomeId);

  useEffect(() => {
    let cancelled = false;
    if (preferredHomeId) {
      setHomeId(preferredHomeId);
      return () => { cancelled = true; };
    }
    setHomeId(undefined);
    if (!home) return () => { cancelled = true; };
    void getCarbonHome(home).then((result) => {
      if (!cancelled && result.available) setHomeId(nonEmpty(result.data.manifest?.id));
    }).catch(() => {
      // Path-derived opaque storage remains the safe fallback on an old sidecar.
    });
    return () => { cancelled = true; };
  }, [home, preferredHomeId]);

  return preferredHomeId ?? homeId;
}

function observedFromHomeQueries(
  entries: readonly { input: CarbonScopeInput; scope: NotificationPreferenceScope }[],
  queries: readonly { data?: Awaited<ReturnType<typeof listCarbonTasks>> }[],
): ObservedTask[] | undefined {
  if (!entries.length) return undefined;
  const observed: ObservedTask[] = [];
  entries.forEach((entry, index) => {
    const result = queries[index]?.data;
    if (!result || result.available !== true) return;
    for (const task of result.data.tasks ?? []) observed.push({ task, source: entry.scope });
  });
  return observed;
}

function tasksFromNotificationQuery(result: Awaited<ReturnType<typeof listCarbonTasks>> | undefined): Task[] | undefined {
  return result?.available === true ? result.data.tasks ?? [] : undefined;
}

function homeNotificationScopeLimit(value?: number): number {
  if (!Number.isFinite(value)) return DEFAULT_HOME_NOTIFICATION_SCOPE_LIMIT;
  return Math.min(MAX_HOME_NOTIFICATION_SCOPE_LIMIT, Math.max(1, Math.floor(value ?? DEFAULT_HOME_NOTIFICATION_SCOPE_LIMIT)));
}

function dedupeHomeNotificationScopes(
  inputs: readonly CarbonScopeInput[],
  homeId: string | undefined,
  limit: number,
): Array<{ input: CarbonScopeInput; scope: NotificationPreferenceScope }> {
  const seen = new Set<string>();
  const entries: Array<{ input: CarbonScopeInput; scope: NotificationPreferenceScope }> = [];
  for (const input of inputs) {
    const scope = scopeIdentity(input, homeId);
    // `include_cluster=true` makes project scopes of the same cluster equivalent
    // for notification polling. Keep a scope-key fallback for old/legacy servers
    // that cannot expose a stable cluster id yet.
    const key = scope.clusterId ? `cluster:${scope.clusterId}` : `scope:${carbonScopeKey(input)}`;
    if (seen.has(key)) continue;
    seen.add(key);
    entries.push({ input, scope });
    if (entries.length >= limit) break;
  }
  return entries;
}

/**
 * Carbon uses the scoped task query, so Home/cluster/project requests never fall
 * back to a legacy filesystem-path task poll just to populate the inbox. The
 * default is deliberately `cluster`, giving a user one signal stream for all
 * sibling projects while retaining a project-specific opt-out when requested.
 */
export function useCarbonNotifications(scope: CarbonScopeInput, actor?: string, options: CarbonNotificationOptions = {}) {
  const aggregation = options.aggregation ?? "cluster";
  const documentVisible = useDocumentVisible();
  const resolvedHomeId = useCarbonHomeID(scope, options.homeId);
  const baseIdentity = useMemo(
    () => scopeIdentity(scope, resolvedHomeId),
    [resolvedHomeId, scope],
  );
  const preferenceScope = useMemo(
    () => ({ homeId: baseIdentity.homeId, home: baseIdentity.home, legacyPath: baseIdentity.legacyPath }),
    [baseIdentity.home, baseIdentity.homeId, baseIdentity.legacyPath],
  );
  const inboxScope = useMemo(
    () => aggregateIdentity(baseIdentity, aggregation),
    [aggregation, baseIdentity],
  );
  // Hooks must stay unconditional, but only the aggregation currently shown in
  // the inbox is allowed to fetch. In particular, a home inbox used to create
  // this project query plus its own per-cluster polls, and a cluster inbox
  // fetched the same tasks once here and once below.
  //
  // Keep the normal task cache key so a project inbox continues to share its
  // data with the visible project board/SSE invalidation when it is enabled.
  const primary = useQuery({
    queryKey: ["carbon", "tasks", carbonScopeKey(scope), "project"],
    queryFn: () => listCarbonTasks(scope, false),
    enabled: aggregation === "project",
    retry: false,
  });
  const clusterPoll = useQuery({
    queryKey: ["carbon", "notification-cluster", carbonScopeKey(scope)],
    queryFn: () => listCarbonTasks(scope, true),
    enabled: aggregation === "cluster" && documentVisible,
    retry: false,
    // This cannot rely on the currently open project's SSE invalidation: a
    // sibling project can change while the user is viewing another one. Polling
    // remains in the tray-resident renderer only; it does not create a service.
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
  });
  const homeScopeLimit = homeNotificationScopeLimit(options.homeScopeLimit);
  const homeEntries = useMemo(
    () => aggregation === "home"
      ? dedupeHomeNotificationScopes(options.homeScopes ?? [], resolvedHomeId, homeScopeLimit)
      : [],
    [aggregation, homeScopeLimit, options.homeScopes, resolvedHomeId],
  );
  const homeQueries = useQueries({
    queries: homeEntries.map((entry) => ({
      queryKey: ["carbon", "notification-home", carbonScopeKey(entry.input)],
      queryFn: () => listCarbonTasks(entry.input, true),
      enabled: aggregation === "home" && documentVisible,
      retry: false,
      // Home aggregation is intentionally opt-in. Its independent polling keeps
      // tray-resident Carbon useful even when no visible project view invalidates
      // the ordinary task query. It does not create a Windows service.
      refetchInterval: 15_000,
      refetchIntervalInBackground: false,
    })),
  });
  const observed = useMemo(() => {
    if (aggregation === "home") {
      // Never fall back to a cached project query here: a home inbox is scoped
      // only by its explicitly deduplicated home entries.
      return observedFromHomeQueries(homeEntries, homeQueries);
    }
    if (aggregation === "cluster") {
      const tasks = tasksFromNotificationQuery(clusterPoll.data);
      return tasks?.map((task) => ({ task, source: inboxScope }));
    }
    const tasks = primary.data?.available ? primary.data.data.tasks ?? [] : undefined;
    return tasks?.map((task) => ({ task, source: inboxScope }));
  }, [aggregation, clusterPoll.data, homeEntries, homeQueries, inboxScope, primary.data]);
  const sourceKey = useMemo(() => notificationInboxStorageKey(inboxScope, aggregation), [aggregation, inboxScope]);
  const legacySourceKeys = useMemo(() => {
    const keys = [fallbackInboxKey(inboxScope, aggregation), `cairn-notifs:carbon:${carbonScopeKey(scope)}`] // one-time legacy inbox migration
      .filter((key): key is string => !!key && key !== sourceKey);
    return [...new Set(keys)];
  }, [aggregation, inboxScope, scope, sourceKey]);
  const route = useCallback((observedTask: ObservedTask): NotificationTargetWithSource | undefined => {
    const source = observedTask.source;
    const projectId = observedTask.task.projectId ?? source?.projectId;
    if (!source?.clusterId) return undefined;
    const target: NotificationTargetWithSource = {
      id: observedTask.task.id,
      clusterId: source.clusterId,
      projectId,
    };
    if (projectId) {
      target.hash = `#carbon/${encodeURIComponent(source.clusterId)}/${encodeURIComponent(projectId)}?task=${encodeURIComponent(observedTask.task.id)}`;
    }
    return target;
  }, []);
  return useNotificationInbox(observed, sourceKey, actor, route, preferenceScope, legacySourceKeys);
}

declare global {
  interface Window {
    webkitAudioContext?: typeof AudioContext;
  }
}
