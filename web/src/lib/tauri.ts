// Tauri-only helpers. The browser build must not hard-depend on @tauri-apps packages
// at load time, so the dialog plugin is imported lazily and only inside the desktop app.

export function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

// pickFolder opens the native OS folder picker and returns the chosen absolute path,
// or null if the user cancelled. Returns null outside the desktop app.
export async function pickFolder(): Promise<string | null> {
  if (!isTauri()) return null;
  const { open } = await import("@tauri-apps/plugin-dialog");
  const selected = await open({ directory: true, multiple: false });
  return typeof selected === "string" ? selected : null;
}
