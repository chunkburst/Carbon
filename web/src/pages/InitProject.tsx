import { useState } from "react";
import { ArrowLeft, Loader2, Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useInitRepo } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import type { Status } from "@/lib/api";

export function InitProject({
  path,
  status,
  onChangeFolder,
}: {
  path: string;
  status: Status;
  onChangeFolder: () => void;
}) {
  const { t } = useI18n();
  const [prefix, setPrefix] = useState(status.suggestedPrefix);
  const init = useInitRepo(path);

  return (
    <div className="flex h-full items-center justify-center bg-background p-6 text-foreground">
      <div className="w-full max-w-md">
        <button
          onClick={onChangeFolder}
          className="mb-4 flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-4" /> {t("Switch project cluster", "切换项目集群")}
        </button>

        <div className="rounded-xl border bg-card p-6 text-card-foreground shadow-xs">
          <div className="flex items-center gap-3">
            <span className="grid size-9 place-items-center rounded-lg bg-brand/10 text-brand">
              <Sparkles className="size-5" />
            </span>
            <div>
              <h1 className="text-lg font-semibold tracking-tight">{t("Initialize Carbon", "初始化 Carbon")}</h1>
              <p className="text-sm text-muted-foreground">{t("No workspace here yet.", "这里还没有工作区。")}</p>
            </div>
          </div>

          <p className="mt-4 truncate font-mono text-xs text-muted-foreground">{status.root}</p>

          <div className="mt-5 grid gap-1.5">
            <Label htmlFor="prefix">{t("Task ID prefix", "任务 ID 前缀")}</Label>
            <Input
              id="prefix"
              value={prefix}
              onChange={(e) => setPrefix(e.target.value.toUpperCase())}
              onKeyDown={(e) => e.key === "Enter" && init.mutate(prefix.trim())}
              placeholder="PROJ"
              className="font-mono"
            />
            <p className="text-xs text-muted-foreground">
              {t("Tasks will be created as ", "任务将创建为")}
              <span className="font-mono">{(prefix || "PROJ") + "-001"}</span>.
            </p>
          </div>

          <Button
            className="mt-5 w-full"
            disabled={init.isPending}
            onClick={() => init.mutate(prefix.trim())}
          >
            {init.isPending && <Loader2 className="animate-spin" />}
            {init.isPending ? t("Initializing…", "正在初始化…") : t("Initialize workspace", "初始化工作区")}
          </Button>
        </div>
      </div>
    </div>
  );
}
