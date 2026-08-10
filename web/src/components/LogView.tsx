import { useState } from "react";
import Anser from "anser";
import { Check, Copy, WrapText } from "lucide-react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import type { Run } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

// LogView renders a check run's captured output, escaping raw text before converting ANSI
// colors to HTML. It also provides a copy button and a soft-wrap toggle; the header shows exit code / duration / timeout.
export function LogView({ run }: { run: Run }) {
  const [wrap, setWrap] = useState(true);
  const [copied, setCopied] = useState(false);
  const { t } = useI18n();
  const output = run.output?.trimEnd() ?? "";
  const html = output ? Anser.ansiToHtml(Anser.escapeForHtml(output), { use_classes: false }) : "";

  const copy = () => {
    navigator.clipboard.writeText(output).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  };

  return (
    <div className="overflow-hidden rounded-md border bg-muted/40">
      <div className="flex items-center gap-3 border-b px-2.5 py-1 font-mono text-[11px] text-muted-foreground">
        <span className={cn(run.exit === 0 ? "text-success" : "text-destructive")}>
          {t("exit {code}", "退出码 {code}", { code: run.exit })}
        </span>
        {run.duration && <span>{run.duration}</span>}
        {run.timedout && <span className="text-destructive">{t("timed out", "已超时")}</span>}
        <span className="ml-auto flex items-center gap-0.5">
          <button
            onClick={() => setWrap((w) => !w)}
            aria-label={t("Toggle wrap", "切换自动换行")}
            className={cn(
              "grid size-5 place-items-center rounded hover:bg-foreground/10",
              wrap && "text-foreground",
            )}
          >
            <WrapText className="size-3" />
          </button>
          <button
            onClick={copy}
            aria-label={t("Copy output", "复制输出")}
            className="grid size-5 place-items-center rounded hover:bg-foreground/10"
          >
            {copied ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
          </button>
        </span>
      </div>
      <ScrollArea className="max-h-64">
        {output ? (
          <pre
            className={cn(
              "px-2.5 py-2 font-mono text-[11px] leading-relaxed",
              wrap ? "break-words whitespace-pre-wrap" : "whitespace-pre",
            )}
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ) : (
          <pre className="px-2.5 py-2 font-mono text-[11px] text-muted-foreground">{t("(no output)", "（无输出）")}</pre>
        )}
      </ScrollArea>
    </div>
  );
}
