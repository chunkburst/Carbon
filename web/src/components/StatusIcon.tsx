import { CircleCheck, CircleDashed, CircleDot, CircleX } from "lucide-react";
import { cn } from "@/lib/utils";

// StatusIcon renders a Linear-style glyph derived from the workspace's status semantics:
// initial = dashed, closed = check (or X for cancel), any active middle state = brand dot.
export function StatusIcon({
  status,
  closed = [],
  initial,
  className,
}: {
  status: string;
  closed?: string[];
  initial?: string;
  className?: string;
}) {
  const cls = cn("size-4 shrink-0", className);
  if (closed.includes(status)) {
    if (/cancel/i.test(status)) return <CircleX className={cn(cls, "text-muted-foreground/70")} />;
    return <CircleCheck className={cn(cls, "text-success")} />;
  }
  if (status === initial) return <CircleDashed className={cn(cls, "text-muted-foreground")} />;
  return <CircleDot className={cn(cls, "text-brand")} />;
}
