import { CircleAlert, Signal, SignalHigh, SignalLow, SignalMedium } from "lucide-react";
import { cn } from "@/lib/utils";
import { translate } from "@/lib/i18n";

// Priority levels, highest first (for selects).
export const PRIORITIES = ["urgent", "high", "medium", "low"] as const;

const MAP: Record<string, { Icon: typeof Signal; cls: string }> = {
  urgent: { Icon: CircleAlert, cls: "text-destructive" },
  high: { Icon: SignalHigh, cls: "text-foreground" },
  medium: { Icon: SignalMedium, cls: "text-muted-foreground" },
  low: { Icon: SignalLow, cls: "text-muted-foreground" },
};

export function priorityLabel(p?: string): string {
  if (!p) return translate("No priority", "无优先级");
  const labels: Record<string, string> = {
    urgent: translate("Urgent", "紧急"),
    high: translate("High", "高"),
    medium: translate("Medium", "中"),
    low: translate("Low", "低"),
  };
  return labels[p] ?? p;
}

// PriorityIcon renders a signal-bar glyph; "none"/unknown shows a faint placeholder.
export function PriorityIcon({ priority, className }: { priority?: string; className?: string }) {
  const m = priority ? MAP[priority] : undefined;
  if (!m) return <Signal className={cn("size-3.5 text-muted-foreground/40", className)} />;
  return <m.Icon className={cn("size-3.5", m.cls, className)} />;
}
