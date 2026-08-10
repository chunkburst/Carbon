import { useEffect, useMemo, useState } from "react";
import { Loader2, Plus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import * as api from "@/lib/api";
import { closeCaptureWindow } from "@/lib/desktop";
import { useCurrentClusterRoot } from "@/lib/clusters";
import { useCluster } from "@/lib/queries";
import { lastWorkspace, registerWorkspace, useWorkspaces } from "@/lib/workspaces";
import { useI18n } from "@/lib/i18n";

type CaptureProject = api.ClusterProject & { slug: string };

// CaptureView is the body of the global quick-add window (#capture). It intentionally
// offers only members of the current cluster, never every historical local workspace.
export function CaptureView() {
  const clusterRoot = useCurrentClusterRoot();
  const { data: cluster, isLoading } = useCluster(clusterRoot);
  const workspaces = useWorkspaces();
  const [path, setPath] = useState("");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const { t } = useI18n();

  useEffect(() => {
    cluster?.projects.forEach((project) => registerWorkspace(project.path, { makeCurrent: false }));
  }, [cluster]);

  const projects = useMemo<CaptureProject[]>(() => {
    if (!cluster) return [];
    return cluster.projects.flatMap((project) => {
      const workspace = workspaces.find((entry) => entry.path === project.path);
      return workspace ? [{ ...project, slug: workspace.slug }] : [];
    });
  }, [cluster, workspaces]);

  useEffect(() => {
    if (projects.length === 0) {
      setPath("");
      return;
    }
    setPath((current) => {
      if (projects.some((project) => project.path === current)) return current;
      const recentPath = lastWorkspace()?.path;
      return projects.find((project) => project.path === recentPath)?.path ?? projects[0].path;
    });
  }, [projects]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") void closeCaptureWindow();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const submit = async () => {
    const taskTitle = title.trim();
    if (!taskTitle || !path || busy) return;
    setBusy(true);
    try {
      await api.createTask(path, { title: taskTitle, body: body.trim() || undefined });
      toast.success(t("Task created", "任务已创建"));
      void closeCaptureWindow();
    } catch (cause) {
      setBusy(false);
      toast.error(cause instanceof Error ? cause.message : t("Could not create task", "无法创建任务"));
    }
  };

  if (!clusterRoot || (!cluster && !isLoading)) {
    return (
      <div className="flex h-screen items-center justify-center bg-background p-6 text-center text-sm text-muted-foreground">
        {t(
          "Open a project cluster in Carbon first, then quick-add will capture into one of its projects.",
          "请先在 Carbon 中打开项目集群，然后才能快速添加任务。",
        )}
      </div>
    );
  }

  if (isLoading || !cluster) {
    return (
      <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">
        <Loader2 data-icon="loader" className="size-4 animate-spin" />
      </div>
    );
  }

  if (projects.length === 0) {
    return (
      <div className="flex h-screen items-center justify-center bg-background p-6 text-center text-sm text-muted-foreground">
        {t(
          "Add a project to the current cluster before using quick-add.",
          "请先向当前项目集群添加项目，再使用快速添加。",
        )}
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col gap-2.5 bg-background p-3 text-foreground">
      <div className="flex items-center gap-2">
        <Input
          autoFocus
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          onKeyDown={(event) => event.key === "Enter" && !event.shiftKey && void submit()}
          placeholder={t("Quick add a task…", "快速添加任务…")}
          className="h-9"
        />
        {projects.length > 1 && (
          <Select value={path} onValueChange={setPath}>
            <SelectTrigger className="h-9 w-40 shrink-0">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {projects.map((project) => (
                  <SelectItem key={`${project.id}:${project.path}`} value={project.path}>
                    {project.name}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        )}
      </div>
      <Textarea
        value={body}
        onChange={(event) => setBody(event.target.value)}
        onKeyDown={(event) =>
          event.key === "Enter" && (event.metaKey || event.ctrlKey) ? void submit() : undefined
        }
        placeholder={t("Details (optional) — ⌘↵ to add", "详情（可选）— ⌘↵ 添加")}
        className="min-h-0 flex-1 resize-none text-sm"
      />
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={() => void closeCaptureWindow()}>
          {t("Cancel", "取消")}
        </Button>
        <Button size="sm" onClick={() => void submit()} disabled={!title.trim() || !path || busy}>
          {busy ? (
            <Loader2 data-icon="inline-start" className="animate-spin" />
          ) : (
            <Plus data-icon="inline-start" />
          )}
          {t("Add task", "添加任务")}
        </Button>
      </div>
    </div>
  );
}
