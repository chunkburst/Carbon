import { useEffect, useState } from "react";
import { FolderSearch, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";
import { isTauri, pickFolder } from "@/lib/tauri";

export function AddProjectDialog({
  open,
  onOpenChange,
  onAdd,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdd: (path: string) => Promise<void>;
}) {
  const { t } = useI18n();
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setPath("");
      setError(null);
    }
  }, [open]);

  const submit = async (candidate = path) => {
    const target = candidate.trim();
    if (!target || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onAdd(target);
      onOpenChange(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const browse = async () => {
    const picked = await pickFolder();
    if (!picked) return;
    setPath(picked);
    await submit(picked);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("Add project", "添加项目")}</DialogTitle>
          <DialogDescription>
            {t(
              "Register an existing project in this cluster. Its files stay where they are.",
              "将已有项目登记到此集群，项目文件会保留在原位置。",
            )}
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <FieldGroup className="gap-4">
            <Field data-invalid={!!error}>
              <FieldLabel htmlFor="project-path">{t("Project folder", "项目文件夹")}</FieldLabel>
              <Input
                id="project-path"
                autoFocus
                value={path}
                onChange={(event) => setPath(event.target.value)}
                placeholder={t("/path/to/project", "/项目/路径")}
                aria-invalid={!!error}
                className="font-mono text-sm"
              />
              <FieldError>{error}</FieldError>
            </Field>
          </FieldGroup>
          <DialogFooter>
            {isTauri() && (
              <Button type="button" variant="outline" disabled={busy} onClick={() => void browse()}>
                <FolderSearch data-icon="inline-start" />
                {t("Choose folder…", "选择文件夹…")}
              </Button>
            )}
            <Button type="submit" disabled={!path.trim() || busy}>
              {busy && <Loader2 data-icon="inline-start" className="animate-spin" />}
              {t("Add project", "添加项目")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
