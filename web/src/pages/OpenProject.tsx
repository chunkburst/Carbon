import { useEffect, useState } from "react";
import { ArrowRight, Clock, FolderOpen, FolderSearch, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import * as api from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { isTauri, pickFolder } from "@/lib/tauri";

const RECENT_KEY = "carbon-recent-clusters";
const LEGACY_RECENT_KEYS = ["cairn-recent-clusters", "cairn-recent-folders"];

function parseRecentClusters(raw: string | null): string[] {
  try {
    const parsed: unknown = JSON.parse(raw ?? "[]");
    return Array.isArray(parsed) ? parsed.filter((path): path is string => typeof path === "string") : [];
  } catch {
    return [];
  }
}

function recentClusters(): string[] {
  try {
    const current = localStorage.getItem(RECENT_KEY);
    if (current !== null) return parseRecentClusters(current);

    const legacyEntries = LEGACY_RECENT_KEYS
      .map((key) => ({ key, raw: localStorage.getItem(key) }))
      .filter((entry): entry is { key: string; raw: string } => entry.raw !== null);
    if (!legacyEntries.length) return [];
    const migrated = [...new Set(legacyEntries.flatMap((entry) => parseRecentClusters(entry.raw)))].slice(0, 5);
    try {
      localStorage.setItem(RECENT_KEY, JSON.stringify(migrated));
      legacyEntries.forEach((entry) => localStorage.removeItem(entry.key));
    } catch {
      // The current session can still render the old recents if storage is unavailable.
    }
    return migrated;
  } catch {
    return [];
  }
}

function remember(path: string) {
  const list = [path, ...recentClusters().filter((p) => p !== path)].slice(0, 5);
  localStorage.setItem(RECENT_KEY, JSON.stringify(list));
}

export function OpenProject({
  onOpen,
  notice,
}: {
  onOpen: (cluster: api.Cluster) => void;
  notice?: string;
}) {
  const { t } = useI18n();
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const recents = recentClusters();

  useEffect(() => {
    // Prefill with the server's launch folder so the common case is one click.
    api.getCluster("").then((cluster) => setPath((p) => p || cluster.root)).catch(() => {});
  }, []);

  async function open(target: string) {
    target = target.trim();
    if (!target) return;
    setBusy(true);
    setError(null);
    try {
      const cluster = await api.openCluster(target);
      remember(cluster.root);
      onOpen(cluster);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  }

  // Native OS folder picker, desktop only. Selecting a folder opens it immediately.
  async function browse() {
    const picked = await pickFolder();
    if (picked) {
      setPath(picked);
      open(picked);
    }
  }

  return (
    <div className="flex h-full items-center justify-center bg-background p-6 text-foreground">
      <div className="w-full max-w-md">
        <div className="mb-6 flex items-center gap-3">
          <span className="grid size-9 place-items-center rounded-lg bg-primary text-primary-foreground">
            <FolderOpen className="size-5" />
          </span>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">
              {t("Open a project cluster", "打开项目集群")}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t(
                "Choose the folder that groups the projects Carbon should manage.",
                "选择用于归集 Carbon 管理项目的文件夹。",
              )}
            </p>
          </div>
        </div>

        {notice && (
          <div className="mb-3 rounded-lg border border-brand/30 bg-brand/5 px-3 py-2 text-sm text-muted-foreground">
            {notice}
          </div>
        )}

        <div className="rounded-xl border bg-card p-5 text-card-foreground shadow-xs">
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void open(path);
            }}
          >
            <FieldGroup className="gap-2">
              <Field data-invalid={!!error}>
                <FieldLabel htmlFor="path">{t("Project cluster folder", "项目集群文件夹")}</FieldLabel>
                <div className="flex gap-2">
                  <Input
                    id="path"
                    autoFocus
                    value={path}
                    onChange={(event) => setPath(event.target.value)}
                    placeholder={t("/path/to/cluster", "/集群/路径")}
                    aria-invalid={!!error}
                    className="font-mono text-sm"
                  />
                  <Button type="submit" disabled={!path.trim() || busy}>
                    {busy ? (
                      <Loader2 data-icon="inline-start" className="animate-spin" />
                    ) : (
                      <ArrowRight data-icon="inline-start" />
                    )}
                    {t("Open cluster", "打开集群")}
                  </Button>
                </div>
                <FieldError>{error}</FieldError>
              </Field>
            </FieldGroup>
            {isTauri() && (
              <Button
                type="button"
                variant="outline"
                onClick={() => void browse()}
                disabled={busy}
                className="mt-2 w-full"
              >
                <FolderSearch data-icon="inline-start" />
                {t("Choose cluster folder…", "选择集群文件夹…")}
              </Button>
            )}
          </form>

          {recents.length > 0 && (
            <div className="mt-5">
              <p className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {t("Recent clusters", "最近打开的集群")}
              </p>
              <div className="-mx-1">
                {recents.map((r) => (
                  <button
                    key={r}
                    onClick={() => open(r)}
                    className="flex w-full items-center gap-2 truncate rounded-md px-1 py-1.5 text-left text-sm hover:bg-muted"
                  >
                    <Clock className="size-3.5 shrink-0 text-muted-foreground" />
                    <span className="truncate font-mono text-xs">{r}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
