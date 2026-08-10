import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"
import { currentLanguage, translate } from "@/lib/i18n"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** A tiny stable hash used for token-backed label tones (never random per render). */
export function labelTone(value: string): string {
  let hash = 0
  for (let i = 0; i < value.length; i++) hash = (hash * 31 + value.charCodeAt(i)) | 0
  return `carbon-label-tone-${Math.abs(hash) % 6}`
}

/** Initials for an actor like "agent:claude-1" -> "CL", "human:shah" -> "SH". */
export function initials(actor?: string): string {
  if (!actor) return "?"
  const name = actor.includes(":") ? actor.slice(actor.indexOf(":") + 1) : actor
  const letters = name.replace(/[^a-zA-Z]/g, "")
  return (letters.slice(0, 2) || name.slice(0, 2)).toUpperCase()
}

/** Whether an actor is an AI agent, a human, or unknown — by the "agent:"/"human:" prefix. */
export function actorKind(actor?: string): "agent" | "human" | null {
  if (!actor) return null
  if (actor.startsWith("agent:")) return "agent"
  if (actor.startsWith("human:")) return "human"
  return null
}

const KNOWN_STATUS_LABELS: Record<string, [string, string]> = {
  active: ["Active", "进行中"],
  awaiting_review: ["Awaiting review", "等待审核"],
  backlog: ["Backlog", "待办"],
  blocked: ["Blocked", "已阻塞"],
  canceled: ["Canceled", "已取消"],
  cancelled: ["Cancelled", "已取消"],
  closed: ["Closed", "已关闭"],
  complete: ["Complete", "已完成"],
  completed: ["Completed", "已完成"],
  done: ["Done", "已完成"],
  in_progress: ["In progress", "进行中"],
  in_review: ["In review", "审核中"],
  open: ["Open", "待处理"],
  ready: ["Ready", "就绪"],
  review: ["Review", "审核中"],
  stalled: ["Stalled", "已停滞"],
  to_do: ["To do", "待办"],
  todo: ["To do", "待办"],
}

/** Human label for a known status string; custom statuses keep their existing label. */
export function statusLabel(status: string): string {
  const normalized = status.trim().toLowerCase().replace(/[\s-]+/g, "_")
  const known = KNOWN_STATUS_LABELS[normalized]
  if (known) return translate(known[0], known[1])
  const s = status.replace(/_/g, " ")
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** Compact relative time: "just now", "5m ago", "3h ago", "2d ago", "4w ago". */
export function timeAgo(iso?: string): string {
  if (!iso) return ""
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ""
  const language = currentLanguage()
  const tr = (en: string, zh: string, vars?: Record<string, string | number>) =>
    translate(en, zh, vars, language)
  const s = Math.floor((Date.now() - t) / 1000)
  if (s < 45) return tr("just now", "刚刚")
  const m = Math.floor(s / 60)
  if (m < 60) return tr("{count}m ago", "{count} 分钟前", { count: m })
  const h = Math.floor(m / 60)
  if (h < 24) return tr("{count}h ago", "{count} 小时前", { count: h })
  const d = Math.floor(h / 24)
  if (d < 7) return tr("{count}d ago", "{count} 天前", { count: d })
  const w = Math.floor(d / 7)
  if (w < 5) return tr("{count}w ago", "{count} 周前", { count: w })
  const mo = Math.floor(d / 30)
  if (mo < 12) return tr("{count}mo ago", "{count} 个月前", { count: mo })
  return tr("{count}y ago", "{count} 年前", { count: Math.floor(d / 365) })
}
