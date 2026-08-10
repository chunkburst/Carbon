import { useEffect, useRef } from "react";
import {
  Bell,
  CircleAlert,
  CircleDot,
  Clock3,
  GitBranch,
  ScanEye,
  Sparkles,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  useCarbonNotifications,
  useNotifications,
  type CarbonNotificationOptions,
  type Notif,
} from "@/lib/notifications";
import type { CarbonScopeInput } from "@/lib/carbon-api";
import { cn, timeAgo } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

const ICON: Record<Notif["kind"], { Icon: typeof Bell; cls: string }> = {
  ready: { Icon: Sparkles, cls: "text-brand" },
  blocked: { Icon: GitBranch, cls: "text-muted-foreground" },
  failed: { Icon: CircleAlert, cls: "text-destructive" },
  assigned: { Icon: CircleDot, cls: "text-foreground" },
  review: { Icon: ScanEye, cls: "text-brand" },
  lease_expiring: { Icon: Clock3, cls: "text-warning" },
};

export function NotificationBell({
  path,
  actor,
  onOpenTask,
}: {
  path: string;
  actor?: string;
  onOpenTask: (id: string) => void;
}) {
  const inbox = useNotifications(path, actor);
  // Legacy workspaces only know their active path, so retain the original
  // id-only callback at this boundary.
  return <NotificationBellMenu inbox={inbox} onOpenTask={(notification) => onOpenTask(notification.taskId)} />;
}

export function CarbonNotificationBell({
  scope,
  actor,
  onOpenTask,
  options,
}: {
  scope: CarbonScopeInput;
  actor?: string;
  onOpenTask: (notification: Notif) => void;
  options?: CarbonNotificationOptions;
}) {
  const inbox = useCarbonNotifications(scope, actor, options);
  return <NotificationBellMenu inbox={inbox} onOpenTask={onOpenTask} />;
}

function NotificationBellMenu({
  inbox,
  onOpenTask,
}: {
  inbox: ReturnType<typeof useNotifications>;
  onOpenTask: (notification: Notif) => void;
}) {
  const { items, unread, markAllRead, markRead, clear } = inbox;
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const { t } = useI18n();
  useEffect(() => () => clearTimeout(timer.current), []);

  const onOpenChange = (open: boolean) => {
    clearTimeout(timer.current);
    if (open && unread) timer.current = setTimeout(markAllRead, 1500);
  };

  return (
    <DropdownMenu onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <button
          aria-label={t("Notifications", "通知")}
          className="relative grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
        >
          <Bell className="size-4" />
          {unread > 0 && (
            <span className="absolute -top-0.5 -right-0.5 grid h-3.5 min-w-3.5 place-items-center rounded-full bg-brand px-0.5 text-[9px] font-medium text-brand-foreground">
              {unread > 9 ? "9+" : unread}
            </span>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <div className="flex items-center justify-between px-2 py-1.5">
          <span className="text-sm font-medium">{t("Inbox", "收件箱")}</span>
          {items.length > 0 && (
            <button onClick={clear} className="text-xs text-muted-foreground hover:text-foreground">
              {t("Clear", "清空")}
            </button>
          )}
        </div>
        <DropdownMenuSeparator />
        {items.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">
            {t("You're all caught up", "已全部处理完毕")}
          </div>
        ) : (
          <div className="max-h-80 overflow-y-auto">
            {items.map((n) => {
              const { Icon, cls } = ICON[n.kind];
              return (
                <DropdownMenuItem
                  key={n.key}
                  onSelect={() => {
                    markRead(n.key);
                    onOpenTask(n);
                  }}
                  className="items-start gap-2"
                >
                  <Icon className={cn("mt-0.5 size-3.5 shrink-0", cls)} />
                  <span className="min-w-0 flex-1">
                    <span className={cn("block text-sm", !n.read && "font-medium")}>{n.text}</span>
                    <span className="text-xs text-muted-foreground">{timeAgo(new Date(n.at).toISOString())}</span>
                  </span>
                  {!n.read && <span className="mt-1.5 size-1.5 shrink-0 rounded-full bg-brand" />}
                </DropdownMenuItem>
              );
            })}
          </div>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
