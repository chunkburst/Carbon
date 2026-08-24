import { useEffect, useState } from "react";
import { Loader2, Music2, Volume2, VolumeX } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import { importNotificationWav, isTauri, playManagedNotificationSound } from "@/lib/desktop";
import {
  getActiveNotificationPreferenceScope,
  loadNotificationPreferences,
  notificationPreferenceStorageKey,
  onActiveNotificationPreferenceScopeChange,
  saveNotificationPreferences,
  type NotificationPreferences,
  type NotificationPreferenceScope,
} from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";

const DAYS = [
  ["0", "S", "日"], ["1", "M", "一"], ["2", "T", "二"], ["3", "W", "三"], ["4", "T", "四"], ["5", "F", "五"], ["6", "S", "六"],
] as const;

async function playDefaultPreview(): Promise<void> {
  const Audio = window.AudioContext ?? window.webkitAudioContext;
  if (!Audio) throw new Error("This browser cannot play an audio preview.");

  const context = new Audio();
  try {
    if (context.state === "suspended") await context.resume();
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.frequency.value = 660;
    gain.gain.setValueAtTime(0.04, context.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.0001, context.currentTime + 0.16);
    oscillator.connect(gain).connect(context.destination);
    const completed = new Promise<void>((resolve) => oscillator.addEventListener("ended", () => resolve(), { once: true }));
    oscillator.start();
    oscillator.stop(context.currentTime + 0.16);
    await completed;
  } finally {
    await context.close().catch(() => undefined);
  }
}

/**
 * `scope` is optional for compatibility with the legacy settings dialog. Carbon
 * notification hooks publish the active home scope, so that unchanged caller
 * still receives a home-namespaced preference record. New callers should pass
 * the manifest `homeId` explicitly when they already have it.
 */
export function NotificationPreferences({ open, scope }: { open: boolean; scope?: NotificationPreferenceScope }) {
  const { t } = useI18n();
  const [scopeVersion, setScopeVersion] = useState(0);
  const activeScope = scope ?? getActiveNotificationPreferenceScope();
  const storageKey = notificationPreferenceStorageKey(activeScope);
  const [preferences, setPreferences] = useState<NotificationPreferences>(() => loadNotificationPreferences(activeScope));
  const [previewPending, setPreviewPending] = useState(false);

  useEffect(() => onActiveNotificationPreferenceScopeChange(() => setScopeVersion((version) => version + 1)), []);

  useEffect(() => {
    if (open) setPreferences(loadNotificationPreferences(scope ?? getActiveNotificationPreferenceScope()));
  }, [open, scope, scopeVersion, storageKey]);

  const update = (next: Partial<NotificationPreferences>) => {
    setPreferences((current) => {
      const value = { ...current, ...next };
      return saveNotificationPreferences(value, scope ?? getActiveNotificationPreferenceScope(), {
        // Non-DND edits must not accidentally overwrite a tray-menu DND change.
        syncDoNotDisturb: Object.hasOwn(next, "doNotDisturb"),
      });
    });
  };

  const previewSound = async () => {
    if (preferences.sound === "off" || previewPending) return;
    setPreviewPending(true);
    try {
      if (preferences.sound === "custom") {
        if (!preferences.customSoundRef) throw new Error(t("Choose a custom WAV before previewing it.", "请先选择自定义 WAV 再试听。"));
        await playManagedNotificationSound(preferences.customSoundRef);
      } else {
        await playDefaultPreview();
      }
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t("Could not preview the notification sound", "无法试听通知声音"));
    } finally {
      setPreviewPending(false);
    }
  };

  return (
    <section className="pt-4">
      <Separator />
      <div className="pt-4">
        <h3 className="text-sm font-medium">{t("Inbox delivery", "收件箱投递")}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t("Schedules apply only to the main window.", "调度只在主窗口生效。")}</p>
      </div>
      <div className="mt-3 flex flex-col gap-3">
        <ToggleRow label={t("Ready", "就绪")} checked={preferences.events.ready} onChange={(ready) => update({ events: { ...preferences.events, ready } })} />
        <ToggleRow label={t("Awaiting review", "等待审核")} checked={preferences.events.review} onChange={(review) => update({ events: { ...preferences.events, review } })} />
        <ToggleRow label={t("Check failed", "检查失败")} checked={preferences.events.check_failed} onChange={(check_failed) => update({ events: { ...preferences.events, check_failed } })} />
        <ToggleRow label={t("Handoff window expiring", "接手期限即将到期")} checked={preferences.events.lease_expiring} onChange={(lease_expiring) => update({ events: { ...preferences.events, lease_expiring } })} />
        <ToggleRow label={t("Do not disturb", "免打扰")} checked={preferences.doNotDisturb} onChange={(doNotDisturb) => update({ doNotDisturb })} />
      </div>
      <div className="mt-4 flex flex-col gap-2">
        <span className="text-sm font-medium">{t("Active days", "活跃星期")}</span>
        <ToggleGroup
          type="multiple"
          value={preferences.activeDays.map(String)}
          variant="outline"
          spacing={0}
          onValueChange={(days) => update({ activeDays: days.map(Number).filter((day) => Number.isInteger(day) && day >= 0 && day <= 6) })}
        >
          {DAYS.map(([value, en, zh]) => {
            const label = t(en, zh);
            return <ToggleGroupItem key={value} value={value} aria-label={label}>{label}</ToggleGroupItem>;
          })}
        </ToggleGroup>
      </div>
      <div className="mt-4 grid grid-cols-2 gap-3">
        <label className="flex flex-col gap-1 text-sm font-medium">
          {t("From", "开始")}
          <Input type="time" value={preferences.start} onChange={(event) => update({ start: event.target.value })} />
        </label>
        <label className="flex flex-col gap-1 text-sm font-medium">
          {t("Until", "结束")}
          <Input type="time" value={preferences.end} onChange={(event) => update({ end: event.target.value })} />
        </label>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{t("Equal times mean all day; an end before the start safely crosses midnight.", "相同时间表示全天；结束早于开始会安全地跨越午夜。")}</p>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Select value={preferences.sound} onValueChange={(sound) => update({ sound: sound as NotificationPreferences["sound"] })}>
          <SelectTrigger className="w-40">{preferences.sound === "off" ? t("Sound off", "关闭声音") : preferences.sound === "custom" ? t("Custom sound", "自定义声音") : t("Default sound", "默认声音")}</SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="off"><VolumeX />{t("Sound off", "关闭声音")}</SelectItem>
              <SelectItem value="default"><Music2 />{t("Default", "默认")}</SelectItem>
              <SelectItem value="custom"><Music2 />{t("Custom WAV", "自定义 WAV")}</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="sm"
          disabled={!isTauri()}
          onClick={() => void importNotificationWav().then((reference) => reference && update({ sound: "custom", customSoundRef: reference }))}
        >
          <Music2 data-icon="inline-start" />
          {t("Choose WAV", "选择 WAV")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={preferences.sound === "off" || previewPending}
          onClick={() => void previewSound()}
        >
          {previewPending ? <Loader2 data-icon="inline-start" className="animate-spin" /> : <Volume2 data-icon="inline-start" />}
          {t("Preview", "试听")}
        </Button>
      </div>
      {!isTauri() && <p className="mt-1 text-xs text-muted-foreground">{t("Custom WAV import is managed by the desktop app; browser mode keeps no audio file copy.", "自定义 WAV 由桌面应用托管；浏览器模式不会复制音频文件。")}</p>}
    </section>
  );
}

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return <div className="flex items-center justify-between gap-3"><span className="text-sm">{label}</span><Switch checked={checked} onCheckedChange={onChange} /></div>;
}
