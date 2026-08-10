import { Bot, GitBranch, History, ListChecks, Terminal } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

export function HelpDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const { t } = useI18n();
  const sections = [
    {
      icon: ListChecks,
      title: t("Tasks & states", "任务与状态"),
      body: t(
        "Every task is a file in your repo. It moves through your configured states (e.g. backlog → in progress → in review → done). The id is assigned for you and never reused.",
        "每个任务都是仓库中的一个文件。它会经过你配置的状态（例如：待办 → 进行中 → 审核中 → 已完成）。系统会分配任务 ID，且永不复用。",
      ),
    },
    {
      icon: GitBranch,
      title: t("Two gates keep work honest", "两道关卡保证工作可靠"),
      body: t(
        "Dependencies: a task can't leave the backlog until everything it depends on is closed. Checks: a task can't be marked done until its checks pass (commands run automatically; manual checks you attest).",
        "依赖：任务所依赖的事项全部关闭后，才能离开待办。检查：所有检查通过后，任务才能标记为完成（命令检查会自动运行，手动检查由你确认）。",
      ),
    },
    {
      icon: Terminal,
      title: t("Checks run a shell", "检查会运行 Shell 命令"),
      body: t(
        "A command check runs in a POSIX shell (sh) — go test ./..., pytest -q && ruff check ., ./scripts/verify.sh. On Windows install Git Bash or WSL, or point CARBON_SHELL at a shell on your PATH.",
        "命令检查会在 POSIX shell（sh）中运行，例如 go test ./...、pytest -q && ruff check .、./scripts/verify.sh。在 Windows 上请安装 Git Bash 或 WSL，或将 CARBON_SHELL 指向 PATH 中的 Shell。",
      ),
    },
    {
      icon: Bot,
      title: t("You + your AI agent", "你与 AI 智能体"),
      body: t(
        "Humans and agents share one task list and one rule-set. Claim a task to take it; hand off by leaving it for the other. Connect Claude Code over MCP and it sees exactly what you see.",
        "人类和智能体共享同一个任务列表和规则集。认领任务即可接手，留给对方即可交接。通过 MCP 连接 Claude Code 后，它会看到和你相同的内容。",
      ),
    },
    {
      icon: History,
      title: t("Provenance", "过程记录"),
      body: t(
        "Each task keeps an append-only log of what happened and who did it — created, claimed, transitioned, notes, attestations — so the decision trail lives with the work.",
        "每个任务都会保留追加式日志，记录发生了什么以及由谁完成——创建、认领、状态变化、备注和确认——使决策过程与工作一同留存。",
      ),
    },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("How Carbon works", "Carbon 使用说明")}</DialogTitle>
          <DialogDescription>{t("A repo-native task tracker you run with your AI agent.", "与你的 AI 智能体一起使用的仓库原生任务追踪器。")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {sections.map(({ icon: Icon, title, body }) => (
            <div key={title} className="flex items-start gap-3">
              <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-md bg-muted text-muted-foreground">
                <Icon className="size-4" />
              </span>
              <div>
                <h3 className="text-sm font-medium">{title}</h3>
                <p className="mt-0.5 text-[13px] leading-relaxed text-muted-foreground">{body}</p>
              </div>
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
