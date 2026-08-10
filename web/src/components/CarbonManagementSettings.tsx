import { useEffect, useMemo, useState } from "react";
import { ListTree, Save, Tags } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import type { CarbonScope } from "@/lib/carbon-api";
import { useCarbonConfig, useCarbonTaskTypes, useCreateCarbonTaskType, useSaveCarbonConfig } from "@/lib/queries";
import { useI18n } from "@/lib/i18n";
import { carbonTaskTypeLabel } from "@/lib/task-labels";

const BUILT_IN_TYPES = ["foundation", "library", "patch", "extension", "plugin"] as const;

function clusterConfigScope(scope: CarbonScope): CarbonScope | undefined {
  if (!scope.home || !scope.clusterId) return undefined;
  return { home: scope.home, clusterId: scope.clusterId };
}

export function CarbonManagementSettings({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const clusterScope = clusterConfigScope(scope);
  if (!clusterScope) {
    return <Alert className="mt-4"><AlertTitle>{t("Cluster settings unavailable", "集群设置不可用")}</AlertTitle><AlertDescription>{t("Open a Carbon cluster before changing its retention or type catalog.", "请先打开 Carbon 集群，再修改其保留期限或类型目录。")}</AlertDescription></Alert>;
  }
  return (
    <section className="mt-4 space-y-4 border-t pt-4">
      <CarbonTrashRetention scope={clusterScope} />
      <CarbonTypeManager scope={clusterScope} />
    </section>
  );
}

function CarbonTrashRetention({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const config = useCarbonConfig(scope);
  const save = useSaveCarbonConfig(scope);
  const [days, setDays] = useState("30");

  useEffect(() => {
    if (config.data?.available) setDays(String(config.data.data.trashRetentionDays));
  }, [config.data]);

  const parsed = Number(days);
  const valid = Number.isInteger(parsed) && parsed >= 1 && parsed <= 3650;
  const current = config.data?.available ? config.data.data.trashRetentionDays : undefined;

  return (
    <div className="space-y-2">
      <div><p className="flex items-center gap-2 text-sm font-medium"><Save className="size-4" />{t("Trash retention", "垃圾站保留期限")}</p><p className="text-xs text-muted-foreground">{t("Expired-item cleanup is evaluated only when a new task enters Trash.", "仅在新任务进入垃圾站时触发过期回收检查。")}</p></div>
      {config.data?.available === false ? <p className="text-xs text-muted-foreground">{t("This sidecar does not expose Carbon configuration.", "此 sidecar 未提供 Carbon 配置接口。")}</p> : <div className="flex items-center gap-2"><Input type="number" min={1} max={3650} value={days} onChange={(event) => setDays(event.target.value)} className="h-8 w-24" aria-label={t("Retention days", "保留天数")} /><span className="text-sm text-muted-foreground">{t("days", "天")}</span><Button size="sm" variant="outline" disabled={!valid || save.isPending || parsed === current} onClick={() => save.mutate({ trashRetentionDays: parsed })}>{t("Save", "保存")}</Button></div>}
      {!valid && days !== "" && <p className="text-xs text-destructive">{t("Enter a whole number from 1 to 3650.", "请输入 1 到 3650 之间的整数。")}</p>}
    </div>
  );
}

function CarbonTypeManager({ scope }: { scope: CarbonScope }) {
  const { t } = useI18n();
  const types = useCarbonTaskTypes(scope);
  const create = useCreateCarbonTaskType(scope);
  const [key, setKey] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const custom = types.data?.available ? types.data.data.custom ?? [] : [];
  const allTypes = useMemo(() => {
    const registered = types.data?.available ? types.data.data.types ?? [] : [];
    return new Set([...BUILT_IN_TYPES, ...registered]);
  }, [types.data]);
  const normalizedKey = key.trim().toLowerCase();
  const duplicate = Boolean(normalizedKey && allTypes.has(normalizedKey));
  const canCreate = Boolean(normalizedKey && !duplicate && confirmed && !create.isPending && types.data?.available);

  return (
    <div className="space-y-3">
      <div><p className="flex items-center gap-2 text-sm font-medium"><Tags className="size-4" />{t("Task type catalog", "任务类型目录")}</p><p className="text-xs text-muted-foreground">{t("Prefer the standard types. Add a custom type only when it will be reused across this cluster.", "优先使用标准类型；仅在会在此集群中重复使用时添加自定义类型。")}</p></div>
      {types.data?.available === false ? <p className="text-xs text-muted-foreground">{t("This sidecar does not expose the task type catalog.", "此 sidecar 未提供任务类型目录。")}</p> : <>
        <div className="flex flex-wrap gap-1.5">
          {BUILT_IN_TYPES.map((type) => (
            <Badge key={type} variant="secondary" title={type}>
              {carbonTaskTypeLabel(type, t)}
            </Badge>
          ))}
        </div>
        {custom.length > 0 && <div className="space-y-1"><p className="text-xs font-medium text-muted-foreground">{t("Custom types", "自定义类型")}</p>{custom.map((type) => <div key={type.key} className="flex items-center justify-between rounded border px-2 py-1.5 text-sm"><span>{type.display_name || type.key}</span><code className="text-xs text-muted-foreground">{type.key}</code></div>)}</div>}
        <p className="flex items-start gap-2 text-xs text-muted-foreground"><ListTree className="mt-0.5 size-3.5 shrink-0" />{t("Built-in types are fixed. The current server exposes no deletion endpoint, so custom types are intentionally not deleted here.", "内置类型固定不可删除。当前服务端未提供删除接口，因此此处不会伪造删除自定义类型。")}</p>
        <div className="grid gap-2 rounded-lg border p-3 sm:grid-cols-2"><Input value={key} onChange={(event) => setKey(event.target.value)} placeholder={t("Reusable type key", "可复用类型键")} /><Input value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder={t("Display name (optional)", "显示名称（可选）")} /><label className="flex items-center gap-2 text-xs sm:col-span-2"><Checkbox checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />{t("I confirm this is a reusable cluster-wide type, not a one-off task label.", "我确认这是可复用的集群级类型，而不是一次性任务标签。")}</label><Button className="w-fit sm:col-span-2" size="sm" disabled={!canCreate} onClick={() => create.mutate({ key: normalizedKey, displayName: displayName.trim() || undefined }, { onSuccess: (result) => { if (result.available) { setKey(""); setDisplayName(""); setConfirmed(false); } } })}>{t("Add custom type", "添加自定义类型")}</Button></div>
        {duplicate && <p className="text-xs text-destructive">{t("That type already exists; use the existing catalog entry.", "该类型已存在；请使用现有目录条目。")}</p>}
      </>}
    </div>
  );
}
