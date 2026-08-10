// Minimal light/dark theme handling — persists choice, falls back to system preference.

export type Theme = "light" | "dark";

const KEY = "carbon-theme";
const LEGACY_KEY = "cairn-theme";

export function getTheme(): Theme {
  const saved = localStorage.getItem(KEY);
  if (saved === "light" || saved === "dark") return saved;
  if (saved === null) {
    const legacy = localStorage.getItem(LEGACY_KEY);
    if (legacy === "light" || legacy === "dark") {
      try {
        localStorage.setItem(KEY, legacy);
        localStorage.removeItem(LEGACY_KEY);
      } catch {
        // The selected theme still applies for this session.
      }
      return legacy;
    }
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  localStorage.setItem(KEY, theme);
}

export function toggleTheme(): Theme {
  const next: Theme = getTheme() === "dark" ? "light" : "dark";
  applyTheme(next);
  return next;
}

export function initTheme() {
  applyTheme(getTheme());
}
