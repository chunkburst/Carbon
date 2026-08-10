/* eslint-disable react-refresh/only-export-components -- fixed catalog token helpers belong with their renderer. */
import { useEffect, useState } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Apple,
  Database,
  FlaskConical,
  Folder,
  FolderKanban,
  Globe,
  Layers,
  Monitor,
  Package,
  Server,
  Smartphone,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { carbonCatalogAssetURL } from "@/lib/carbon-api";

const CATALOG_ASSET_CHANGE_EVENT = "carbon-catalog-asset-change";

export type CatalogIconKind = "builtin" | "emoji";

export type CatalogIconToken = {
  kind: CatalogIconKind;
  key: string;
};

export type CatalogIconTarget = "cluster" | "project";

export type CatalogPresentationIcons = {
  clusters: Record<string, CatalogIconToken>;
  projects: Record<string, CatalogIconToken>;
};

export type CatalogIconMutation = {
  target: CatalogIconTarget;
  id: string;
  icon: CatalogIconToken | null;
};

export const catalogBuiltinIconKeys = [
  "folder",
  "layers",
  "monitor",
  "smartphone",
  "apple",
  "globe",
  "server",
  "package",
  "database",
  "flask",
] as const;

export const catalogEmojiIconKeys = ["atom", "rocket", "spark", "puzzle", "shield", "palette"] as const;

export const catalogIconChoices: CatalogIconToken[] = [
  ...catalogBuiltinIconKeys.map((key) => ({ kind: "builtin" as const, key })),
  ...catalogEmojiIconKeys.map((key) => ({ kind: "emoji" as const, key })),
];

const builtinIcons: Record<(typeof catalogBuiltinIconKeys)[number], LucideIcon> = {
  folder: Folder,
  layers: Layers,
  monitor: Monitor,
  smartphone: Smartphone,
  apple: Apple,
  globe: Globe,
  server: Server,
  package: Package,
  database: Database,
  flask: FlaskConical,
};

const emojiGlyphs: Record<(typeof catalogEmojiIconKeys)[number], string> = {
  atom: "⚛",
  rocket: "🚀",
  spark: "✦",
  puzzle: "🧩",
  shield: "🛡",
  palette: "🎨",
};

function isBuiltinKey(value: string): value is keyof typeof builtinIcons {
  return value in builtinIcons;
}

function isEmojiKey(value: string): value is keyof typeof emojiGlyphs {
  return value in emojiGlyphs;
}

// isCatalogIconToken deliberately accepts only the finite token set shipped with the
// app. It is the UI-side counterpart to the server allowlist and never renders a URL,
// path, SVG, HTML, data URI, or caller-supplied glyph.
export function isCatalogIconToken(value: unknown): value is CatalogIconToken {
  if (!value || typeof value !== "object") return false;
  const candidate = value as { kind?: unknown; key?: unknown };
  return (candidate.kind === "builtin" && typeof candidate.key === "string" && isBuiltinKey(candidate.key))
    || (candidate.kind === "emoji" && typeof candidate.key === "string" && isEmojiKey(candidate.key));
}

export function catalogIconFor(
  presentation: CatalogPresentationIcons | undefined,
  target: CatalogIconTarget,
  id: string | undefined,
): CatalogIconToken | null {
  if (!presentation || !id) return null;
  const candidate = target === "cluster" ? presentation.clusters?.[id] : presentation.projects?.[id];
  return isCatalogIconToken(candidate) ? candidate : null;
}

export function catalogIconsEqual(left: CatalogIconToken | null | undefined, right: CatalogIconToken | null | undefined): boolean {
  return left?.kind === right?.kind && left?.key === right?.key;
}

/**
 * Builds the only image source the catalog renderer accepts.  The server serves
 * this relative same-origin endpoint with `nosniff`; callers never pass a URL,
 * filesystem path, SVG string, or data URI through icon presentation.
 */
export function catalogIconAssetURL(
  home: string | undefined,
  target: CatalogIconTarget,
  id: string | undefined,
  revision?: string | number,
): string | undefined {
  return carbonCatalogAssetURL(home, target, id, revision);
}

/** Refresh rendered asset URLs after a successful raw PUT or DELETE. */
export function notifyCatalogIconAssetChanged(): void {
  if (typeof window !== "undefined") window.dispatchEvent(new Event(CATALOG_ASSET_CHANGE_EVENT));
}

function safeCatalogAssetURL(value?: string): string | undefined {
  // The renderer is deliberately not a generic image component. Restrict its
  // remote branch to this app's relative presentation endpoint so a token UI
  // cannot become a URL/path/SVG injection surface.
  return value?.startsWith("/api/home/presentation/") ? value : undefined;
}

export function CatalogIcon({
  icon,
  assetURL,
  fallback: Fallback = FolderKanban,
  className,
  label,
}: {
  icon?: CatalogIconToken | null;
  /** Same-origin image endpoint returned by `catalogIconAssetURL`; no arbitrary sources. */
  assetURL?: string;
  fallback?: LucideIcon;
  className?: string;
  label?: string;
}) {
  const [assetFailed, setAssetFailed] = useState(false);
  const [assetRevision, setAssetRevision] = useState(0);
  const imageSource = safeCatalogAssetURL(assetURL);
  useEffect(() => setAssetFailed(false), [imageSource]);
  useEffect(() => {
    const refresh = () => {
      setAssetFailed(false);
      setAssetRevision((current) => current + 1);
    };
    window.addEventListener(CATALOG_ASSET_CHANGE_EVENT, refresh);
    return () => window.removeEventListener(CATALOG_ASSET_CHANGE_EVENT, refresh);
  }, []);
  const accessible = label ? { role: "img", "aria-label": label } : { "aria-hidden": true };
  const renderedSource = imageSource
    ? `${imageSource}${imageSource.includes("?") ? "&" : "?"}asset-revision=${assetRevision}`
    : undefined;
  if (renderedSource && !assetFailed) {
    return (
      <img
        src={renderedSource}
        alt={label ?? ""}
        aria-hidden={label ? undefined : true}
        className={cn("size-4 shrink-0 object-cover", className)}
        onError={() => setAssetFailed(true)}
      />
    );
  }
  if (isCatalogIconToken(icon) && icon.kind === "emoji" && isEmojiKey(icon.key)) {
    return <span {...accessible} className={cn("inline-flex shrink-0 items-center justify-center leading-none", className)}>{emojiGlyphs[icon.key]}</span>;
  }
  const Glyph = isCatalogIconToken(icon) && icon.kind === "builtin" && isBuiltinKey(icon.key)
    ? builtinIcons[icon.key]
    : Fallback;
  return <Glyph {...accessible} className={cn("size-4 shrink-0", className)} />;
}
