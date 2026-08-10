import { useState } from "react";
import { Bot, Check, ChevronDown, Copy, GitBranch, ListChecks, Plus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import type { Status } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

export function Onboarding({ status, onNewTask }: { status: Status; onNewTask: () => void }) {
  const [copied, setCopied] = useState(false);
  const snippet = `claude mcp add carbon -- "${status.root}/bin/carbon" serve --actor agent:claude-1 --repo "${status.root}"`;
  const { t } = useI18n();
  const points = [
    { icon: ListChecks, text: t("Tasks live in your repo as files — git is the history.", "任务以文件形式保存在仓库中，Git 就是历史记录。") },
    { icon: GitBranch, text: t("Gates keep work honest: deps must close before start, checks before done.", "关卡确保工作可靠：开始前依赖必须关闭，完成前检查必须通过。") },
    { icon: Bot, text: t("You and your AI agent share one task list and rule-set.", "你与 AI 智能体共享同一个任务列表和规则集。") },
  ];

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(snippet);
      setCopied(true);
      toast.success(t("Copied — paste it in your terminal", "已复制——请粘贴到终端中"));
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error(t("Couldn't copy", "无法复制"));
    }
  };

  return (
    <div className="flex h-full flex-col items-center justify-center px-6 py-10">
      <div className="w-full max-w-md">
        <h1 className="text-lg font-semibold">{t("Welcome to {project}", "欢迎使用 {project}", { project: status.prefix })}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {t("A repo-native task tracker you run with your AI agent.", "与你的 AI 智能体一起使用的仓库原生任务追踪器。")}
        </p>

        <ul className="mt-5 space-y-2.5">
          {points.map(({ icon: Icon, text }) => (
            <li key={text} className="flex items-start gap-2.5 text-[13px]">
              <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <span>{text}</span>
            </li>
          ))}
        </ul>

        <Button className="mt-6 w-full" onClick={onNewTask}>
          <Plus /> {t("Create your first task", "创建第一个任务")}
        </Button>

        <Collapsible className="mt-3">
          <CollapsibleTrigger className="group flex w-full items-center justify-between rounded-md px-1 py-1.5 text-[13px] text-muted-foreground hover:text-foreground">
            <span className="flex items-center gap-2">
              <Bot className="size-4" /> {t("Connect your AI agent", "连接你的 AI 智能体")}
            </span>
            <ChevronDown className="size-4 transition-transform group-data-[state=open]:rotate-180" />
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-2">
            <p className="px-1 pb-2 text-xs text-muted-foreground">
              {t("Give Claude Code the same tasks over MCP — run this once:", "通过 MCP 将相同任务提供给 Claude Code——运行一次以下命令：")}
            </p>
            <div className="relative rounded-md border bg-muted/40 p-2.5 pr-9">
              <code className="block break-all font-mono text-[11px] leading-relaxed">{snippet}</code>
              <button
                onClick={copy}
                aria-label={t("Copy command", "复制命令")}
                className="absolute top-1.5 right-1.5 grid size-6 place-items-center rounded text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
              >
                {copied ? <Check className={cn("size-3.5 text-success")} /> : <Copy className="size-3.5" />}
              </button>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </div>
  );
}
