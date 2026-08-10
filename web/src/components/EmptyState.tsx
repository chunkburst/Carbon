import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";

// EmptyState is the shared "nothing here" panel used across list / board / graph.
export function EmptyState({
  icon: Icon,
  title,
  message,
  action,
  secondaryAction,
}: {
  icon: LucideIcon;
  title: string;
  message?: string;
  action?: { label: string; icon?: LucideIcon; onClick: () => void };
  secondaryAction?: { label: string; onClick: () => void };
}) {
  return (
    <Empty className="h-full rounded-none border-0 p-6">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle className="text-sm">{title}</EmptyTitle>
        {message && <EmptyDescription>{message}</EmptyDescription>}
      </EmptyHeader>
      {(action || secondaryAction) && (
        <EmptyContent className="flex-row justify-center gap-2">
          {action && (
            <Button size="sm" onClick={action.onClick}>
              {action.icon && <action.icon data-icon="inline-start" />} {action.label}
            </Button>
          )}
          {secondaryAction && (
            <Button variant="outline" size="sm" onClick={secondaryAction.onClick}>
              {secondaryAction.label}
            </Button>
          )}
        </EmptyContent>
      )}
    </Empty>
  );
}
