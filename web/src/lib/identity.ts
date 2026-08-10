// The current human's identity for this browser (one name across all workspaces). Stored in
// localStorage and sent on every write via the X-Carbon-Actor header. Defaults to the OS
// username the server suggests (status.suggestedActor) until the user sets their own.
import { useEffect, useSyncExternalStore } from "react";

const KEY = "carbon-actor";
const EVENT = "carbon-actor-change";
const LEGACY_KEY = "cairn-actor";

export function currentActor(): string {
  try {
    const current = localStorage.getItem(KEY);
    if (current !== null) return current;

    const legacy = localStorage.getItem(LEGACY_KEY) || "";
    if (!legacy) return "";
    try {
      localStorage.setItem(KEY, legacy);
      localStorage.removeItem(LEGACY_KEY);
    } catch {
      // The current session can use the legacy actor if storage is unavailable.
    }
    return legacy;
  } catch {
    return "";
  }
}

export function setActor(actor: string) {
  const clean = sanitize(actor);
  if (!clean) return;
  localStorage.setItem(KEY, clean);
  window.dispatchEvent(new Event(EVENT));
}

/** Strip the kind prefix for display: "human:shahram" -> "shahram". */
export function displayName(actor?: string): string {
  if (!actor) return "";
  const i = actor.indexOf(":");
  return i >= 0 ? actor.slice(i + 1) : actor;
}

/** Turn a typed name into a human actor: "Shahram K" -> "human:Shahram-K". */
export function toActor(name: string): string {
  const handle = name.trim().replace(/\s+/g, "-");
  return handle ? `human:${handle}` : "";
}

function sanitize(actor: string): string {
  return actor
    .replace(/[\n\r\t]/g, "")
    .trim()
    .slice(0, 64);
}

function subscribe(cb: () => void) {
  window.addEventListener(EVENT, cb);
  const onStorage = (event: StorageEvent) => {
    if (event.key === KEY) cb();
  };
  window.addEventListener("storage", onStorage); // sync across Carbon tabs
  return () => {
    window.removeEventListener(EVENT, cb);
    window.removeEventListener("storage", onStorage);
  };
}

/**
 * Current identity, seeded from the server's suggested actor (OS username) the first time it
 * arrives if the user hasn't chosen one yet. Returns the effective actor + a setter.
 */
export function useIdentity(suggested?: string): { actor: string; setName: (name: string) => void } {
  const stored = useSyncExternalStore(subscribe, currentActor, () => "");
  useEffect(() => {
    if (!stored && suggested) setActor(suggested); // seed once so writes are attributed
  }, [stored, suggested]);
  return {
    actor: stored || suggested || "",
    setName: (name: string) => setActor(toActor(name)),
  };
}
