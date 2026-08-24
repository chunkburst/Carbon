import { useEffect, useState } from "react";
import { Bot, CandlestickChart, FolderCog, PanelsTopLeft, Rows3 } from "lucide-react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Field, FieldContent, FieldDescription, FieldGroup, FieldTitle } from "@/components/ui/field";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { BackupSettings } from "@/components/BackupSettings";
import { CarbonManagementSettings } from "@/components/CarbonManagementSettings";
import { NotificationPreferences } from "@/components/NotificationPreferences";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  getAutostartMode,
  getMainDataHome,
  isPortable,
  isTauri,
  osNotifEnabled,
  setAutostartMode,
  setMainDataHome,
  setOsNotifEnabled,
  type AutostartMode,
} from "@/lib/desktop";
import { pickFolder } from "@/lib/tauri";
import type { CarbonScope } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";
import { displayName, useIdentity } from "@/lib/identity";
import { applyTheme, getTheme, type Theme } from "@/lib/theme";
import { ANIMATION_BOARD_STYLE_REGISTRY } from "@/lib/animation-board";
import {
  getAnimationBoardStyle,
  getTaskListPresentation,
  getProjectManagementPresentation,
  isAnimationBoardStyle,
  PERSONALIZATION_EVENT,
  setAnimationBoardStyle,
  setTaskListPresentation,
  setProjectManagementPresentation,
  type AnimationBoardStyle,
  type TaskListPresentation,
  type ProjectManagementPresentation,
} from "@/lib/personalization";
import { useSetCheckShell } from "@/lib/queries";

type SettingsTab = "general" | "notifications" | "data";

// SettingsDialog holds desktop preferences. In the browser it shows only a short note,
// since every toggle here drives a native capability.
export function SettingsDialog({
  open,
  onOpenChange,
  path,
  checkShell,
  carbonMode = false,
  carbonScope,
  notificationHomeId,
  showHomeBackup = false,
  onDataHomeChange,
  suggestedActor,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  path: string;
  checkShell?: string;
  carbonMode?: boolean;
  carbonScope?: CarbonScope;
  notificationHomeId?: string;
  showHomeBackup?: boolean;
  onDataHomeChange?: (path: string) => void;
  suggestedActor?: string;
}) {
  const { language, setLanguage, t } = useI18n();
  const { actor, setName } = useIdentity(suggestedActor);
  const desktop = isTauri();
  // Backups are a Home-management concern. A project/cluster workspace must opt
  // out even though its `carbonScope` contains the same Home identifier.
  const backupHome = showHomeBackup && carbonScope?.home && !carbonScope.clusterId && !carbonScope.projectId
    ? carbonScope.home
    : undefined;
  const [notifs, setNotifs] = useState(true);
  const [autostart, setAutostartState] = useState<AutostartMode>("off");
  const [changingAutostart, setChangingAutostart] = useState(false);
  const [portable, setPortable] = useState(false);
  const [dataHome, setDataHome] = useState<string>("");
  const [pendingDataHome, setPendingDataHome] = useState<string>();
  const [homeRestartRequired, setHomeRestartRequired] = useState(false);
  const [shell, setShell] = useState(checkShell ?? "");
  const [identityDraft, setIdentityDraft] = useState("");
  const [theme, setTheme] = useState<Theme>(getTheme);
  const [taskListPresentation, setTaskListPresentationState] = useState<TaskListPresentation>(getTaskListPresentation);
  const [animationBoardStyle, setAnimationBoardStyleState] = useState<AnimationBoardStyle>(getAnimationBoardStyle);
  const [projectManagementPresentation, setProjectManagementPresentationState] = useState<ProjectManagementPresentation>(getProjectManagementPresentation);
  const [activeTab, setActiveTab] = useState<SettingsTab>("general");
  const saveShell = useSetCheckShell(path);
  const shellDirty = shell.trim() !== (checkShell ?? "");
  const showDataTab = carbonMode && (desktop || Boolean(carbonScope) || Boolean(backupHome));

  useEffect(() => {
    if (open) setActiveTab("general");
  }, [open]);

  useEffect(() => {
    if (!showDataTab && activeTab === "data") setActiveTab("general");
  }, [activeTab, showDataTab]);

  useEffect(() => {
    if (!open || activeTab !== "general") return;
    setShell(checkShell ?? "");
    setIdentityDraft(displayName(actor));
    setTheme(getTheme());
    if (carbonMode) {
      setTaskListPresentationState(getTaskListPresentation());
      setAnimationBoardStyleState(getAnimationBoardStyle());
      setProjectManagementPresentationState(getProjectManagementPresentation());
    }
  }, [activeTab, actor, carbonMode, checkShell, open]);

  useEffect(() => {
    if (!carbonMode) return;
    const syncBoardPresentation = () => {
      setTaskListPresentationState(getTaskListPresentation());
      setAnimationBoardStyleState(getAnimationBoardStyle());
    };
    window.addEventListener(PERSONALIZATION_EVENT, syncBoardPresentation);
    window.addEventListener("storage", syncBoardPresentation);
    return () => {
      window.removeEventListener(PERSONALIZATION_EVENT, syncBoardPresentation);
      window.removeEventListener("storage", syncBoardPresentation);
    };
  }, [carbonMode]);

  useEffect(() => {
    if (!open || !desktop || activeTab !== "general") return;
    let cancelled = false;

    void isPortable().then((portableBuild) => {
      if (!cancelled) setPortable(portableBuild);
    });
    void getAutostartMode()
      .then((mode) => {
        if (!cancelled) setAutostartState(mode);
      })
      .catch(() => {
        // Keep the last known control usable. A bounded desktop call can be retried
        // by closing and reopening this tab instead of leaving Settings blocked.
      });

    return () => {
      cancelled = true;
    };
  }, [activeTab, desktop, open]);

  useEffect(() => {
    if (!open || !desktop || activeTab !== "notifications") return;
    setNotifs(osNotifEnabled());
  }, [activeTab, desktop, open]);

  useEffect(() => {
    if (!open || !desktop || !carbonMode || activeTab !== "data") return;
    let cancelled = false;

    void getMainDataHome().then((home) => {
      if (cancelled || !home) return;
      setDataHome(home.path);
      setPendingDataHome(home.pendingPath);
      setHomeRestartRequired(home.restartRequired);
    });

    return () => {
      cancelled = true;
    };
  }, [activeTab, carbonMode, desktop, open]);

  const toggleNotifs = (on: boolean) => {
    setNotifs(on);
    setOsNotifEnabled(on);
  };

  const changeProjectManagementPresentation = (value: string) => {
    const next: ProjectManagementPresentation = value === "page" ? "page" : "dialog";
    setProjectManagementPresentationState(next);
    setProjectManagementPresentation(next);
  };

  const changeTaskListPresentation = (value: string) => {
    if (value !== "row" && value !== "card") return;
    setTaskListPresentationState(value);
    setTaskListPresentation(value);
  };

  const changeAnimationBoardStyle = (value: string) => {
    if (!isAnimationBoardStyle(value)) return;
    setAnimationBoardStyleState(value);
    setAnimationBoardStyle(value);
  };

  const changeAutostart = async (mode: AutostartMode) => {
    if (
      mode === "admin" &&
      !window.confirm(
        t(
          "Administrator autostart runs the entire Carbon app, its local server, and project checks with elevated privileges. It also requires a protected application folder and a Windows UAC prompt. Continue?",
          "管理员自启动会让整个 Carbon、其本地服务和项目检查命令都以高权限运行，并要求程序目录受到保护；Windows 还会弹出 UAC 确认。确定继续吗？",
        ),
      )
    ) {
      return;
    }
    const previous = autostart;
    setAutostartState(mode);
    setChangingAutostart(true);
    try {
      await setAutostartMode(mode);
      toast.success(t("Autostart setting saved", "自启动设置已保存"));
    } catch (error) {
      setAutostartState(previous);
      const description = error instanceof Error && error.message.includes("timed out")
        ? t(
            "The desktop app did not respond in time. The change may still finish; reopen Settings to refresh its state.",
            "桌面应用未能及时响应。操作可能仍在完成；重新打开设置即可刷新状态。",
          )
        : error instanceof Error ? error.message : String(error);
      toast.error(t("Could not change autostart", "无法更改自启动设置"), {
        description,
      });
      // A native timeout does not cancel the scheduled-task operation. Re-read the
      // authoritative state in the background so the selector recovers if it finishes.
      void getAutostartMode().then(setAutostartState).catch(() => undefined);
    } finally {
      setChangingAutostart(false);
    }
  };

  const chooseDataHome = async () => {
    const picked = await pickFolder();
    if (!picked) return;
    const result = await setMainDataHome(picked);
    if (!result) {
      toast.error(t("Could not change Main Data Home", "无法更改主数据目录"));
      return;
    }
    setDataHome(result.path);
    setPendingDataHome(result.pendingPath);
    setHomeRestartRequired(result.restartRequired);
    // `path` remains the active home until the desktop shell has restarted.
    onDataHomeChange?.(result.path);
    if (result.restartRequired && result.pendingPath) {
      toast.message(t("Main Data Home will change after restart", "重启后将切换主数据目录"));
    } else {
      toast.success(t("Main Data Home saved", "主数据目录已保存"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("Settings", "设置")}</DialogTitle>
          <DialogDescription>
            {t("Preferences for this workspace and machine.", "当前工作区与本机的偏好设置。")}
          </DialogDescription>
        </DialogHeader>

        <Tabs value={activeTab} onValueChange={(value) => setActiveTab(value as SettingsTab)} className="gap-0">
          <TabsList className="w-full" aria-label={t("Settings categories", "设置分类")}>
            <TabsTrigger value="general">{t("Common", "常用")}</TabsTrigger>
            <TabsTrigger value="notifications">{t("Notifications", "通知")}</TabsTrigger>
            {showDataTab && <TabsTrigger value="data">{t("Data & backup", "数据与备份")}</TabsTrigger>}
          </TabsList>

          <TabsContent value="general" className="mt-4">
            {activeTab === "general" && (
              <section className="flex flex-col gap-1">
                <Row
                  title={t("Language", "语言")}
                  desc={t("Choose the display language. Changes apply immediately.", "选择界面语言，切换后立即生效。")}
                >
                  <Select value={language} onValueChange={(value) => setLanguage(value as "en" | "zh")}>
                    <SelectTrigger className="w-32" aria-label={t("Language", "语言")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="zh">简体中文</SelectItem>
                      <SelectItem value="en">English</SelectItem>
                    </SelectContent>
                  </Select>
                </Row>

                <Row
                  title={t("Identity", "身份")}
                  desc={t("This name appears in task assignments and take-over records.", "此名称会显示在任务分配和接手记录中。")}
                >
                  <div className="flex items-center gap-1.5">
                    <Input value={identityDraft} onChange={(event) => setIdentityDraft(event.target.value)} placeholder="you" className="h-8 w-32 text-sm" />
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={!identityDraft.trim() || identityDraft.trim() === displayName(actor)}
                      onClick={() => setName(identityDraft.trim())}
                    >
                      {t("Save", "保存")}
                    </Button>
                  </div>
                </Row>

                {!carbonMode && (
                  <Row
                    title={t("Check shell", "检查命令 Shell")}
                    desc={t(
                      "Shell that runs command checks. Empty uses sh; on Windows use Git Bash/WSL or a path to a shell. (The CARBON_SHELL env var overrides this.)",
                      "运行检查命令所使用的 Shell。留空时使用 sh；Windows 可填写 Git Bash、WSL 或 Shell 路径。（CARBON_SHELL 环境变量优先。）",
                    )}
                  >
                    <div className="flex items-center gap-1.5">
                      <Input
                        value={shell}
                        onChange={(e) => setShell(e.target.value)}
                        placeholder="sh"
                        spellCheck={false}
                        className="h-8 w-40 font-mono text-sm"
                      />
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={!shellDirty || saveShell.isPending}
                        onClick={() => saveShell.mutate(shell.trim())}
                      >
                        {t("Save", "保存")}
                      </Button>
                    </div>
                  </Row>
                )}

                {carbonMode && (
                  <section className="flex flex-col gap-1">
                    <p className="pt-2 text-sm font-medium">{t("Personalization", "个性化")}</p>
                    <Row
                      title={t("Appearance", "外观")}
                      desc={t(
                        "Choose a layered light or charcoal-blue theme. Carbon no longer uses a pure-black surface.",
                        "选择有层次的浅色或炭蓝深色主题；Carbon 不再使用纯黑底色。",
                      )}
                    >
                      <Select
                        value={theme}
                        onValueChange={(value) => {
                          const next: Theme = value === "dark" ? "dark" : "light";
                          setTheme(next);
                          applyTheme(next);
                        }}
                      >
                        <SelectTrigger className="w-32" aria-label={t("Appearance", "外观")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="light">{t("Light", "浅色")}</SelectItem>
                          <SelectItem value="dark">{t("Charcoal blue", "炭蓝深色")}</SelectItem>
                        </SelectContent>
                      </Select>
                    </Row>
                    <FieldGroup className="gap-0">
                      <Field orientation="horizontal" className="items-center justify-between gap-4 py-2.5">
                        <FieldContent className="min-w-0">
                          <FieldTitle>{t("Task presentation", "任务展示")}</FieldTitle>
                          <FieldDescription>
                            {t(
                              "Choose rows or cards for Tasks and Agent work. Visual board styles are configured separately below.",
                              "任务与智能体工作始终保留原有功能，只在行模式和卡片模式之间切换。",
                            )}
                          </FieldDescription>
                        </FieldContent>
                        <ToggleGroup
                          type="single"
                          variant="outline"
                          size="sm"
                          spacing={0}
                          value={taskListPresentation}
                          onValueChange={changeTaskListPresentation}
                          className="flex-wrap justify-end"
                          aria-label={t("Task presentation", "任务展示")}
                        >
                          <ToggleGroupItem value="row" aria-label={t("Row mode", "行模式")}>
                            <Rows3 data-icon="inline-start" />
                            {t("Row", "行模式")}
                          </ToggleGroupItem>
                          <ToggleGroupItem value="card" aria-label={t("Card mode", "卡片模式")}>
                            <PanelsTopLeft data-icon="inline-start" />
                            {t("Card", "卡片")}
                          </ToggleGroupItem>
                        </ToggleGroup>
                      </Field>
                      <Field orientation="horizontal" className="items-center justify-between gap-4 border-t py-2.5">
                        <FieldContent className="min-w-0">
                          <FieldTitle>{t("Board style", "看板风格")}</FieldTitle>
                          <FieldDescription>{t("Used only by the dedicated Board; task lists stay intact.", "仅用于“看板”，不会替换任务或智能体工作页面。")}</FieldDescription>
                        </FieldContent>
                        <ToggleGroup
                          type="single"
                          variant="outline"
                          size="sm"
                          spacing={0}
                          value={animationBoardStyle}
                          onValueChange={changeAnimationBoardStyle}
                          aria-label={t("Animation board style", "动画看板风格")}
                        >
                          {Object.values(ANIMATION_BOARD_STYLE_REGISTRY).map((definition) => (
                            <ToggleGroupItem
                              key={definition.id}
                              value={definition.id}
                              aria-label={t(definition.label.english, definition.label.chinese)}
                              title={t(definition.description.english, definition.description.chinese)}
                            >
                              {definition.id === "pixel-agents" ? <Bot data-icon="inline-start" /> : <CandlestickChart data-icon="inline-start" />}
                              {t(definition.label.english, definition.label.chinese)}
                            </ToggleGroupItem>
                          ))}
                        </ToggleGroup>
                      </Field>
                    </FieldGroup>
                    <Row
                      title={t("Project management", "项目管理")}
                      desc={t(
                        "Choose what opens when you select Projects from a task board.",
                        "设置从任务看板选择“项目”时的打开方式。",
                      )}
                    >
                      <Select value={projectManagementPresentation} onValueChange={changeProjectManagementPresentation}>
                        <SelectTrigger className="w-32" aria-label={t("Project management presentation", "项目管理展示方式")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="dialog">{t("Dialog", "弹窗")}</SelectItem>
                          <SelectItem value="page">{t("Full page", "独立页面")}</SelectItem>
                        </SelectContent>
                      </Select>
                    </Row>
                  </section>
                )}

                {desktop ? (
                  <>
                    <Row
                      title={t("Launch at login", "登录时自启动")}
                      desc={
                        autostart === "admin"
                          ? t(
                              "Runs the entire app with elevated privileges. Use only for trusted projects and a protected application folder.",
                              "整个应用将以管理员权限运行。仅应处理可信项目，并确保程序目录不可被普通用户修改。",
                            )
                          : t(
                              "Standard mode starts hidden in the tray. Administrator mode requires UAC and a protected folder.",
                              "普通模式会在登录后静默启动到托盘；管理员模式需要 UAC 和受保护的程序目录。",
                            )
                      }
                    >
                      <Select
                        value={autostart}
                        disabled={changingAutostart}
                        onValueChange={(value) => void changeAutostart(value as AutostartMode)}
                      >
                        <SelectTrigger className="w-36" aria-label={t("Autostart mode", "自启动模式")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="off">{t("Off", "关闭")}</SelectItem>
                          <SelectItem value="user">{t("Standard", "普通")}</SelectItem>
                          <SelectItem value="admin">{t("Administrator", "管理员")}</SelectItem>
                        </SelectContent>
                      </Select>
                    </Row>
                    {portable ? (
                      <Row
                        title={t("Portable mode", "便携模式")}
                        desc={t(
                          "Installer updates and deep-link registration are disabled. Archive the old files and replace them with a newer portable build to update.",
                          "安装器更新和深链注册已关闭。更新时请归档旧文件，再替换为新版便携文件。",
                        )}
                      >
                        <Badge variant="secondary">{t("Active", "已启用")}</Badge>
                      </Row>
                    ) : (
                      <Row
                        title={carbonMode ? t("Carbon updates", "Carbon 更新") : t("Updates", "更新")}
                        desc={t("No update source is configured. Automatic updates are disabled; replace builds manually.", "未配置更新源。自动更新已禁用；请手动替换构建版本。")}
                      >
                        <Badge variant="secondary">{t("Disabled", "已禁用")}</Badge>
                      </Row>
                    )}
                  </>
                ) : (
                  <p className="pt-2 text-xs text-muted-foreground">
                    {t(
                      "Launch at login and portable-build details are available only in the Carbon desktop app.",
                      "自启动和便携版信息仅在 Carbon 桌面应用中可用。",
                    )}
                  </p>
                )}
              </section>
            )}
          </TabsContent>

          <TabsContent value="notifications" className="mt-4">
            {activeTab === "notifications" && (
              <section className="flex flex-col gap-1">
                {desktop && (
                  <Row
                    title={t("System notifications", "系统通知")}
                    desc={t(
                      "Alert me when a task becomes ready, a check fails, or work is awaiting review.",
                      "任务就绪、检查失败或等待审核时通知我。",
                    )}
                  >
                    <Switch checked={notifs} onCheckedChange={toggleNotifs} />
                  </Row>
                )}
                <NotificationPreferences
                  open={open}
                  scope={carbonMode
                    ? { homeId: notificationHomeId, home: carbonScope?.home }
                    : path ? { legacyPath: path } : undefined}
                />
              </section>
            )}
          </TabsContent>

          {showDataTab && (
            <TabsContent value="data" className="mt-4">
              {activeTab === "data" && (
                <section className="flex flex-col gap-4">
                  {desktop && carbonMode && (
                    <Row
                      title={t("Main Data Home", "主数据目录")}
                      desc={
                        homeRestartRequired && pendingDataHome
                          ? t(
                              `Active home remains ${dataHome || "the current home"} until restart. Pending home: ${pendingDataHome}.`,
                              `重启前仍使用 ${dataHome || "当前目录"}；待切换目录：${pendingDataHome}。`,
                            )
                          : t(
                              "Clusters and their logical projects live under this home, not directly in a source folder.",
                              "集群及其逻辑项目保存在此目录中，而非直接保存在源码文件夹中。",
                            )
                      }
                    >
                      <Button variant="outline" size="sm" onClick={() => void chooseDataHome()}>
                        <FolderCog data-icon="inline-start" />
                        {dataHome ? t("Change", "更改") : t("Choose", "选择")}
                      </Button>
                    </Row>
                  )}
                  {carbonScope && <CarbonManagementSettings scope={carbonScope} />}
                  {backupHome && <BackupSettings home={backupHome} />}
                </section>
              )}
            </TabsContent>
          )}
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function Row({
  title,
  desc,
  children,
}: {
  title: string;
  desc: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-4 py-2.5">
      <div className="min-w-0">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{desc}</p>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}
