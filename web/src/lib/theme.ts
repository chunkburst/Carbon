// Minimal light/dark theme handling — persists choice, falls back to system preference.

export type Theme = "light" | "dark";

const KEY = "carbon-theme";
const LEGACY_KEY = "cairn-theme";

// A desktop WebView can briefly expose a storage area that is unavailable (for example while
// its profile is locked by another process). Theme setup runs before React mounts, so a storage
// exception must never prevent the main Carbon surface from rendering. Keep the preference
// best-effort and always fall back to the system theme for that session.
function readStorage(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // A theme should never prevent the app from mounting.
  }
}

function systemTheme(): Theme {
  try {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  } catch {
    return "light";
  }
}

export function getTheme(): Theme {
  const saved = readStorage(KEY);
  if (saved === "light" || saved === "dark") return saved;
  if (saved === null) {
    const legacy = readStorage(LEGACY_KEY);
    if (legacy === "light" || legacy === "dark") {
      writeStorage(KEY, legacy);
      try { localStorage.removeItem(LEGACY_KEY); } catch { /* best effort migration */ }
      return legacy;
    }
  }
  return systemTheme();
}

export function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  writeStorage(KEY, theme);
}

export function toggleTheme(): Theme {
  const next: Theme = getTheme() === "dark" ? "light" : "dark";
  applyTheme(next);
  return next;
}

export function initTheme() {
  applyTheme(getTheme());
}
