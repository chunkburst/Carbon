import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { createPortal } from "react-dom";
import { Grip, Maximize2, Minus, Pin, PinOff, X } from "lucide-react";
import { CarbonAnimationBoard } from "@/components/CarbonAnimationBoard";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import type { AnimationBoardStyle, FloatingBoardPreference } from "@/lib/personalization";
import { cn } from "@/lib/utils";
import type { Status, Task } from "@/lib/api";

type Bounds = { x: number; y: number; width: number; height: number };
type Gesture = {
  kind: "move" | "resize";
  pointerId: number;
  startX: number;
  startY: number;
  bounds: Bounds;
};

const EDGE_GAP = 12;
const MIN_WIDTH = 360;
const MIN_HEIGHT = 280;

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(Math.max(value, minimum), Math.max(minimum, maximum));
}

function fitToViewport(preference: FloatingBoardPreference): Bounds {
  const viewportWidth = Math.max(320, window.innerWidth);
  const viewportHeight = Math.max(240, window.innerHeight);
  const minimumWidth = Math.min(MIN_WIDTH, viewportWidth - EDGE_GAP * 2);
  const minimumHeight = Math.min(MIN_HEIGHT, viewportHeight - EDGE_GAP * 2);
  const width = clamp(preference.width, minimumWidth, viewportWidth - EDGE_GAP * 2);
  const height = clamp(preference.height, minimumHeight, viewportHeight - EDGE_GAP * 2);
  const defaultX = viewportWidth - width - 24;
  const defaultY = viewportHeight - height - 24;
  return {
    x: clamp(preference.x ?? defaultX, EDGE_GAP, viewportWidth - width - EDGE_GAP),
    y: clamp(preference.y ?? defaultY, EDGE_GAP, viewportHeight - height - EDGE_GAP),
    width,
    height,
  };
}

type CarbonFloatingBoardProps = {
  projectKey: string;
  preference: FloatingBoardPreference;
  style: AnimationBoardStyle;
  tasks: Task[];
  status: Status;
  prefersReducedMotion: boolean;
  onPreferenceChange: (preference: FloatingBoardPreference) => void;
  onOpenTask: (task: Task) => void;
};

/**
 * A non-modal, project-safe picture-in-picture surface. It stays inside Carbon so
 * it can reuse the current scoped query and live updates without opening a second
 * webview or widening the task boundary.
 */
export function CarbonFloatingBoard({
  projectKey,
  preference,
  style,
  tasks,
  status,
  prefersReducedMotion,
  onPreferenceChange,
  onOpenTask,
}: CarbonFloatingBoardProps) {
  const { t } = useI18n();
  const [bounds, setBounds] = useState<Bounds>(() => fitToViewport(preference));
  const gestureRef = useRef<Gesture | null>(null);

  const commit = useCallback((nextBounds: Bounds, patch: Partial<FloatingBoardPreference> = {}) => {
    const fitted = fitToViewport({ ...preference, ...patch, ...nextBounds });
    setBounds(fitted);
    onPreferenceChange({ ...preference, ...patch, ...fitted });
  }, [onPreferenceChange, preference]);

  useEffect(() => {
    setBounds(fitToViewport(preference));
  }, [preference]);

  useEffect(() => {
    const fit = () => commit(fitToViewport({ ...preference, ...bounds }));
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [bounds, commit, preference]);

  const startGesture = useCallback((kind: Gesture["kind"], event: PointerEvent<HTMLElement>) => {
    if (preference.pinned || event.button !== 0) return;
    if (kind === "move" && event.target instanceof Element && event.target.closest("button")) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    gestureRef.current = {
      kind,
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      bounds,
    };
  }, [bounds, preference.pinned]);

  const moveGesture = useCallback((event: PointerEvent<HTMLElement>) => {
    const gesture = gestureRef.current;
    if (!gesture || gesture.pointerId !== event.pointerId) return;
    const dx = event.clientX - gesture.startX;
    const dy = event.clientY - gesture.startY;
    if (gesture.kind === "move") {
      setBounds((current) => fitToViewport({
        ...preference,
        ...current,
        x: gesture.bounds.x + dx,
        y: gesture.bounds.y + dy,
      }));
      return;
    }
    setBounds((current) => fitToViewport({
      ...preference,
      ...current,
      width: gesture.bounds.width + dx,
      height: gesture.bounds.height + dy,
    }));
  }, [preference]);

  const endGesture = useCallback((event: PointerEvent<HTMLElement>) => {
    const gesture = gestureRef.current;
    if (!gesture || gesture.pointerId !== event.pointerId) return;
    gestureRef.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    commit(bounds);
  }, [bounds, commit]);

  const nudge = useCallback((event: KeyboardEvent<HTMLElement>, kind: Gesture["kind"]) => {
    if (event.target !== event.currentTarget) return;
    if (preference.pinned || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
    event.preventDefault();
    const amount = event.shiftKey ? 32 : 8;
    const horizontal = event.key === "ArrowLeft" ? -amount : event.key === "ArrowRight" ? amount : 0;
    const vertical = event.key === "ArrowUp" ? -amount : event.key === "ArrowDown" ? amount : 0;
    commit(kind === "move"
      ? { ...bounds, x: bounds.x + horizontal, y: bounds.y + vertical }
      : { ...bounds, width: bounds.width + horizontal, height: bounds.height + vertical });
  }, [bounds, commit, preference.pinned]);

  if (!preference.open || typeof document === "undefined") return null;

  const height = preference.minimized ? 46 : bounds.height;
  return createPortal(
    <section
      role="region"
      aria-label={t("Floating work window", "悬浮工作窗")}
      className={cn(
        "fixed z-40 flex overflow-hidden rounded-2xl border bg-popover/96 text-popover-foreground shadow-2xl ring-1 ring-foreground/10 supports-backdrop-filter:backdrop-blur-xl",
        "motion-safe:animate-in motion-safe:fade-in-0 motion-safe:zoom-in-95 motion-safe:slide-in-from-bottom-2",
        preference.minimized ? "flex-row" : "flex-col",
      )}
      style={{ left: bounds.x, top: bounds.y, width: bounds.width, height }}
    >
      <header
        tabIndex={preference.pinned ? -1 : 0}
        aria-label={t("Drag floating board", "拖动悬浮看板")}
        className={cn(
          "flex h-11 shrink-0 select-none items-center gap-2 border-b bg-muted/55 px-2 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          preference.pinned ? "cursor-default" : "cursor-grab active:cursor-grabbing",
        )}
        onPointerDown={(event) => startGesture("move", event)}
        onPointerMove={moveGesture}
        onPointerUp={endGesture}
        onPointerCancel={endGesture}
        onKeyDown={(event) => nudge(event, "move")}
      >
        <Grip className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="truncate text-xs font-semibold tracking-wide">{t("Work in progress", "工作进度")}</p>
          {!preference.minimized && <p className="truncate text-[10px] text-muted-foreground">{status.prefix} · {tasks.length} {t("tasks", "个任务")}</p>}
        </div>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={preference.pinned ? t("Unlock position", "解除位置锁定") : t("Lock position", "锁定位置")}
              aria-pressed={preference.pinned}
              onClick={() => onPreferenceChange({ ...preference, ...bounds, pinned: !preference.pinned })}
            >
              {preference.pinned ? <PinOff /> : <Pin />}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{preference.pinned ? t("Unlock position", "解除位置锁定") : t("Lock position", "锁定位置")}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={preference.minimized ? t("Restore floating board", "还原悬浮看板") : t("Minimize floating board", "最小化悬浮看板")}
              onClick={() => onPreferenceChange({ ...preference, ...bounds, minimized: !preference.minimized })}
            >
              {preference.minimized ? <Maximize2 /> : <Minus />}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{preference.minimized ? t("Restore", "还原") : t("Minimize", "最小化")}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("Close floating board", "关闭悬浮看板")}
              onClick={() => onPreferenceChange({ ...preference, ...bounds, open: false })}
            >
              <X />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("Close", "关闭")}</TooltipContent>
        </Tooltip>
      </header>

      {!preference.minimized && (
        <div className="flex min-h-0 flex-1 overflow-hidden">
          <CarbonAnimationBoard
            projectKey={projectKey}
            tasks={tasks}
            status={status}
            style={style}
            onOpenTask={onOpenTask}
            prefersReducedMotion={prefersReducedMotion}
            compact
          />
        </div>
      )}

      {!preference.minimized && !preference.pinned && (
        <span
          role="separator"
          tabIndex={0}
          aria-orientation="horizontal"
          aria-label={t("Resize floating board", "调整悬浮看板大小")}
          className="absolute bottom-0 right-0 size-5 cursor-nwse-resize outline-none after:absolute after:bottom-1 after:right-1 after:size-2 after:border-b-2 after:border-r-2 after:border-muted-foreground/60 focus-visible:ring-2 focus-visible:ring-ring"
          onPointerDown={(event) => startGesture("resize", event)}
          onPointerMove={moveGesture}
          onPointerUp={endGesture}
          onPointerCancel={endGesture}
          onKeyDown={(event) => nudge(event, "resize")}
        />
      )}
    </section>,
    document.body,
  );
}
