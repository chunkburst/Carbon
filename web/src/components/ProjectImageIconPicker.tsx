import { useEffect, useId, useRef, useState } from "react";
import { ImagePlus, Loader2, RotateCcw, X } from "lucide-react";
import { CatalogIcon, catalogIconAssetURL, type CatalogIconToken } from "@/components/CatalogIcon";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

const CATALOG_ICON_MAX_BYTES = 1024 * 1024;
const CATALOG_ICON_MAX_DIMENSION = 4096;
const CATALOG_ICON_MAX_PIXELS = 1024 * 1024;

export type ProjectImageIntent =
  | { kind: "unchanged" }
  | { kind: "upload"; file: File }
  | { kind: "clear" };

const acceptedTypes = new Set(["image/png", "image/jpeg", "image/webp"]);

function hasPNGSignature(bytes: Uint8Array): boolean {
  return bytes.length >= 8
    && bytes[0] === 0x89
    && bytes[1] === 0x50
    && bytes[2] === 0x4e
    && bytes[3] === 0x47
    && bytes[4] === 0x0d
    && bytes[5] === 0x0a
    && bytes[6] === 0x1a
    && bytes[7] === 0x0a;
}

function hasJPEGSignature(bytes: Uint8Array): boolean {
  return bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff;
}

function hasWebPSignature(bytes: Uint8Array): boolean {
  return bytes.length >= 12
    && String.fromCharCode(...bytes.slice(0, 4)) === "RIFF"
    && String.fromCharCode(...bytes.slice(8, 12)) === "WEBP";
}

function signatureMatches(type: string, bytes: Uint8Array): boolean {
  switch (type) {
    case "image/png": return hasPNGSignature(bytes);
    case "image/jpeg": return hasJPEGSignature(bytes);
    case "image/webp": return hasWebPSignature(bytes);
    default: return false;
  }
}

async function imageDimensions(file: File): Promise<{ width: number; height: number }> {
  const source = URL.createObjectURL(file);
  try {
    return await new Promise((resolve, reject) => {
      const image = new Image();
      image.onload = () => resolve({ width: image.naturalWidth, height: image.naturalHeight });
      image.onerror = () => reject(new Error("The selected image cannot be decoded."));
      image.src = source;
    });
  } finally {
    URL.revokeObjectURL(source);
  }
}

async function validateCatalogImageFile(file: File): Promise<{ width: number; height: number }> {
  if (!acceptedTypes.has(file.type)) {
    throw new Error("Choose a PNG, JPEG, or WebP image.");
  }
  if (!file.size || file.size > CATALOG_ICON_MAX_BYTES) {
    throw new Error("Choose an image no larger than 1 MB.");
  }
  const bytes = new Uint8Array(await file.slice(0, 16).arrayBuffer());
  if (!signatureMatches(file.type, bytes)) {
    throw new Error("The file contents do not match its image type.");
  }
  const dimensions = await imageDimensions(file);
  if (!dimensions.width || !dimensions.height) {
    throw new Error("Choose a valid image with visible dimensions.");
  }
  if (dimensions.width > CATALOG_ICON_MAX_DIMENSION || dimensions.height > CATALOG_ICON_MAX_DIMENSION) {
    throw new Error("Choose an image up to 4096 × 4096 pixels.");
  }
  if (dimensions.width * dimensions.height > CATALOG_ICON_MAX_PIXELS) {
    throw new Error("Choose an image with at most 1 megapixel.");
  }
  return dimensions;
}

function fileSize(size: number): string {
  return `${Math.max(1, Math.ceil(size / 1024))} KB`;
}

/**
 * Project presentation supports only a locally selected raster File.  Its preview
 * is an object URL that is revoked whenever the draft changes or unmounts; stored
 * previews always use the same-origin asset endpoint through `CatalogIcon`.
 */
export function ProjectImageIconPicker({
  home,
  projectId,
  token,
  intent,
  assetRevision,
  disabled = false,
  pending = false,
  onIntentChange,
}: {
  home?: string;
  projectId?: string;
  token?: CatalogIconToken | null;
  intent: ProjectImageIntent;
  assetRevision?: string | number;
  disabled?: boolean;
  pending?: boolean;
  onIntentChange: (intent: ProjectImageIntent) => void;
}) {
  const { t } = useI18n();
  const inputRef = useRef<HTMLInputElement>(null);
  const inputId = useId();
  const statusId = useId();
  const [error, setError] = useState("");
  const [dimensions, setDimensions] = useState<{ width: number; height: number } | null>(null);
  const [previewURL, setPreviewURL] = useState<string>();
  const selectedFile = intent.kind === "upload" ? intent.file : undefined;

  useEffect(() => {
    if (!selectedFile) {
      setPreviewURL(undefined);
      setDimensions(null);
      return;
    }
    const next = URL.createObjectURL(selectedFile);
    setPreviewURL(next);
    return () => URL.revokeObjectURL(next);
  }, [selectedFile]);

  const chooseFile = async (file?: File) => {
    if (!file) return;
    setError("");
    try {
      const nextDimensions = await validateCatalogImageFile(file);
      setDimensions(nextDimensions);
      onIntentChange({ kind: "upload", file });
    } catch (cause) {
      setDimensions(null);
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (inputRef.current) inputRef.current.value = "";
    }
  };

  const clear = () => {
    setError("");
    setDimensions(null);
    // A draft for a not-yet-created project has no persisted asset to clear.
    onIntentChange(projectId ? { kind: "clear" } : { kind: "unchanged" });
  };

  const remoteURL = intent.kind === "unchanged"
    ? catalogIconAssetURL(home, "project", projectId, assetRevision)
    : undefined;
  const hasDraft = intent.kind === "upload";
  const hasClear = intent.kind === "clear";
  const canChange = !disabled && !pending;

  return (
    <div className="flex flex-wrap items-center gap-3" aria-describedby={statusId}>
      <span className="grid size-12 shrink-0 place-items-center overflow-hidden rounded-lg border bg-muted/25 text-brand">
        {previewURL && hasDraft ? (
          <img src={previewURL} alt="" className="size-full object-cover" />
        ) : (
          <CatalogIcon
            icon={token}
            assetURL={hasClear ? undefined : remoteURL}
            className="size-5"
            label={t("Project icon preview", "项目图标预览")}
          />
        )}
      </span>
      <div className="min-w-52 flex-1">
        <input
          ref={inputRef}
          id={inputId}
          type="file"
          className="sr-only"
          accept="image/png,image/jpeg,image/webp"
          disabled={!canChange}
          aria-label={t("Upload project image", "上传项目图片")}
          aria-invalid={Boolean(error)}
          aria-describedby={statusId}
          onChange={(event) => void chooseFile(event.currentTarget.files?.[0])}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!canChange}
            aria-describedby={statusId}
            onClick={() => inputRef.current?.click()}
          >
            {pending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <ImagePlus data-icon="inline-start" />}
            {hasDraft ? t("Replace image", "替换图片") : t("Upload image", "上传图片")}
          </Button>
          {(hasDraft || projectId) && (
            <Button type="button" variant="ghost" size="sm" disabled={!canChange} onClick={clear}>
              {hasDraft ? <X data-icon="inline-start" /> : <RotateCcw data-icon="inline-start" />}
              {hasDraft ? t("Remove selection", "移除所选图片") : t("Remove image", "移除图片")}
            </Button>
          )}
        </div>
        <p id={statusId} className="mt-1 text-xs text-muted-foreground" aria-live="polite">
          {hasDraft && selectedFile
            ? `${selectedFile.name} · ${fileSize(selectedFile.size)}${dimensions ? ` · ${dimensions.width} × ${dimensions.height}` : ""}`
            : hasClear
              ? t("The uploaded image will be removed when you save.", "保存后将移除已上传图片。")
              : t("PNG, JPEG, or WebP only · up to 1 MB, 4096 px per side, and 1 megapixel.", "仅限 PNG、JPEG 或 WebP · 最大 1 MB、单边 4096 像素、总计 100 万像素。")}
        </p>
        {error && <p role="alert" className="mt-1 text-xs text-destructive">{error}</p>}
      </div>
    </div>
  );
}
