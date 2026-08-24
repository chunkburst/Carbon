import { useEffect, useState } from "react";
import { ArchiveRestore, CalendarClock, CheckCircle2, CloudCog, DatabaseBackup, Loader2, ShieldAlert, ShieldCheck, Trash2, Upload } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDeleteDialog } from "@/components/ConfirmDeleteDialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import type { CarbonBackupConfig, CarbonBackupLocalSchedule, CarbonBackupManifest, CarbonBackupProfile, CarbonHomeScope } from "@/lib/carbon-api";
import {
  useBackupConfig,
  useBackupStatus,
  useBackupSnapshots,
  usePruneBackupSnapshots,
  useRestorePlan,
  useRunBackupNow,
  useSaveBackupConfig,
  useSetBackupContinuousAuthorization,
  useUploadBackupSnapshot,
  useVerifyBackup,
} from "@/lib/queries";
import { timeAgo } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

const blank: CarbonBackupConfig = {
  profile: { backend: "s3", enabled: false, continuousAuthorization: false, encryption: false },
  local: { enabled: true, intervalHours: 6, onStart: true, keepLast: 30, keepDays: 30 },
};

function normalizeConfig(value: CarbonBackupConfig): CarbonBackupConfig {
  return {
    profile: { ...blank.profile, ...value.profile },
    local: { ...blank.local, ...value.local },
  };
}

function snapshotSize(manifest: CarbonBackupManifest): number {
  return (manifest.files ?? []).reduce((total, file) => total + Math.max(0, file.size || 0), 0);
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  return `${value >= 10 ? value.toFixed(1) : value.toFixed(2)} ${unit}`;
}

function validLocalSchedule(local: CarbonBackupLocalSchedule): boolean {
  return Number.isInteger(local.intervalHours) && local.intervalHours >= 1 && local.intervalHours <= 744
    && Number.isInteger(local.keepLast) && local.keepLast >= 1 && local.keepLast <= 100_000
    && Number.isInteger(local.keepDays) && local.keepDays >= 1 && local.keepDays <= 100_000;
}

export function BackupSettings({ home }: { home: string }) {
  const { t } = useI18n();
  const scope: CarbonHomeScope = { home };
  const configQuery = useBackupConfig(scope);
  const statusQuery = useBackupStatus(scope);
  const snapshotsQuery = useBackupSnapshots(scope);
  const save = useSaveBackupConfig(scope);
  const runNow = useRunBackupNow(scope);
  const prune = usePruneBackupSnapshots(scope);
  const setContinuousAuthorization = useSetBackupContinuousAuthorization(scope);
  const upload = useUploadBackupSnapshot(scope);
  const verify = useVerifyBackup(scope);
  const restorePlan = useRestorePlan(scope);
  const [config, setConfig] = useState<CarbonBackupConfig>(blank);
  const [authorizationChange, setAuthorizationChange] = useState<boolean | null>(null);
  const [pruneConfirmationOpen, setPruneConfirmationOpen] = useState(false);

  useEffect(() => {
    if (configQuery.data?.available && configQuery.data.data) {
      setConfig(normalizeConfig(configQuery.data.data));
    }
  }, [configQuery.data]);

  const available = configQuery.data?.available === true;
  const snapshots = snapshotsQuery.data?.available ? snapshotsQuery.data.data.snapshots ?? [] : [];
  const verification = verify.data?.available ? verify.data.data : undefined;
  const restore = restorePlan.data?.available ? restorePlan.data.data : undefined;
  const backupStatus = statusQuery.data?.available ? statusQuery.data.data : undefined;
  const savedProfile = configQuery.data?.available ? configQuery.data.data?.profile : undefined;
  const canUpload = savedProfile?.enabled === true && savedProfile.encryption === true && Boolean(savedProfile.encryptionKeyRef?.trim());
  const canAuthorizeContinuously = config.profile.enabled && config.profile.encryption
    && Boolean(config.profile.bucket?.trim()) && Boolean(config.profile.region?.trim())
    && Boolean(config.profile.credentialRef?.trim()) && Boolean(config.profile.encryptionKeyRef?.trim());
  const localIsValid = validLocalSchedule(config.local);
  const updateProfile = (next: Partial<CarbonBackupProfile>) => {
    setConfig((current) => ({ ...current, profile: { ...current.profile, ...next } }));
  };
  const updateLocal = (next: Partial<CarbonBackupLocalSchedule>) => {
    setConfig((current) => ({ ...current, local: { ...current.local, ...next } }));
  };

  const confirmUpload = (snapshotId: string, manifest: CarbonBackupManifest) => {
    const files = manifest.files ?? [];
    const destination = [savedProfile?.backend?.toUpperCase(), savedProfile?.bucket, savedProfile?.region]
      .filter(Boolean)
      .join(" / ");
    if (!window.confirm(t(
      "Upload {count} files ({size}) to {destination}? Carbon encrypts and verifies the remote copy. Review the file list first.",
      "确认将 {count} 个文件（{size}）上传到 {destination} 吗？Carbon 会加密并验证远程副本，请先审阅文件列表。",
      { count: files.length, size: formatBytes(snapshotSize(manifest)), destination: destination || t("the configured destination", "已配置的远程目标") },
    ))) return;
    upload.mutate(snapshotId);
  };

  const localStatus = backupStatus?.local;
  const displayedInterval = localStatus?.intervalHours ?? config.local.intervalHours;
  const displayedKeepLast = localStatus?.keepLast ?? config.local.keepLast;
  const displayedKeepDays = localStatus?.keepDays ?? config.local.keepDays;
  const displayedOnStart = localStatus?.onStart ?? config.local.onStart;

  return (
    <section className="pt-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><DatabaseBackup />{t("Backup & snapshots", "备份和快照")}</CardTitle>
          <CardDescription>{t("Automatic recovery snapshots stay on this device. After explicit authorization, scheduled runs encrypt and sync to the configured remote destination.", "自动恢复快照保留在本机；明确授权后，计划任务会加密并同步到已配置的远程目标。")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {!available && !configQuery.isLoading && (
            <Alert>
              <AlertTitle>{t("Backups are not available in this Carbon version", "当前 Carbon 版本暂不支持备份")}</AlertTitle>
              <AlertDescription>{t("Update Carbon and try again.", "请更新 Carbon 后重试。")}</AlertDescription>
            </Alert>
          )}
          <Alert>
            <CalendarClock className="size-4" />
            <AlertTitle>{t("Local snapshots come first", "本地快照始终优先")}</AlertTitle>
            <AlertDescription>{t("Every scheduled run creates and verifies its local snapshot first. With the default or an upgraded profile, remote authorization is off and this remains zero-network; unchanged content reuses its manifest.", "每次计划运行都会先创建并验证本地快照。默认或升级后的配置均未授权远程同步，因此完全不访问网络；内容未变时会复用既有清单。")}</AlertDescription>
          </Alert>
          <Alert>
            <ShieldAlert className="size-4" />
            <AlertTitle>{t("Remote authorization is deliberate", "远程授权需要明确确认")}</AlertTitle>
            <AlertDescription>{t("A configured enabled encrypted destination needs a separate confirmation. After authorization, scheduled runs automatically encrypt and sync on the local cadence; authorization itself does not upload now.", "已配置、启用且加密的远程目标需要单独确认。授权后按本地周期自动加密同步；授权操作本身不会立即上传。")}</AlertDescription>
          </Alert>
          {backupStatus && localStatus && (
            <div className="grid gap-2 rounded-lg border bg-muted/20 p-3 text-sm sm:grid-cols-2">
              <p><span className="font-medium">{t("Local schedule", "本地计划")}: </span>{localStatus.enabled ? t("enabled", "已启用") : t("manual only", "仅手动")}</p>
              <p><span className="font-medium">{t("Cadence", "频率")}: </span>{t("every {hours}h{onStart}", "每 {hours} 小时{onStart}", { hours: displayedInterval, onStart: displayedOnStart ? t(" + on start", " + 启动时") : "" })}</p>
              <p><span className="font-medium">{t("Local retention", "本地保留")}: </span>{t("newest {count} and {days} days", "最新 {count} 个且保留 {days} 天", { count: displayedKeepLast, days: displayedKeepDays })}</p>
              <p><span className="font-medium">{t("Last local run", "最近本地运行")}: </span>{timeAgo(localStatus.lastSuccessAt) || localStatus.lastSuccessAt || t("not yet", "尚未运行")}</p>
              <p><span className="font-medium">{t("Last prune", "最近清理")}: </span>{localStatus.lastPruneAt ? `${timeAgo(localStatus.lastPruneAt) || localStatus.lastPruneAt} · ${t("{count} manifests", "{count} 个清单", { count: localStatus.lastPruned ?? 0 })}` : t("not yet", "尚未运行")}</p>
              <p><span className="font-medium">{t("Remote profile", "远程配置")}: </span>{backupStatus.remote.configured ? t("configured", "已配置") : t("not configured", "未配置")}{backupStatus.remote.continuousAuthorization ? ` · ${t("continuous authorization on", "持续授权已开启")}` : ""}</p>
              <p><span className="font-medium">{t("Last remote sync", "最近远程同步")}: </span>{backupStatus.remote.lastRemoteSnapshotAt ? `${timeAgo(backupStatus.remote.lastRemoteSnapshotAt) || backupStatus.remote.lastRemoteSnapshotAt}${backupStatus.remote.lastRemoteSnapshotId ? ` · ${backupStatus.remote.lastRemoteSnapshotId.slice(0, 12)}` : ""}` : t("not yet", "尚未运行")}</p>
              <p className={backupStatus.remote.lastRemoteError ? "text-destructive" : undefined}><span className="font-medium">{t("Last remote failure", "最近远程失败")}: </span>{backupStatus.remote.lastRemoteFailureAt ? `${timeAgo(backupStatus.remote.lastRemoteFailureAt) || backupStatus.remote.lastRemoteFailureAt}${backupStatus.remote.lastRemoteError ? ` · ${backupStatus.remote.lastRemoteError}` : ""}` : t("none", "无")}</p>
            </div>
          )}

          <FieldGroup className="gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <div><p className="text-sm font-medium">{t("Automatic local snapshots", "自动本地快照")}</p><p className="text-xs text-muted-foreground">{t("When disabled, Run now remains available but stays local-only. Scheduled encrypted syncs also stop because there is no scheduled run.", "关闭后仍可手动立即运行，但始终仅在本地执行；没有计划运行时也不会自动加密同步。")}</p></div>
              <Switch checked={config.local.enabled} disabled={!available} onCheckedChange={(enabled) => updateLocal({ enabled })} />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="backup-interval">{t("Snapshot interval (hours)", "快照间隔（小时）")}</FieldLabel>
                <Input id="backup-interval" type="number" min={1} max={744} value={config.local.intervalHours} disabled={!available} onChange={(event) => updateLocal({ intervalHours: Number(event.target.value) })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-keep-last">{t("Keep newest snapshots", "保留最新快照")}</FieldLabel>
                <Input id="backup-keep-last" type="number" min={1} max={100000} value={config.local.keepLast} disabled={!available} onChange={(event) => updateLocal({ keepLast: Number(event.target.value) })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-keep-days">{t("Keep snapshots for days", "按天保留快照")}</FieldLabel>
                <Input id="backup-keep-days" type="number" min={1} max={100000} value={config.local.keepDays} disabled={!available} onChange={(event) => updateLocal({ keepDays: Number(event.target.value) })} />
                <FieldDescription>{t("A snapshot is kept if it satisfies either retention rule, which avoids unexpected deletion.", "快照只要满足任一保留规则就会留下，避免意外删除。")}</FieldDescription>
              </Field>
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3 sm:mt-6">
                <div><p className="text-sm font-medium">{t("Run on application start", "应用启动时运行")}</p><p className="text-xs text-muted-foreground">{t("Creates or reuses a local snapshot after the host starts, then syncs it only when remote authorization is active.", "主程序启动后创建或复用本地快照；仅在远程授权已启用时才会随后同步。")}</p></div>
                <Switch checked={config.local.onStart} disabled={!available} onCheckedChange={(onStart) => updateLocal({ onStart })} />
              </div>
            </div>
            {!localIsValid && <p className="text-xs text-destructive">{t("Use 1–744 hours and whole retention values from 1 to 100,000.", "间隔应为 1–744 小时，保留值应为 1–100,000 的整数。")}</p>}

            <div className="grid gap-3 border-t pt-4 sm:grid-cols-2">
              <Field>
                <FieldLabel>{t("Backend", "后端")}</FieldLabel>
                <Select value={config.profile.backend} disabled={!available} onValueChange={(backend) => updateProfile({ backend })}>
                  <SelectTrigger className="w-full">{config.profile.backend.toUpperCase()}</SelectTrigger>
                  <SelectContent><SelectItem value="s3">S3</SelectItem><SelectItem value="cos">COS</SelectItem></SelectContent>
                </Select>
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-bucket">{t("Bucket", "存储桶")}</FieldLabel>
                <Input id="backup-bucket" value={config.profile.bucket ?? ""} disabled={!available} onChange={(event) => updateProfile({ bucket: event.target.value })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-region">{t("Region", "区域")}</FieldLabel>
                <Input id="backup-region" value={config.profile.region ?? ""} disabled={!available} onChange={(event) => updateProfile({ region: event.target.value })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-endpoint">{t("Service address", "服务地址")}</FieldLabel>
                <Input id="backup-endpoint" value={config.profile.endpoint ?? ""} disabled={!available} onChange={(event) => updateProfile({ endpoint: event.target.value })} placeholder="https://…" />
              </Field>
              <Field>
                <FieldLabel htmlFor="backup-prefix">{t("Prefix", "前缀")}</FieldLabel>
                <Input id="backup-prefix" value={config.profile.prefix ?? ""} disabled={!available} onChange={(event) => updateProfile({ prefix: event.target.value })} placeholder="carbon" />
              </Field>
              <Field className="sm:col-span-2">
                <FieldLabel htmlFor="backup-credential-ref">{t("Credential reference", "凭据引用")}</FieldLabel>
                <Input id="backup-credential-ref" value={config.profile.credentialRef ?? ""} disabled={!available} onChange={(event) => updateProfile({ credentialRef: event.target.value })} placeholder={config.profile.backend === "cos" ? "cos-env://PREFIX" : "aws-default://"} />
                <FieldDescription>{t("S3: aws-default://, aws-profile://NAME, or env://PREFIX. COS: cos-env://PREFIX. Never paste access keys or secrets.", "S3 可用 aws-default://、aws-profile://NAME 或 env://PREFIX；COS 可用 cos-env://PREFIX。请勿粘贴访问密钥或机密。")}</FieldDescription>
              </Field>
              <Field className="sm:col-span-2">
                <FieldLabel htmlFor="backup-encryption-key-ref">{t("Encryption key reference", "加密密钥引用")}</FieldLabel>
                <Input id="backup-encryption-key-ref" value={config.profile.encryptionKeyRef ?? ""} disabled={!available} onChange={(event) => updateProfile({ encryptionKeyRef: event.target.value })} placeholder="env://CARBON_BACKUP_KEY" />
                <FieldDescription>{t("Use an env://VAR key reference only; never enter a raw encryption key.", "仅使用 env://VAR 形式的密钥引用；绝不输入明文加密密钥。")}</FieldDescription>
              </Field>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <div><p className="text-sm font-medium">{t("Enable remote backup", "启用远程备份")}</p><p className="text-xs text-muted-foreground">{t("Allows explicit encrypted uploads. Saving never makes a network request; scheduled sync additionally requires separate authorization.", "允许显式加密上传；保存不会发起网络请求，计划同步还需要单独授权。")}</p></div>
              <Switch checked={config.profile.enabled} disabled={!available} onCheckedChange={(enabled) => updateProfile({ enabled })} />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <div><p className="text-sm font-medium">{t("Path-style addressing", "路径式寻址")}</p><p className="text-xs text-muted-foreground">{t("Use path-style URLs for compatible object storage.", "为兼容对象存储使用路径式 URL。")}</p></div>
              <Switch checked={config.profile.usePathStyle === true} disabled={!available} onCheckedChange={(usePathStyle) => updateProfile({ usePathStyle })} />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <div><p className="text-sm font-medium">{t("Allow an insecure address", "允许使用不安全地址")}</p><p className="text-xs text-muted-foreground">{t("Use this only with a trusted local test service.", "仅用于可信的本地测试服务。")}</p></div>
              <Switch checked={config.profile.allowInsecureEndpoint === true} disabled={!available} onCheckedChange={(allowInsecureEndpoint) => updateProfile({ allowInsecureEndpoint })} />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <div><p className="text-sm font-medium">{t("Encryption", "加密")}</p><p className="text-xs text-muted-foreground">{t("Remote publication always requires an external key reference.", "远程发布始终需要外部密钥引用。")}</p></div>
              <Switch checked={config.profile.encryption === true} disabled={!available} onCheckedChange={(encryption) => updateProfile({ encryption })} />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/40 p-3">
              <div><p className="text-sm font-medium">{t("Continuous remote authorization", "持续远程授权")}</p><p className="text-xs text-muted-foreground">{t("Requires a separate danger confirmation. After authorization, scheduled runs automatically encrypt and sync on the local cadence; it does not upload now.", "需要单独的高风险确认。授权后按本地周期自动加密同步；不会立即上传。")}</p></div>
              <Switch checked={config.profile.continuousAuthorization === true} disabled={!available || !canAuthorizeContinuously || setContinuousAuthorization.isPending} onCheckedChange={(enabled) => setAuthorizationChange(enabled)} />
            </div>
            {!canAuthorizeContinuously && <p className="text-xs text-muted-foreground">{t("Save an enabled encrypted remote destination before changing continuous authorization.", "请先保存一个已启用且加密的远程目标，再更改持续授权。")}</p>}
          </FieldGroup>

          <div className="flex flex-wrap gap-2">
            <Button disabled={!available || save.isPending || !localIsValid} onClick={() => save.mutate(config)}>{save.isPending && <Loader2 className="animate-spin" />}<CloudCog data-icon="inline-start" />{t("Save configuration", "保存配置")}</Button>
            <Button variant="outline" disabled={!available || runNow.isPending} onClick={() => runNow.mutate()}>{runNow.isPending && <Loader2 className="animate-spin" />}<DatabaseBackup data-icon="inline-start" />{t("Run local snapshot now", "立即运行本地快照")}</Button>
            <Button variant="outline" disabled={!available || prune.isPending} onClick={() => setPruneConfirmationOpen(true)}><Trash2 data-icon="inline-start" />{t("Prune local history", "清理本地历史")}</Button>
          </div>
          {verification && <p className="flex items-center gap-2 text-sm text-muted-foreground"><CheckCircle2 className={verification.verified ? "text-success" : "text-destructive"} />{verification.verified ? t("Snapshot verified", "快照已验证") : t("Snapshot verification failed", "快照验证失败")}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader><CardTitle>{t("Snapshots", "快照")}</CardTitle><CardDescription>{t("Verify a snapshot or create a read-only restore plan; neither action restores files.", "验证快照或创建只读恢复计划；两者均不会恢复文件。")}</CardDescription></CardHeader>
        <CardContent>
          {available && snapshots.length > 0 && !canUpload && (
            <p className="mb-3 text-sm text-muted-foreground">{t("To show encrypted upload, save an enabled remote profile with encryption and an encryption key reference.", "若要显示加密上传，请先保存一个已启用、开启加密且包含加密密钥引用的远程配置。")}</p>
          )}
          {available && snapshots.length ? (
            <div className="flex flex-col gap-2">
              {snapshots.map(({ snapshot, manifest }) => (
                <div key={snapshot.id} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3">
                  <div className="min-w-0 flex-1">
                    <p className="font-mono text-sm">{snapshot.id}</p>
                    <p className="text-xs text-muted-foreground">
                      {timeAgo(manifest.created_at) || manifest.created_at || t("unknown time", "未知时间")}
                      {` · ${(manifest.files ?? []).length} ${t("files", "个文件")} · ${formatBytes(snapshotSize(manifest))}`}
                    </p>
                    <details className="mt-2 text-xs text-muted-foreground">
                      <summary className="cursor-pointer select-none">{t("Review snapshot file list", "审阅快照文件清单")}</summary>
                      <div className="mt-1 max-h-40 overflow-auto rounded border bg-muted/20 p-2 font-mono">
                        {(manifest.files ?? []).map((file) => <p key={file.path} className="break-all">{file.path} · {formatBytes(file.size)}</p>)}
                      </div>
                    </details>
                  </div>
                  <div className="flex gap-2">
                    {canUpload && (
                      <Button size="sm" disabled={upload.isPending} onClick={() => confirmUpload(snapshot.id, manifest)}>
                        {upload.isPending && <Loader2 className="animate-spin" />}
                        <Upload data-icon="inline-start" />
                        {t("Explicit encrypted upload", "显式加密上传")}
                      </Button>
                    )}
                    <Button size="sm" variant="outline" disabled={verify.isPending} onClick={() => verify.mutate(snapshot.id)}><ShieldCheck data-icon="inline-start" />{t("Verify", "验证")}</Button>
                    <Button size="sm" variant="outline" disabled={restorePlan.isPending} onClick={() => restorePlan.mutate(snapshot.id)}><ArchiveRestore data-icon="inline-start" />{t("Create restore plan", "创建恢复计划")}</Button>
                  </div>
                </div>
              ))}
            </div>
          ) : available ? <p className="text-sm text-muted-foreground">{t("No snapshots yet.", "暂无快照。")}</p> : null}
          {restore && <Alert className="mt-3"><AlertTitle>{t("Restore plan", "恢复计划")}</AlertTitle><AlertDescription>{restore.restore || t("Plan generated; no restore has been performed.", "计划已生成；尚未执行恢复。")} {typeof restore.files === "number" && `(${restore.files} ${t("files", "个文件")})`}</AlertDescription></Alert>}
        </CardContent>
      </Card>

      <ConfirmDeleteDialog
        open={authorizationChange !== null}
        onOpenChange={(open) => { if (!open) setAuthorizationChange(null); }}
        title={authorizationChange ? t("Authorize continuous remote backup?", "授权持续远程备份？") : t("Revoke continuous remote authorization?", "撤销持续远程授权？")}
        description={authorizationChange
          ? t("Future scheduled runs will encrypt and sync verified local snapshots on the saved local cadence. This does not upload now; every remote publication still uses encryption and read-back verification.", "未来的计划运行会按已保存的本地周期加密并同步已验证的本地快照。这不会立即上传；每次远程发布仍会加密并读回验证。")
          : t("This immediately blocks future scheduled provider calls. Existing remote objects are not deleted.", "这会立即阻止后续计划任务访问远程提供方；不会删除现有远程对象。")}
        confirmLabel={authorizationChange ? t("Authorize", "确认授权") : t("Revoke authorization", "撤销授权")}
        pending={setContinuousAuthorization.isPending}
        onConfirm={() => {
          if (authorizationChange === null) return;
          setContinuousAuthorization.mutate(authorizationChange, { onSuccess: () => setAuthorizationChange(null) });
        }}
      />
      <ConfirmDeleteDialog
        open={pruneConfirmationOpen}
        onOpenChange={setPruneConfirmationOpen}
        title={t("Prune local snapshot history?", "清理本地快照历史？")}
        description={t("This removes expired local snapshot manifests according to the saved retention policy. It never contacts or deletes remote storage, and shared immutable objects remain protected.", "这会按已保存的保留策略移除过期的本地快照清单；不会访问或删除远程存储，并会保护共享的不可变对象。")}
        confirmLabel={t("Prune local history", "清理本地历史")}
        pending={prune.isPending}
        onConfirm={() => prune.mutate(undefined, { onSuccess: () => setPruneConfirmationOpen(false) })}
      />
    </section>
  );
}
