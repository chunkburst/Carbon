import { useEffect, useState } from "react";
import { Save } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { TaskMetadata } from "@/components/TaskMetadata";
import { hasCarbonFeature } from "@/lib/carbon-api";
import { useCarbonCapabilities, usePatchCarbonTask } from "@/lib/queries";
import type { Task } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { CARBON_IMPORTANCE, CARBON_TASK_TYPES, carbonImportanceLabel, carbonTaskTypeLabel } from "@/lib/task-labels";

export function CarbonTaskProperties({ path, task }: { path: string; task: Task }) {
  const { t } = useI18n();
  const { data: capabilityResult } = useCarbonCapabilities(path);
  const patch = usePatchCarbonTask(path);
  const available = capabilityResult?.available === true && hasCarbonFeature(capabilityResult.data, "task_fields");
  const [projectId, setProjectId] = useState(task.projectId ?? "");
  const [type, setType] = useState(task.type ?? "");
  const [importance, setImportance] = useState(task.importance ?? "");

  useEffect(() => {
    setProjectId(task.projectId ?? "");
    setType(task.type ?? "");
    setImportance(task.importance ?? "");
  }, [task.projectId, task.type, task.importance]);

  const changed =
    projectId !== (task.projectId ?? "") ||
    type !== (task.type ?? "") ||
    importance !== (task.importance ?? "");

  return (
    <section className="border-t pt-5">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{t("Carbon fields", "Carbon 字段")}</h3>
        <TaskMetadata task={task} compact />
      </div>
      {!available && (
        <Alert className="mb-4">
          <AlertTitle>{t("Carbon stable v2 not available", "Carbon stable v2 不可用")}</AlertTitle>
          <AlertDescription>
            {t("These fields are intentionally read-only until this sidecar advertises the Carbon API.", "在当前 sidecar 声明 Carbon API 前，这些字段会保持只读。")}
          </AlertDescription>
        </Alert>
      )}
      <FieldGroup className="gap-3">
        <Field>
          <FieldLabel htmlFor="carbon-project-id">{t("Project ID", "项目 ID")}</FieldLabel>
          <Input id="carbon-project-id" value={projectId} disabled={!available} onChange={(event) => setProjectId(event.target.value)} />
        </Field>
        <Field>
          <FieldLabel htmlFor="carbon-type">{t("Type", "类型")}</FieldLabel>
          <Input
            id="carbon-type"
            list="carbon-task-types-detail"
            value={type}
            disabled={!available}
            onChange={(event) => setType(event.target.value)}
          />
          <datalist id="carbon-task-types-detail">
            {CARBON_TASK_TYPES.map((value) => (
              <option key={value} value={value}>{carbonTaskTypeLabel(value, t)}</option>
            ))}
          </datalist>
        </Field>
        <Field>
          <FieldLabel>{t("Importance", "重要性")}</FieldLabel>
          <Select value={importance || "none"} disabled={!available} onValueChange={(value) => setImportance(value === "none" ? "" : value)}>
            <SelectTrigger className="w-full">{importance ? carbonImportanceLabel(importance, t) : t("No importance", "无重要性")}</SelectTrigger>
            <SelectContent>
              <SelectItem value="none">{t("No importance", "无重要性")}</SelectItem>
              {CARBON_IMPORTANCE.map((value) => (
                <SelectItem key={value} value={value}>{carbonImportanceLabel(value, t)}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </FieldGroup>
      <Button
        className="mt-3 w-full"
        size="sm"
        disabled={!available || !changed || patch.isPending}
        onClick={() => patch.mutate({ id: task.id, fields: { projectId: projectId || undefined, type: type || undefined, importance: importance || undefined, expectedVersion: task.version } })}
      >
        <Save data-icon="inline-start" />
        {t("Save Carbon fields", "保存 Carbon 字段")}
      </Button>
    </section>
  );
}
