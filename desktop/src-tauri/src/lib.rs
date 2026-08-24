use std::fs;
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Component, Path, PathBuf};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, Mutex,
};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tauri::menu::{CheckMenuItemBuilder, MenuBuilder, MenuItemBuilder, SubmenuBuilder};
use tauri::tray::TrayIconBuilder;
use tauri::webview::PageLoadEvent;
use tauri::{Emitter, Manager, RunEvent, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_dialog::{DialogExt, FilePath};

/// The public product name used by native shell surfaces.
const PRODUCT_NAME: &str = "Carbon";

/// Preferred listen address for the bundled server. The Go side falls back to the
/// next free port if this is taken, and reports the port it actually bound.
const PREFERRED_ADDR: &str = "127.0.0.1:2525";

const DESKTOP_PREFERENCES_FILE: &str = "carbon-desktop.json";
/// Migration-only name used by the pre-Carbon desktop profile.
const LEGACY_DESKTOP_PREFERENCES_FILE: &str = "cairn-desktop.json";
/// The window-state plugin's default filename.  It lives in the app-config profile rather
/// than the app-data profile used by the rest of the native preferences.
const WINDOW_STATE_FILE: &str = ".window-state.json";
/// The exact Windows inner-size signature observed in the clipped restore shown by the user.
/// Keep the repair surgical: physical-pixel sizes cannot safely be generalized across displays,
/// so legitimate ultrawide layouts (for example 1920×720) must remain untouched.
#[cfg(target_os = "windows")]
const CLIPPED_RESTORE_MAIN_WIDTH: f64 = 1_968.0;
#[cfg(target_os = "windows")]
const CLIPPED_RESTORE_MAIN_HEIGHT: f64 = 741.0;
const DESKTOP_APP_IDENTIFIER: &str = "com.shaho.carbon";
/// Migration-only app identifier used to locate the pre-Carbon desktop profile.
const LEGACY_DESKTOP_APP_IDENTIFIER: &str = "com.shaho.cairn";
const NOTIFICATION_SOUNDS_DIR: &str = "notification-sounds";
const MAX_NOTIFICATION_SOUND_BYTES: u64 = 10 * 1024 * 1024;

const CARBON_WEB_URL_PREFIX: &str = "CARBON_WEB_URL=";
/// Compatibility-only stdout handshake accepted from an already-installed legacy sidecar.
const LEGACY_CAIRN_WEB_URL_PREFIX: &str = "CAIRN_WEB_URL=";

/// A marker beside the Windows executable makes an unpacked ZIP a portable build.
/// The marker is deliberately opt-in so installed builds keep their protocol-registration
/// behavior separate from portable folders.
#[cfg(target_os = "windows")]
const PORTABLE_MARKER: &str = "carbon-portable.marker";
/// Compatibility-only marker accepted from a portable folder created before the rename.
#[cfg(target_os = "windows")]
const LEGACY_PORTABLE_MARKER: &str = "cairn-portable.marker";

/// A protected Task Scheduler entry passes the approved data home explicitly.  It must never
/// fall back to the ordinary desktop-preferences file, because that file is intentionally
/// writable by the signed-in (non-elevated) user.
#[cfg(target_os = "windows")]
const ADMIN_DATA_HOME_ARG: &str = "--carbon-admin-data-home";

/// Default global shortcut that pops the quick-capture window.
const CAPTURE_SHORTCUT: &str = "CmdOrCtrl+Shift+K";
/// Stable label for the single native picture-in-picture task board.  Keep this out of the
/// persisted window-state cache: the scope is transient and is always supplied by the main UI.
const FLOATING_BOARD_WINDOW_LABEL: &str = "floating-board";

/// Full-page message shown if the bundled Carbon server never comes up. Self-contained
/// so it works regardless of what the hidden webview had loaded.
const STARTUP_ERROR_JS: &str = "document.open();document.write('<!doctype html><meta charset=utf-8><body style=\"margin:0;height:100vh;display:grid;place-items:center;font-family:ui-sans-serif,system-ui,sans-serif;background:radial-gradient(circle at 24% 12%,#2b3f78 0,transparent 38%),linear-gradient(135deg,#172136,#292d43 58%,#1c3942);color:#e7ecfa\"><div style=\"max-width:420px;padding:30px 34px;text-align:center;border:1px solid rgba(151,169,226,.24);border-radius:18px;background:rgba(47,54,78,.78);box-shadow:0 24px 70px rgba(8,15,30,.32)\"><div style=\"font-weight:650;letter-spacing:.03em;color:#aebcff\">Carbon</div><div style=\"font-size:13px;color:#d2daf0;margin-top:9px\">本地服务未能启动，请退出后重新打开 Carbon。</div><div style=\"font-size:12px;color:#9facc8;margin-top:5px\">Could not start the local server. Quit and reopen Carbon.</div></div></body>');document.close();";

/// Holds the spawned sidecar so we can kill it on a real quit. The Go side also watches
/// stdin (via --parent-watch) and dies on EOF, covering crashes/force-quits.
struct Sidecar(Mutex<Option<tauri_plugin_shell::process::CommandChild>>);

/// The URL the bundled server bound to (from its stdout handshake). Used to open the
/// capture window at the right origin. None in dev or before the server is up.
struct ServerUrl(Mutex<Option<String>>);

/// Only opaque catalog identifiers cross the native window boundary.  In particular, callers
/// cannot make the shell navigate a floating webview to a filesystem path, a custom URL, or a
/// script-bearing string.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct FloatingBoardTarget {
    cluster_id: Option<String>,
    /// Omitted only for an explicit cluster-wide task feed.
    project_id: Option<String>,
    /// The workspace project that remains visible when a cluster-wide task opens in main.
    workspace_project_id: String,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct FloatingTaskTarget {
    cluster_id: Option<String>,
    project_id: Option<String>,
    workspace_project_id: String,
    task_id: String,
}

/// Whether this executable was packaged as the Windows portable ZIP. The state is captured
/// once on startup, so deep-link registration cannot be enabled later by deleting the marker
/// while the application is running. Autostart remains available: a
/// portable app needs to be able to start itself without an installer.
struct PortableMode(bool);

/// True only for a login-item launch. The initial window stays hidden once the local server is
/// ready, while a normal user launch or a tray click still reveals it.
struct BackgroundLaunch(bool);

/// The small amount of text owned by the native shell. The web frontend owns its full UI and
/// dynamic tray content, but this keeps the native menubar/tray usable before hydration.
struct NativeLanguage(Mutex<UiLanguage>);

/// `update_tray` is driven by the frontend. Do not overwrite that dynamic menu when only the
/// native language changes; the frontend will submit its translated model itself.
struct DynamicTray(Mutex<bool>);

/// Local shell preferences live in Carbon's Tauri app-data profile. On first launch, compatible
/// settings are copied from the legacy profile without making that profile the active location.
struct DesktopPreferencesState(Mutex<DesktopPreferences>);

/// Present only for the immutable data home supplied by a protected administrator Task
/// Scheduler entry.  Its presence is a trust boundary: this process neither loads nor persists
/// ordinary app-data preferences, because those files are writable by the signed-in user.
struct LockedAdminDataHome(Option<PathBuf>);

/// The exact home given to the sidecar for this process lifetime. A saved preference may change
/// while Carbon is running, but it is deliberately only a next-launch choice.
struct LaunchDataHome(PathBuf);

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct DesktopPreferences {
    notification_sound: Option<NotificationSound>,
    /// An explicit Go server home picked by the user. `None` means use the platform default.
    /// Changing it only persists a pending choice; it never moves data and takes effect after
    /// the app is restarted.
    data_home: Option<String>,
}

impl Default for DesktopPreferences {
    fn default() -> Self {
        Self {
            notification_sound: None,
            data_home: None,
        }
    }
}

/// Metadata is intentionally path-free. The UI only learns that a sound was imported; the
/// copied file remains under our app-data directory and cannot be played from an arbitrary path.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NotificationSound {
    name: String,
    extension: String,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct DataHome {
    /// The actual home of the sidecar already running in this desktop process.
    path: String,
    /// Whether the active sidecar home is Carbon's platform default.
    is_default: bool,
    /// A persisted home that will replace `path` only after Carbon restarts.
    #[serde(skip_serializing_if = "Option::is_none")]
    pending_path: Option<String>,
    restart_required: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum UiLanguage {
    English,
    Chinese,
}

impl UiLanguage {
    fn parse(value: &str) -> Result<Self, String> {
        match value {
            "en" => Ok(Self::English),
            "zh" => Ok(Self::Chinese),
            _ => Err("Language must be either 'en' or 'zh'.".to_string()),
        }
    }

    fn code(self) -> &'static str {
        match self {
            Self::English => "en",
            Self::Chinese => "zh",
        }
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let startup_args = std::env::args().skip(1).collect::<Vec<_>>();

    #[cfg(target_os = "windows")]
    {
        if startup_args
            .first()
            .is_some_and(|arg| is_autostart_helper_arg(arg))
        {
            let code = autostart_helper_mode(&startup_args)
                .map(|(mode, data_home)| {
                    windows_autostart::run_elevated_helper_entry(mode, data_home.as_deref())
                })
                .unwrap_or(64);
            std::process::exit(code);
        }
    }

    #[cfg(target_os = "windows")]
    let locked_admin_data_home = match locked_admin_data_home_from_args(&startup_args) {
        Ok(data_home) => data_home,
        Err(error) => {
            eprintln!("Carbon: {error}");
            std::process::exit(64);
        }
    };
    #[cfg(not(target_os = "windows"))]
    let locked_admin_data_home: Option<PathBuf> = None;

    let portable = portable_mode();
    let background = has_background_arg(&startup_args);
    let mut builder = tauri::Builder::default();

    // single-instance MUST be registered first: a second launch focuses the running
    // window instead of spawning a duplicate sidecar (which would fight for the port).
    #[cfg(desktop)]
    {
        builder = builder.plugin(tauri_plugin_single_instance::init(
            move |app, argv, _cwd| {
                // A second launch from a login item must not make a manually hidden window
                // reappear. Regular launches and deep links still reveal the app as usual.
                if !has_background_arg(&argv) {
                    show_main(app);
                }
                if portable {
                    return;
                }
                // On Windows/Linux a carbon:// open while running arrives as a CLI arg to this
                // second instance — forward it to the UI.
                for arg in &argv {
                    if is_supported_deep_link(arg) {
                        let _ = app.emit("deep-link", arg.clone());
                    }
                }
            },
        ));
    }

    builder = builder
        .plugin(
            tauri::plugin::Builder::<tauri::Wry, ()>::new("navigation-guard")
                .on_navigation(navigation_allowed)
                .build(),
        )
        // This must initialize before window-state. The plugin loads its cache during setup,
        // so copying later in the application setup would lose the old geometry on the first
        // Carbon launch (and would be overwritten when the app exits).
        .plugin(
            tauri::plugin::Builder::<tauri::Wry, ()>::new("legacy-profile-migration")
                .setup(|app, _api| {
                    if let Err(error) = migrate_legacy_window_state(app) {
                        // Window placement is a convenience only; do not prevent a desktop
                        // upgrade from starting because the old cache was unreadable.
                        log::warn!("could not migrate legacy Carbon window state: {error}");
                    }
                    #[cfg(target_os = "windows")]
                    {
                        if let Err(error) = sanitize_main_window_state(app) {
                            // The window-state plugin already treats an unreadable cache as
                            // empty. Keep that behavior and never make placement cache repair
                            // fatal to application startup.
                            log::warn!("could not sanitize saved Carbon window state: {error}");
                        }
                    }
                    Ok(())
                })
                .build(),
        )
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_denylist(&["capture", FLOATING_BOARD_WINDOW_LABEL])
                .build(),
        )
        .plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(|app, _shortcut, event| {
                    if event.state == tauri_plugin_global_shortcut::ShortcutState::Pressed {
                        open_capture(app);
                    }
                })
                .build(),
        )
        .plugin(tauri_plugin_notification::init());

    if !portable {
        builder = builder.plugin(tauri_plugin_deep_link::init());
    }

    builder = builder
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init());

    builder
        .setup(move |app| {
            app.manage(PortableMode(portable));
            app.manage(BackgroundLaunch(background));
            app.manage(NativeLanguage(Mutex::new(UiLanguage::English)));
            app.manage(DynamicTray(Mutex::new(false)));

            // An elevated Carbon process must never select its data home from the ordinary
            // app-data preferences.  A protected administrator autostart task provides the
            // immutable command-line value instead; manual elevation without that value is
            // intentionally rejected before preferences are read.
            #[cfg(target_os = "windows")]
            let locked_launch_data_home = match locked_admin_data_home.as_deref() {
                Some(path) => {
                    if !windows_autostart::is_current_process_elevated()? {
                        return Err("Carbon's administrator data-home argument is reserved for the protected elevated administrator-autostart task. Start normally for user mode, or reconfigure administrator autostart.".into());
                    }
                    let path = resolve_existing_data_home(path)?;
                    windows_autostart::ensure_admin_data_home_is_safe(&path)?;
                    Some(path)
                }
                None => {
                    if windows_autostart::is_current_process_elevated()? {
                        return Err("Carbon was started elevated without its protected administrator-autostart data-home argument. To avoid reading user-writable desktop preferences, Carbon will not start this way. Reconfigure administrator autostart, use normal user startup, or choose a protected data home.".into());
                    }
                    None
                }
            };
            #[cfg(not(target_os = "windows"))]
            let locked_launch_data_home: Option<PathBuf> = None;

            let locked_active_data_home = locked_launch_data_home.clone();
            let preferences = desktop_preferences_for_launch(
                locked_active_data_home.as_deref(),
                || load_desktop_preferences(app.handle()),
            )?;
            let launch_data_home = match locked_launch_data_home {
                Some(path) => path,
                None => configured_data_home(app.handle(), preferences.data_home.as_deref())?,
            };
            app.manage(DesktopPreferencesState(Mutex::new(preferences)));
            app.manage(LockedAdminDataHome(locked_active_data_home));
            app.manage(LaunchDataHome(launch_data_home));

            // Windows has a custom implementation because a portable executable can live in a
            // path with spaces. Other desktop platforms retain Tauri's supported launcher.
            #[cfg(all(desktop, not(target_os = "windows")))]
            {
                if !portable {
                    use tauri_plugin_autostart::MacosLauncher;
                    app.handle().plugin(tauri_plugin_autostart::init(
                        MacosLauncher::LaunchAgent,
                        Some(vec!["--background"]),
                    ))?;
                }
            }

            #[cfg(debug_assertions)]
            app.handle().plugin(
                tauri_plugin_log::Builder::default()
                    .level(log::LevelFilter::Info)
                    .build(),
            )?;

            app.manage(ServerUrl(Mutex::new(None)));

            // Native menu (gives macOS its Edit menu → copy/paste in inputs) + tray.
            let menu = build_menu(app.handle(), UiLanguage::English)?;
            app.set_menu(menu)?;
            app.on_menu_event(|app, event| handle_menu(app, event.id().as_ref()));
            build_tray(app, UiLanguage::English)?;

            if background {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.hide();
                }
            }

            // Global quick-capture shortcut.
            #[cfg(desktop)]
            {
                use tauri_plugin_global_shortcut::GlobalShortcutExt;
                let _ = app.global_shortcut().register(CAPTURE_SHORTCUT);
            }

            // Carbon deep links: forward OS opens to the UI. register_all()
            // is best-effort for Linux/Windows runtime registration; macOS uses bundled schemes.
            // A portable Windows executable must not write a persistent protocol association.
            #[cfg(desktop)]
            {
                if !portable {
                    use tauri_plugin_deep_link::DeepLinkExt;
                    let _ = app.deep_link().register_all();
                    let dh = app.handle().clone();
                    app.deep_link().on_open_url(move |event| {
                        for url in event.urls() {
                            let value = url.to_string();
                            if !is_supported_deep_link(&value) {
                                continue;
                            }
                            show_main(&dh);
                            let _ = dh.emit("deep-link", value);
                        }
                    });
                }
            }

            // Close-to-tray (prod only): the X hides the window so the server + MCP stay
            // up. Quit explicitly from the tray/menu. Dev keeps normal close for fast iter.
            if !cfg!(debug_assertions) {
                if let Some(w) = app.get_webview_window("main") {
                    let wc = w.clone();
                    w.on_window_event(move |ev| {
                        if let tauri::WindowEvent::CloseRequested { api, .. } = ev {
                            api.prevent_close();
                            let _ = wc.hide();
                        }
                    });
                }
            }

            if cfg!(debug_assertions) {
                // Dev: Vite (devUrl) serves the live UI and the developer runs
                // `carbon web` separately — just reveal the window.
                if !background {
                    if let Some(w) = app.get_webview_window("main") {
                        let _ = w.show();
                    }
                }
                return Ok(());
            }

            // Production: spawn the bundled Carbon server. It binds a local port
            // (2525, or the next free one), prints `CARBON_WEB_URL=<url>` on stdout,
            // and dies with us thanks to --parent-watch.
            use tauri_plugin_shell::ShellExt;
            let data_home = app.state::<LaunchDataHome>().0.clone();
            let data_home = resolve_existing_data_home(&data_home)?;
            #[cfg(target_os = "windows")]
            if desktop_preferences_are_locked(app.handle()) {
                // Re-check immediately before spawning the elevated sidecar. This closes the
                // gap after startup validation and rejects a changed/junctioned home instead of
                // letting the sidecar resolve it with administrator rights.
                windows_autostart::ensure_admin_data_home_is_safe(&data_home)?;
            }
            let data_home = data_home
                .to_str()
                .ok_or_else(|| "Carbon data directory is not valid Unicode.".to_string())?
                .to_string();
            let sidecar_args = vec![
                "web".to_string(),
                "--addr".to_string(),
                PREFERRED_ADDR.to_string(),
                "--parent-watch".to_string(),
                "--home".to_string(),
                data_home,
            ];
            let (mut rx, child) = app.shell().sidecar("carbon")?.args(sidecar_args).spawn()?;
            app.manage(Sidecar(Mutex::new(Some(child))));

            // Read the sidecar's stdout for the URL line; record it (for the capture
            // window) and, once seen, poll /healthz on a worker thread before navigating.
            let handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                use tauri_plugin_shell::process::CommandEvent;
                while let Some(ev) = rx.recv().await {
                    if let CommandEvent::Stdout(b) | CommandEvent::Stderr(b) = ev {
                        let chunk = String::from_utf8_lossy(&b);
                        for line in chunk.lines() {
                            if let Some(url) = parse_url(line) {
                                *handle.state::<ServerUrl>().0.lock().unwrap() = Some(url.clone());
                                let h = handle.clone();
                                std::thread::spawn(move || finish_startup(&h, &url));
                            }
                        }
                        log::info!("carbon: {}", chunk.trim_end());
                    }
                }
            });

            // Safety net: if no URL ever arrives, show a clear error rather than a
            // window stuck hidden (or, worse, never shown at all).
            let h2 = app.handle().clone();
            std::thread::spawn(move || {
                std::thread::sleep(Duration::from_secs(20));
                if let Some(w) = h2.get_webview_window("main") {
                    // A successful `--background` startup intentionally stays hidden. Only
                    // surface this fallback if the sidecar never printed a URL at all; a URL
                    // with a failed health check is handled by `finish_startup` itself.
                    let saw_server_url = h2.state::<ServerUrl>().0.lock().unwrap().is_some();
                    if !saw_server_url && !w.is_visible().unwrap_or(false) {
                        let _ = w.eval(STARTUP_ERROR_JS);
                        let _ = w.show();
                    }
                }
            });

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            is_portable,
            get_autostart_mode,
            set_autostart_mode,
            set_ui_language,
            update_tray,
            is_notification_owner,
            choose_notification_sound,
            get_notification_sound,
            clear_notification_sound,
            play_notification_sound,
            get_data_home,
            set_data_home,
            open_floating_board,
            close_floating_board,
            focus_main_task
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|app, event| {
            // Belt to the Go-side stdin watcher: kill the child on a real quit.
            if matches!(event, RunEvent::ExitRequested { .. } | RunEvent::Exit) {
                if let Some(state) = app.try_state::<Sidecar>() {
                    if let Some(child) = state.0.lock().unwrap().take() {
                        let _ = child.kill();
                    }
                }
            }
        });
}

/// Returns the captured portable-mode flag to the UI so it can communicate packaging-specific
/// behavior without trusting a mutable frontend flag.
#[tauri::command]
fn is_portable(portable: tauri::State<'_, PortableMode>) -> bool {
    portable.0
}

/// Open or update the one system-level floating board.  The route is composed exclusively from
/// validated catalog IDs and the sidecar URL captured by this process; a renderer can never turn
/// this command into a general-purpose navigation primitive.
#[tauri::command]
fn open_floating_board(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
    target: FloatingBoardTarget,
) -> Result<(), String> {
    require_main_window(&window)?;
    validate_floating_board_target(&target)?;
    let server = current_server_url(&app)?;
    let url = floating_board_url(&app, &target)?;
    // Do not create a second webview against a port that has only been announced but is not
    // serving yet. A native webview remembers that blank response and will not recover by itself.
    if !server_ready(server.as_str()) {
        return Err("Carbon's local server is still warming up.".into());
    }

    if let Some(floating) = app.get_webview_window(FLOATING_BOARD_WINDOW_LABEL) {
        floating
            .navigate(url)
            .map_err(|error| format!("Could not update Carbon's floating board: {error}"))?;
        let _ = floating.set_always_on_top(true);
        let _ = floating.show();
        let _ = floating.unminimize();
        let _ = floating.set_focus();
        return Ok(());
    }

    // Start from the bundled `tauri.localhost` document, then redirect to the verified loopback
    // server after the app document has painted. Loading the external URL directly can race the
    // navigation guard during WebView2 initialization; that leaves a perfectly healthy native
    // window with a black, never-painted surface.
    let redirect_url = url.clone();
    let redirected = Arc::new(AtomicBool::new(false));
    let loaded = Arc::new(AtomicBool::new(false));
    let redirect_once = redirected.clone();
    let loaded_once = loaded.clone();
    let floating = WebviewWindowBuilder::new(
        &app,
        FLOATING_BOARD_WINDOW_LABEL,
        WebviewUrl::App("index.html".into()),
    )
    .title("Carbon · Floating task board")
    .inner_size(560.0, 460.0)
    .min_inner_size(360.0, 300.0)
    .resizable(true)
    .always_on_top(true)
    .on_page_load(move |window, payload| {
        if payload.event() != PageLoadEvent::Finished {
            return;
        }

        let page_url = payload.url();
        let is_bundled_document =
            page_url.scheme() == "tauri" || page_url.host_str() == Some("tauri.localhost");
        if is_bundled_document {
            if !redirect_once.swap(true, Ordering::AcqRel) {
                if let Err(error) = window.navigate(redirect_url.clone()) {
                    log::warn!(
                        "could not navigate Carbon's floating board to its local server: {error}"
                    );
                }
            }
            return;
        }

        if page_url.origin() == redirect_url.origin() {
            loaded_once.store(true, Ordering::Release);
            let _ = window.show();
            let _ = window.unminimize();
            let _ = window.set_focus();
        }
    })
    .build()
    .map_err(|error| format!("Could not open Carbon's floating board: {error}"))?;

    // Make the native surface visible immediately as a second line of defence for WebView2
    // runtimes that ignore the builder's default visibility until their first navigation. The
    // bundled document is already safe to show; the callback below will focus it again after the
    // verified loopback page has painted.
    let _ = floating.show();
    let _ = floating.unminimize();
    let _ = floating.set_focus();

    // If a particular WebView2 runtime drops the first page-load callback, retry the verified
    // navigation from the app handle after a short grace period. The retry is bounded and never
    // touches the user's data or creates another native window.
    let retry_app = app.clone();
    let retry_url = url.clone();
    let retry_loaded = loaded.clone();
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(900));
        // `redirected` only means that the bundled document attempted the hand-off; the
        // hand-off itself may still have been rejected by a transient navigation-guard race.
        // Retry until the loopback document reports Finished, rather than treating that first
        // attempt as success.
        if retry_loaded.load(Ordering::Acquire) {
            return;
        }
        if let Some(window) = retry_app.get_webview_window(FLOATING_BOARD_WINDOW_LABEL) {
            // A few WebView2 builds do not deliver the first page-load callback reliably. Surface
            // the already-created window at this bounded fallback and replay the verified
            // navigation; the external document can then finish painting and the normal callback
            // will keep it focused.
            let _ = window.navigate(retry_url);
            let _ = window.show();
            let _ = window.unminimize();
            let _ = window.set_focus();
        }
    });
    let event_app = app.clone();
    floating.on_window_event(move |event| {
        if matches!(event, tauri::WindowEvent::Destroyed) {
            // The event is emitted only after the native window is gone, covering its X button,
            // Escape, programmatic close, and platform title-bar close in one lifecycle signal.
            let _ = event_app.emit("floating-board:closed", ());
        }
    });
    Ok(())
}

/// Close the one native floating board from either its own controls or the main workspace. The
/// destroyed-window listener above is the source of truth for clearing the main UI's toggle.
#[tauri::command]
fn close_floating_board(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
) -> Result<bool, String> {
    if window.label() != "main" && window.label() != FLOATING_BOARD_WINDOW_LABEL {
        return Err(
            "Only Carbon's main window or floating board may close the floating board.".into(),
        );
    }
    let Some(floating) = app.get_webview_window(FLOATING_BOARD_WINDOW_LABEL) else {
        return Ok(false);
    };
    // `destroy` bypasses any close-request handler and is intentional here: this command is the
    // explicit escape hatch for a webview whose renderer/IPC queue is already unresponsive.
    floating
        .destroy()
        .map_err(|error| format!("Could not close Carbon's floating board: {error}"))?;
    Ok(true)
}

/// A task click in the floating webview returns to the main Carbon window through one canonical
/// Carbon hash route.  This intentionally accepts task metadata, not a caller-provided URL.
#[tauri::command]
fn focus_main_task(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
    target: FloatingTaskTarget,
) -> Result<(), String> {
    if window.label() != FLOATING_BOARD_WINDOW_LABEL {
        return Err("Only Carbon's floating board may open a task in the main window.".into());
    }
    validate_floating_task_target(&target)?;
    let url = main_task_url(&app, &target)?;
    let main = app
        .get_webview_window("main")
        .ok_or_else(|| "Carbon's main window is unavailable.".to_string())?;
    main.navigate(url)
        .map_err(|error| format!("Could not open the task in Carbon's main window: {error}"))?;
    let _ = main.show();
    let _ = main.unminimize();
    let _ = main.set_focus();
    Ok(())
}

fn app_data_dir(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("Could not resolve Carbon app-data directory: {error}"))?;
    fs::create_dir_all(&dir)
        .map_err(|error| format!("Could not create Carbon app-data directory: {error}"))?;
    Ok(dir)
}

fn desktop_preferences_path(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    Ok(app_data_dir(app)?.join(DESKTOP_PREFERENCES_FILE))
}

/// Return the old profile only when the resolver produced the canonical Carbon directory. This
/// avoids guessing at an unrelated parent layout on platforms with a non-standard app-data path.
fn legacy_profile_dir(carbon_profile_dir: &Path) -> Option<PathBuf> {
    if carbon_profile_dir.file_name()?.to_str()? != DESKTOP_APP_IDENTIFIER {
        return None;
    }
    carbon_profile_dir
        .parent()
        .map(|parent| parent.join(LEGACY_DESKTOP_APP_IDENTIFIER))
}

fn legacy_app_data_dir(carbon_app_data_dir: &Path) -> Option<PathBuf> {
    legacy_profile_dir(carbon_app_data_dir)
}

/// Copy the previous window-state cache before the window-state plugin initializes. Like desktop
/// preferences, the old profile is read once and never written to by Carbon.
fn migrate_legacy_window_state(app: &tauri::AppHandle) -> Result<(), String> {
    let carbon_profile = app
        .path()
        .app_config_dir()
        .map_err(|error| format!("Could not resolve Carbon app-config directory: {error}"))?;
    let destination = carbon_profile.join(WINDOW_STATE_FILE);
    if destination.exists() {
        return Ok(());
    }

    let Some(legacy_profile) = legacy_profile_dir(&carbon_profile) else {
        return Ok(());
    };
    let source = legacy_profile.join(WINDOW_STATE_FILE);
    let contents = match fs::read(&source) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => {
            return Err(format!(
                "Could not read legacy Carbon window state at {}: {error}",
                source.display()
            ));
        }
    };
    // Avoid creating a permanent invalid cache that would suppress another migration attempt.
    serde_json::from_slice::<serde_json::Value>(&contents).map_err(|error| {
        format!(
            "Could not parse legacy Carbon window state at {}: {error}",
            source.display()
        )
    })?;
    fs::create_dir_all(&carbon_profile)
        .map_err(|error| format!("Could not create Carbon app-config directory: {error}"))?;
    fs::write(&destination, contents).map_err(|error| {
        format!(
            "Could not write Carbon window state at {}: {error}",
            destination.display()
        )
    })?;
    log::info!(
        "migrated window state from {} to {}",
        source.display(),
        destination.display()
    );
    Ok(())
}

/// Clear only a known-bad restored `main` entry before `tauri-plugin-window-state` reads its
/// cache. A minimized/restored Windows window can occasionally persist a very wide, short
/// geometry that leaves the desktop UI visibly clipped on every later launch. Removing that
/// one entry lets the configured 1280×832 default take over while retaining any other windows
/// and future plugin metadata in the same cache.
#[cfg(target_os = "windows")]
fn sanitize_main_window_state(app: &tauri::AppHandle) -> Result<(), String> {
    let profile = app
        .path()
        .app_config_dir()
        .map_err(|error| format!("Could not resolve Carbon app-config directory: {error}"))?;
    let path = profile.join(WINDOW_STATE_FILE);

    if sanitize_window_state_cache_file(&path)? {
        log::warn!(
            "removed an abnormal restored main-window geometry from {}",
            path.display()
        );
    }
    Ok(())
}

/// Read and conditionally rewrite the cache only after it successfully parses. In particular,
/// a corrupt cache is deliberately left intact: the window-state plugin will keep its existing
/// tolerant fallback and the original file remains available for diagnosis.
#[cfg(target_os = "windows")]
fn sanitize_window_state_cache_file(path: &Path) -> Result<bool, String> {
    let contents = match fs::read(path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(error) => {
            return Err(format!(
                "Could not read Carbon window state at {}: {error}",
                path.display()
            ));
        }
    };
    let Some(sanitized) = sanitize_window_state_contents(&contents).map_err(|error| {
        format!(
            "Could not parse Carbon window state at {}: {error}",
            path.display()
        )
    })?
    else {
        return Ok(false);
    };

    replace_window_state_atomically(path, &sanitized)?;
    Ok(true)
}

/// Replace the cache only after a same-directory temporary file is fully written. Windows'
/// `ReplaceFileW` keeps readers from observing a truncated JSON document if the process exits
/// during this one-time repair.
#[cfg(target_os = "windows")]
fn replace_window_state_atomically(path: &Path, contents: &[u8]) -> Result<(), String> {
    use std::fs::OpenOptions;
    use std::os::windows::ffi::OsStrExt;
    use std::ptr::null;

    use windows_sys::Win32::Storage::FileSystem::{ReplaceFileW, REPLACEFILE_WRITE_THROUGH};

    let parent = path
        .parent()
        .ok_or_else(|| format!("Window-state path has no parent: {}", path.display()))?;
    let file_name = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("window-state");
    let mut temporary = None;

    for attempt in 0..16 {
        let candidate = parent.join(format!(
            ".{file_name}.carbon-repair-{}-{attempt}.tmp",
            std::process::id()
        ));
        match OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&candidate)
        {
            Ok(mut file) => {
                if let Err(error) = file.write_all(contents) {
                    drop(file);
                    let _ = fs::remove_file(&candidate);
                    return Err(format!(
                        "Could not write repaired Carbon window state at {}: {error}",
                        candidate.display()
                    ));
                }
                if let Err(error) = file.sync_all() {
                    drop(file);
                    let _ = fs::remove_file(&candidate);
                    return Err(format!(
                        "Could not flush repaired Carbon window state at {}: {error}",
                        candidate.display()
                    ));
                }
                drop(file);
                temporary = Some(candidate);
                break;
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => {
                return Err(format!(
                    "Could not create a temporary Carbon window-state file beside {}: {error}",
                    path.display()
                ));
            }
        }
    }

    let temporary = temporary.ok_or_else(|| {
        format!(
            "Could not reserve a temporary Carbon window-state file beside {}",
            path.display()
        )
    })?;
    let destination_wide = path
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect::<Vec<_>>();
    let temporary_wide = temporary
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect::<Vec<_>>();
    let replaced = unsafe {
        ReplaceFileW(
            destination_wide.as_ptr(),
            temporary_wide.as_ptr(),
            null(),
            REPLACEFILE_WRITE_THROUGH,
            null(),
            null(),
        )
    };
    if replaced == 0 {
        let error = std::io::Error::last_os_error();
        let _ = fs::remove_file(&temporary);
        return Err(format!(
            "Could not atomically update Carbon window state at {}: {error}",
            path.display()
        ));
    }
    Ok(())
}

/// Pure cache transformation used before native window-state initialization. `None` means the
/// parsed cache is safe to leave byte-for-byte untouched.
#[cfg(target_os = "windows")]
fn sanitize_window_state_contents(contents: &[u8]) -> Result<Option<Vec<u8>>, serde_json::Error> {
    let mut cache = serde_json::from_slice::<serde_json::Value>(contents)?;
    if !remove_abnormal_main_window_state(&mut cache) {
        return Ok(None);
    }
    serde_json::to_vec_pretty(&cache).map(Some)
}

#[cfg(target_os = "windows")]
fn remove_abnormal_main_window_state(cache: &mut serde_json::Value) -> bool {
    let Some(windows) = cache.as_object_mut() else {
        return false;
    };
    let Some(main_value) = windows.get("main") else {
        return false;
    };
    let Some(main) = main_value.as_object() else {
        // The plugin expects a map of window labels to complete state objects. An invalid main
        // entry can otherwise make it discard the whole cache, so reset only that entry.
        windows.remove("main");
        return true;
    };

    let width = valid_window_state_dimension(main.get("width"));
    let height = valid_window_state_dimension(main.get("height"));
    let (Some(width), Some(height)) = (width, height) else {
        // Zero, non-finite, non-integral, or out-of-range dimensions are never valid plugin
        // state. Reset them even if the window was last maximized or fullscreen.
        windows.remove("main");
        return true;
    };

    // Be conservative around an incomplete or future plugin format. The current plugin writes
    // both flags, so only an explicitly restored (not maximized/fullscreen) window qualifies.
    if main.get("maximized").and_then(serde_json::Value::as_bool) != Some(false)
        || main.get("fullscreen").and_then(serde_json::Value::as_bool) != Some(false)
    {
        return false;
    }

    if width == CLIPPED_RESTORE_MAIN_WIDTH && height == CLIPPED_RESTORE_MAIN_HEIGHT {
        windows.remove("main");
        return true;
    }
    false
}

/// The plugin stores physical `u32` dimensions. Do not infer intent from malformed values.
#[cfg(target_os = "windows")]
fn valid_window_state_dimension(value: Option<&serde_json::Value>) -> Option<f64> {
    let value = value?.as_f64()?;
    (value.is_finite() && value > 0.0 && value <= f64::from(u32::MAX) && value.fract() == 0.0)
        .then_some(value)
}

fn load_desktop_preferences(app: &tauri::AppHandle) -> Result<DesktopPreferences, String> {
    let path = desktop_preferences_path(app)?;
    match fs::read_to_string(&path) {
        Ok(contents) => match serde_json::from_str::<DesktopPreferences>(&contents) {
            Ok(preferences) => Ok(preferences),
            Err(error) => {
                // A corrupt local preferences file must not stop the local server from starting.
                // Leave it in place for diagnostics and start from safe defaults instead.
                log::warn!("ignoring unreadable Carbon desktop preferences: {error}");
                Ok(DesktopPreferences::default())
            }
        },
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            migrate_legacy_desktop_preferences(app, &path)?
                .map_or_else(|| Ok(DesktopPreferences::default()), Ok)
        }
        Err(error) => Err(format!(
            "Could not read Carbon desktop preferences: {error}"
        )),
    }
}

/// Copy legacy shell preferences once into the Carbon profile. The source is never modified, so
/// a user can still roll back to an older desktop build after upgrading.
fn migrate_legacy_desktop_preferences(
    app: &tauri::AppHandle,
    carbon_preferences_path: &Path,
) -> Result<Option<DesktopPreferences>, String> {
    let carbon_profile = app_data_dir(app)?;
    let mut sources = vec![carbon_profile.join(LEGACY_DESKTOP_PREFERENCES_FILE)];
    if let Some(legacy_profile) = legacy_app_data_dir(&carbon_profile) {
        sources.push(legacy_profile.join(LEGACY_DESKTOP_PREFERENCES_FILE));
    }

    for source in sources {
        let contents = match fs::read_to_string(&source) {
            Ok(contents) => contents,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
            Err(error) => {
                log::warn!(
                    "could not read legacy Carbon desktop preferences at {}: {error}",
                    source.display()
                );
                continue;
            }
        };
        let mut preferences = match serde_json::from_str::<DesktopPreferences>(&contents) {
            Ok(preferences) => preferences,
            Err(error) => {
                log::warn!(
                    "ignoring unreadable legacy Carbon desktop preferences at {}: {error}",
                    source.display()
                );
                continue;
            }
        };

        let legacy_profile = source.parent().ok_or_else(|| {
            format!(
                "Could not determine the legacy desktop profile for {}",
                source.display()
            )
        })?;
        migrate_legacy_notification_sound(app, legacy_profile, &mut preferences)?;
        write_desktop_preferences(carbon_preferences_path, &preferences)?;
        log::info!(
            "migrated desktop preferences from {} to {}",
            source.display(),
            carbon_preferences_path.display()
        );
        return Ok(Some(preferences));
    }

    Ok(None)
}

/// A protected elevated launch must be completely independent of the normal desktop preference
/// file. Keep this decision in one small helper so it can be unit-tested without touching the
/// filesystem.
fn desktop_preferences_for_launch(
    locked_admin_data_home: Option<&Path>,
    load: impl FnOnce() -> Result<DesktopPreferences, String>,
) -> Result<DesktopPreferences, String> {
    match locked_admin_data_home {
        Some(_) => Ok(DesktopPreferences::default()),
        None => load(),
    }
}

fn locked_admin_data_home(app: &tauri::AppHandle) -> Option<PathBuf> {
    app.state::<LockedAdminDataHome>().0.clone()
}

fn desktop_preferences_are_locked(app: &tauri::AppHandle) -> bool {
    locked_admin_data_home(app).is_some()
}

fn reject_locked_desktop_preference_write(app: &tauri::AppHandle) -> Result<(), String> {
    if desktop_preferences_are_locked(app) {
        return Err("Carbon is running with a protected administrator data home. Desktop preferences are intentionally disabled in this elevated session; restart normally to change them.".to_string());
    }
    Ok(())
}

fn save_desktop_preferences(
    app: &tauri::AppHandle,
    preferences: &DesktopPreferences,
) -> Result<(), String> {
    if desktop_preferences_are_locked(app) {
        return Ok(());
    }
    let path = desktop_preferences_path(app)?;
    write_desktop_preferences(&path, preferences)
}

fn write_desktop_preferences(path: &Path, preferences: &DesktopPreferences) -> Result<(), String> {
    let contents = serde_json::to_vec_pretty(preferences)
        .map_err(|error| format!("Could not encode Carbon desktop preferences: {error}"))?;
    let temporary = path.with_extension("json.tmp");
    fs::write(&temporary, contents)
        .map_err(|error| format!("Could not write Carbon desktop preferences: {error}"))?;

    // Windows cannot atomically replace an existing file with rename. The file is an
    // app-owned preference cache, so a short replace window is preferable to writing a partial
    // JSON document in place. The temporary file is in the same trusted directory.
    if path.exists() {
        fs::remove_file(&path)
            .map_err(|error| format!("Could not replace Carbon desktop preferences: {error}"))?;
    }
    fs::rename(&temporary, &path)
        .map_err(|error| format!("Could not finalize Carbon desktop preferences: {error}"))
}

#[tauri::command]
fn is_notification_owner(window: WebviewWindow<tauri::Wry>) -> bool {
    window.label() == "main"
}

fn require_notification_owner(window: &WebviewWindow<tauri::Wry>) -> Result<(), String> {
    if window.label() == "main" {
        Ok(())
    } else {
        Err("Only Carbon's main window may configure or play notification sounds.".into())
    }
}

fn is_safe_local_path(path: &Path) -> bool {
    if !path.is_absolute()
        || path
            .components()
            .any(|component| component == Component::ParentDir)
    {
        return false;
    }
    #[cfg(target_os = "windows")]
    {
        // Deliberately allow only local drive-letter paths. `canonicalize` returns a verbatim
        // disk prefix (`\\?\C:\...`) on Windows, so accept that local form while rejecting UNC,
        // verbatim UNC, device paths, and drive-relative forms before a remote webview can turn
        // a sound picker result into an unexpected network or escaped filesystem read.
        matches!(
            path.components().next(),
            Some(Component::Prefix(prefix)) if matches!(
                prefix.kind(),
                std::path::Prefix::Disk(_) | std::path::Prefix::VerbatimDisk(_)
            )
        )
    }
    #[cfg(not(target_os = "windows"))]
    {
        true
    }
}

#[cfg(target_os = "windows")]
fn is_reparse_point(path: &Path) -> Result<bool, String> {
    use std::os::windows::fs::MetadataExt;

    const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x0000_0400;
    let metadata = fs::symlink_metadata(path)
        .map_err(|error| format!("Could not inspect selected sound: {error}"))?;
    Ok(metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

#[cfg(not(target_os = "windows"))]
fn is_reparse_point(path: &Path) -> Result<bool, String> {
    Ok(fs::symlink_metadata(path)
        .map_err(|error| format!("Could not inspect selected sound: {error}"))?
        .file_type()
        .is_symlink())
}

fn audio_extension(path: &Path) -> Result<String, String> {
    let extension = path
        .extension()
        .and_then(|extension| extension.to_str())
        .map(|extension| extension.to_ascii_lowercase())
        .ok_or_else(|| "The selected notification sound has no file extension.".to_string())?;
    #[cfg(target_os = "windows")]
    if extension != "wav" {
        return Err("Windows custom notification sounds must be WAV files.".to_string());
    }
    #[cfg(not(target_os = "windows"))]
    if !matches!(extension.as_str(), "wav" | "mp3" | "m4a" | "aif" | "aiff") {
        return Err("Select a WAV, MP3, M4A, AIFF, or AIF audio file.".to_string());
    }
    Ok(extension)
}

fn validate_wav_header(path: &Path) -> Result<(), String> {
    let mut file = fs::File::open(path)
        .map_err(|error| format!("Could not read selected notification sound: {error}"))?;
    let mut header = [0_u8; 12];
    file.read_exact(&mut header)
        .map_err(|_| "The selected WAV file is too small or invalid.".to_string())?;
    if &header[..4] != b"RIFF" || &header[8..] != b"WAVE" {
        return Err("The selected WAV file does not have a valid RIFF/WAVE header.".to_string());
    }
    Ok(())
}

fn validate_notification_sound_source(path: &Path) -> Result<(PathBuf, String, String), String> {
    if !is_safe_local_path(path) {
        return Err(
            "Select a local absolute audio file; network/UNC and traversal paths are not allowed."
                .to_string(),
        );
    }
    if is_reparse_point(path)? {
        return Err(
            "Symbolic-link and reparse-point notification sounds are not allowed.".to_string(),
        );
    }
    let metadata = fs::metadata(path)
        .map_err(|error| format!("Could not inspect selected notification sound: {error}"))?;
    if !metadata.is_file() {
        return Err("The selected notification sound is not a file.".to_string());
    }
    if metadata.len() == 0 || metadata.len() > MAX_NOTIFICATION_SOUND_BYTES {
        return Err(format!(
            "Notification sounds must be between 1 byte and {MAX_NOTIFICATION_SOUND_BYTES} bytes."
        ));
    }
    let canonical = fs::canonicalize(path)
        .map_err(|error| format!("Could not resolve selected notification sound: {error}"))?;
    if !is_safe_local_path(&canonical) {
        return Err(
            "The resolved notification sound escapes to a network or unsafe location.".to_string(),
        );
    }
    let extension = audio_extension(&canonical)?;
    if extension == "wav" {
        validate_wav_header(&canonical)?;
    }
    let name = canonical
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty())
        .ok_or_else(|| "The selected notification sound has an invalid file name.".to_string())?
        .to_string();
    Ok((canonical, name, extension))
}

fn notification_sound_directory(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    let root = app_data_dir(app)?;
    let canonical_root = fs::canonicalize(&root)
        .map_err(|error| format!("Could not resolve Carbon app-data directory: {error}"))?;
    let directory = root.join(NOTIFICATION_SOUNDS_DIR);
    fs::create_dir_all(&directory).map_err(|error| {
        format!("Could not create Carbon notification-sound directory: {error}")
    })?;
    if is_reparse_point(&directory)? {
        return Err(
            "Carbon notification-sound directory may not be a symbolic link or reparse point."
                .to_string(),
        );
    }
    let canonical_directory = fs::canonicalize(&directory).map_err(|error| {
        format!("Could not resolve Carbon notification-sound directory: {error}")
    })?;
    if !canonical_directory.starts_with(&canonical_root) {
        return Err(
            "Carbon notification-sound directory escapes the app-data directory.".to_string(),
        );
    }
    Ok(canonical_directory)
}

fn notification_sound_path(
    app: &tauri::AppHandle,
    sound: &NotificationSound,
) -> Result<PathBuf, String> {
    // `extension` only ever comes from `audio_extension`; repeat the character check before it
    // contributes to a filename so a damaged preferences file cannot create path traversal.
    Ok(notification_sound_directory(app)?.join(notification_sound_file_name(sound)?))
}

fn notification_sound_file_name(sound: &NotificationSound) -> Result<String, String> {
    if sound.extension.is_empty()
        || sound.extension.len() > 8
        || !sound
            .extension
            .chars()
            .all(|character| character.is_ascii_alphanumeric())
    {
        return Err("Carbon saved an invalid notification-sound extension.".to_string());
    }
    Ok(format!("notification.{}", sound.extension))
}

/// Preserve a selected notification sound when its preferences migrate from the legacy profile.
/// The metadata is cleared if the legacy file is missing rather than leaving a broken selection.
fn migrate_legacy_notification_sound(
    app: &tauri::AppHandle,
    legacy_profile: &Path,
    preferences: &mut DesktopPreferences,
) -> Result<(), String> {
    let Some(sound) = preferences.notification_sound.clone() else {
        return Ok(());
    };
    let source = legacy_profile
        .join(NOTIFICATION_SOUNDS_DIR)
        .join(notification_sound_file_name(&sound)?);
    if !source.is_file() {
        preferences.notification_sound = None;
        return Ok(());
    }

    let destination = notification_sound_path(app, &sound)?;
    if source == destination || destination.exists() {
        return Ok(());
    }
    let temporary = destination.with_extension(format!("{}.tmp", sound.extension));
    fs::copy(&source, &temporary).map_err(|error| {
        format!(
            "Could not migrate Carbon notification sound from {}: {error}",
            source.display()
        )
    })?;
    if destination.exists() {
        fs::remove_file(&destination)
            .map_err(|error| format!("Could not replace Carbon notification sound: {error}"))?;
    }
    fs::rename(&temporary, &destination)
        .map_err(|error| format!("Could not save Carbon notification sound: {error}"))
}

fn import_notification_sound(
    app: &tauri::AppHandle,
    source: &Path,
) -> Result<NotificationSound, String> {
    reject_locked_desktop_preference_write(app)?;
    let (source, name, extension) = validate_notification_sound_source(source)?;
    let sound = NotificationSound { name, extension };
    let destination = notification_sound_path(app, &sound)?;
    let temporary = destination.with_extension(format!("{}.tmp", sound.extension));
    fs::copy(&source, &temporary).map_err(|error| {
        format!("Could not copy notification sound into Carbon app data: {error}")
    })?;
    if sound.extension == "wav" {
        validate_wav_header(&temporary)?;
    }
    if destination.exists() {
        fs::remove_file(&destination)
            .map_err(|error| format!("Could not replace Carbon notification sound: {error}"))?;
    }
    fs::rename(&temporary, &destination)
        .map_err(|error| format!("Could not save Carbon notification sound: {error}"))?;

    let state = app.state::<DesktopPreferencesState>();
    let mut preferences = state.0.lock().unwrap();
    preferences.notification_sound = Some(sound.clone());
    save_desktop_preferences(app, &preferences)?;
    Ok(sound)
}

/// Opens the OS chooser itself instead of accepting a frontend-supplied path. That makes the
/// import boundary explicit and prevents a localhost webview from requesting arbitrary UNC or
/// escaped files. The validated file is copied into Carbon app data before it is remembered.
#[tauri::command]
async fn choose_notification_sound(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
) -> Result<Option<NotificationSound>, String> {
    require_notification_owner(&window)?;
    let dialog = app
        .dialog()
        .file()
        .set_parent(&window)
        .set_title("Select Carbon notification sound");
    #[cfg(target_os = "windows")]
    let dialog = dialog.add_filter("WAV audio", &["wav"]);
    #[cfg(not(target_os = "windows"))]
    let dialog = dialog.add_filter("Audio", &["wav", "mp3", "m4a", "aif", "aiff"]);
    let Some(selected) = dialog.blocking_pick_file() else {
        return Ok(None);
    };
    let source = match selected {
        FilePath::Path(path) => path,
        FilePath::Url(_) => {
            return Err("Only local files may be used as Carbon notification sounds.".to_string())
        }
    };
    import_notification_sound(&app, &source).map(Some)
}

#[tauri::command]
fn get_notification_sound(
    state: tauri::State<'_, DesktopPreferencesState>,
) -> Option<NotificationSound> {
    state.0.lock().unwrap().notification_sound.clone()
}

#[tauri::command]
fn clear_notification_sound(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
) -> Result<(), String> {
    require_notification_owner(&window)?;
    reject_locked_desktop_preference_write(&app)?;
    let sound = app
        .state::<DesktopPreferencesState>()
        .0
        .lock()
        .unwrap()
        .notification_sound
        .clone();
    if let Some(sound) = sound {
        let path = notification_sound_path(&app, &sound)?;
        if path.exists() {
            fs::remove_file(path)
                .map_err(|error| format!("Could not remove Carbon notification sound: {error}"))?;
        }
    }
    let state = app.state::<DesktopPreferencesState>();
    let mut preferences = state.0.lock().unwrap();
    preferences.notification_sound = None;
    save_desktop_preferences(&app, &preferences)
}

#[tauri::command]
fn play_notification_sound(
    app: tauri::AppHandle,
    window: WebviewWindow<tauri::Wry>,
) -> Result<bool, String> {
    require_notification_owner(&window)?;
    let Some(sound) = app
        .state::<DesktopPreferencesState>()
        .0
        .lock()
        .unwrap()
        .notification_sound
        .clone()
    else {
        return Ok(false);
    };
    let path = notification_sound_path(&app, &sound)?;
    if !path.is_file() {
        return Err(
            "Carbon's selected notification sound is missing. Choose it again.".to_string(),
        );
    }

    #[cfg(target_os = "windows")]
    {
        use std::os::windows::ffi::OsStrExt;
        use windows_sys::Win32::Media::Audio::{
            PlaySoundW, SND_ASYNC, SND_FILENAME, SND_NODEFAULT,
        };

        // WAV was validated at import and is the only accepted Windows custom sound format.
        let wide = path
            .as_os_str()
            .encode_wide()
            .chain(Some(0))
            .collect::<Vec<_>>();
        let played = unsafe {
            PlaySoundW(
                wide.as_ptr(),
                std::ptr::null_mut(),
                SND_ASYNC | SND_FILENAME | SND_NODEFAULT,
            )
        };
        if played == 0 {
            return Err("Windows could not play Carbon's custom notification sound.".to_string());
        }
        return Ok(true);
    }

    #[cfg(not(target_os = "windows"))]
    {
        // The selected sound remains available to a future native player on this platform. The
        // browser/native notification service may provide its own sound policy meanwhile.
        let _ = path;
        Ok(false)
    }
}

fn default_data_home(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    if app.state::<PortableMode>().0 {
        return std::env::current_exe()
            .map_err(|error| format!("Could not locate Carbon Portable executable: {error}"))?
            .parent()
            .map(Path::to_path_buf)
            .ok_or_else(|| "Carbon Portable executable has no parent directory.".to_string());
    }
    Ok(app_data_dir(app)?.join(PRODUCT_NAME))
}

/// Reject data homes that cross a symlink, junction, mount point, or another reparse point.
/// `canonicalize` alone is insufficient because it erases the offending component from the
/// returned path; inspect the requested chain both before and after directory creation.
#[cfg(target_os = "windows")]
fn ensure_no_reparse_path_components(path: &Path) -> Result<(), String> {
    if !is_safe_local_path(path) {
        return Err("Carbon data home must be a local absolute drive path; UNC, device, and traversal paths are not allowed."
            .to_string());
    }

    let mut current = PathBuf::new();
    for component in path.components() {
        current.push(component.as_os_str());
        if !matches!(component, Component::Normal(_)) {
            continue;
        }
        match fs::symlink_metadata(&current) {
            Ok(metadata) => {
                use std::os::windows::fs::MetadataExt;

                const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x0000_0400;
                if metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
                    return Err(format!(
                        "Carbon data home may not contain a symbolic-link, junction, or reparse-point component: {}",
                        current.display()
                    ));
                }
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(format!(
                    "Could not inspect Carbon data-home component {}: {error}",
                    current.display()
                ))
            }
        }
    }
    Ok(())
}

/// `std::fs::canonicalize` returns a verbatim drive path (`\\?\C:\...`) on Windows.
/// That spelling is useful while inspecting the filesystem, but it must not cross the desktop
/// trust boundary: the Go server deliberately rejects device/verbatim paths supplied by clients.
/// Convert only the one canonical form we already proved is a local drive, and keep UNC/device
/// namespaces fail-closed.
#[cfg(target_os = "windows")]
fn portable_local_drive_path(path: &Path) -> Result<PathBuf, String> {
    let value = path
        .to_str()
        .ok_or_else(|| "Carbon data home is not valid Unicode.".to_string())?;
    let Some(value) = value.strip_prefix(r"\\?\") else {
        return Ok(path.to_path_buf());
    };
    let bytes = value.as_bytes();
    if bytes.len() < 3 || !bytes[0].is_ascii_alphabetic() || bytes[1] != b':' || bytes[2] != b'\\' {
        return Err(
            "Carbon data home resolved outside a local Windows drive namespace.".to_string(),
        );
    }
    let portable = PathBuf::from(value);
    if !is_safe_local_path(&portable) {
        return Err("Carbon data home resolves to an unsafe location.".to_string());
    }
    Ok(portable)
}

/// Resolve an already-existing data home immediately before it is used by the sidecar.  The
/// returned value is the canonical path; callers must not reuse the original lexical spelling.
fn resolve_existing_data_home(path: &Path) -> Result<PathBuf, String> {
    if !is_safe_local_path(path) {
        return Err("Carbon data home must be a local absolute directory; UNC/network, device, and traversal paths are not allowed."
            .to_string());
    }
    #[cfg(target_os = "windows")]
    ensure_no_reparse_path_components(path)?;

    let resolved = fs::canonicalize(path)
        .map_err(|error| format!("Could not resolve Carbon data home: {error}"))?;
    if !is_safe_local_path(&resolved) || !resolved.is_dir() {
        return Err("Carbon data home resolves to an unsafe location.".to_string());
    }
    #[cfg(target_os = "windows")]
    {
        ensure_no_reparse_path_components(&resolved)?;
        return portable_local_drive_path(&resolved);
    }
    #[cfg(not(target_os = "windows"))]
    Ok(resolved)
}

fn normalize_data_home(path: &Path) -> Result<PathBuf, String> {
    if !is_safe_local_path(path) {
        return Err("Carbon data home must be a local absolute directory; UNC/network, device, and traversal paths are not allowed."
            .to_string());
    }
    #[cfg(target_os = "windows")]
    ensure_no_reparse_path_components(path)?;
    fs::create_dir_all(path)
        .map_err(|error| format!("Could not create Carbon data home: {error}"))?;
    // A missing descendant may have been created below a newly inserted reparse point; repeat
    // the component inspection after `create_dir_all` before returning the canonical path.
    resolve_existing_data_home(path)
}

fn configured_data_home(
    app: &tauri::AppHandle,
    configured: Option<&str>,
) -> Result<PathBuf, String> {
    match configured {
        Some(path) => normalize_data_home(Path::new(path)),
        None => normalize_data_home(&default_data_home(app)?),
    }
}

fn data_home_paths_match(left: &Path, right: &Path) -> bool {
    #[cfg(target_os = "windows")]
    {
        let left = left.to_string_lossy();
        let right = right.to_string_lossy();
        left.as_ref().eq_ignore_ascii_case(right.as_ref())
    }

    #[cfg(not(target_os = "windows"))]
    {
        left == right
    }
}

fn data_home_status_from_paths(active: &Path, default: &Path, next: &Path) -> DataHome {
    let pending_path =
        (!data_home_paths_match(active, next)).then(|| next.to_string_lossy().into_owned());
    DataHome {
        path: active.to_string_lossy().into_owned(),
        is_default: data_home_paths_match(active, default),
        restart_required: pending_path.is_some(),
        pending_path,
    }
}

fn locked_data_home_status(path: &Path) -> DataHome {
    DataHome {
        path: path.to_string_lossy().into_owned(),
        // Do not calculate the normal default in an elevated locked session: doing so would
        // touch the user-writable app-data area and would make the returned status ambiguous.
        is_default: false,
        pending_path: None,
        restart_required: false,
    }
}

fn data_home_change_is_allowed(locked_admin_data_home: Option<&Path>) -> bool {
    locked_admin_data_home.is_none()
}

fn data_home_status(app: &tauri::AppHandle) -> Result<DataHome, String> {
    if let Some(locked) = locked_admin_data_home(app) {
        return Ok(locked_data_home_status(&locked));
    }
    let active = app.state::<LaunchDataHome>().0.clone();
    let configured = app
        .state::<DesktopPreferencesState>()
        .0
        .lock()
        .unwrap()
        .data_home
        .clone();
    let default = configured_data_home(app, None)?;
    let next = configured_data_home(app, configured.as_deref())?;
    Ok(data_home_status_from_paths(&active, &default, &next))
}

#[tauri::command]
fn get_data_home(app: tauri::AppHandle) -> Result<DataHome, String> {
    data_home_status(&app)
}

/// Persist a requested sidecar home for the next launch. This never moves project data or
/// restarts behind the user's back; the frontend can clearly ask the user to restart/migrate.
#[tauri::command]
fn set_data_home(app: tauri::AppHandle, path: String) -> Result<DataHome, String> {
    let locked_admin_data_home = locked_admin_data_home(&app);
    if !data_home_change_is_allowed(locked_admin_data_home.as_deref()) {
        return Err("Carbon is running with a protected administrator data home. Changing the data home is disabled for this elevated session; restart normally to change it.".to_string());
    }
    let path = normalize_data_home(Path::new(&path))?;
    let value = path.to_string_lossy().into_owned();
    {
        let state = app.state::<DesktopPreferencesState>();
        let mut preferences = state.0.lock().unwrap();
        preferences.data_home = Some(value);
        save_desktop_preferences(&app, &preferences)?;
    }
    data_home_status(&app)
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum AutostartMode {
    Off,
    User,
    Admin,
}

impl AutostartMode {
    fn parse(value: &str) -> Result<Self, String> {
        match value {
            "off" => Ok(Self::Off),
            "user" => Ok(Self::User),
            "admin" => Ok(Self::Admin),
            _ => Err("Autostart mode must be 'off', 'user', or 'admin'.".to_string()),
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::Off => "off",
            Self::User => "user",
            Self::Admin => "admin",
        }
    }
}

/// Report the active login-start mode. Windows gives the protected Task Scheduler entry
/// priority over the ordinary HKCU Run entry, so callers never see two enabled modes.
#[tauri::command]
async fn get_autostart_mode(app: tauri::AppHandle) -> Result<String, String> {
    #[cfg(target_os = "windows")]
    {
        let _ = app;
        // `schtasks`, ACL validation, registry reads, and the named mutex can all wait on
        // Windows. Tauri may dispatch this command from the webview/UI path, so isolate the
        // complete native operation on the blocking pool rather than delaying input or paint.
        return tauri::async_runtime::spawn_blocking(windows_autostart::get_mode)
            .await
            .map_err(|error| format!("Could not read Carbon autostart mode: {error}"))?
            .map(|mode| mode.as_str().to_string());
    }

    #[cfg(all(
        not(target_os = "windows"),
        not(any(target_os = "android", target_os = "ios"))
    ))]
    {
        return tauri::async_runtime::spawn_blocking(move || {
            use tauri_plugin_autostart::ManagerExt;
            app.autolaunch()
                .is_enabled()
                .map(|enabled| {
                    if enabled {
                        AutostartMode::User.as_str().to_string()
                    } else {
                        AutostartMode::Off.as_str().to_string()
                    }
                })
                .map_err(|error| error.to_string())
        })
        .await
        .map_err(|error| format!("Could not read Carbon autostart mode: {error}"))?;
    }

    #[cfg(any(target_os = "android", target_os = "ios"))]
    {
        let _ = app;
        Ok(AutostartMode::Off.as_str().to_string())
    }
}

/// Set a login-start mode. The custom Windows implementation is intentionally not the
/// upstream autostart plugin: it must preserve a quoted portable executable path and avoid
/// granting elevated startup from a user-writable directory.
#[tauri::command]
async fn set_autostart_mode(app: tauri::AppHandle, mode: String) -> Result<String, String> {
    let mode = AutostartMode::parse(&mode)?;

    #[cfg(target_os = "windows")]
    {
        let active_data_home = app.state::<LaunchDataHome>().0.clone();
        // Registration can invoke UAC, acquire the process-wide lock, query/delete a task,
        // and inspect filesystem ACLs. Never make the webview/UI event path wait for it.
        return tauri::async_runtime::spawn_blocking(move || {
            windows_autostart::set_mode(mode, &active_data_home)
                .map(|active| active.as_str().to_string())
        })
        .await
        .map_err(|error| format!("Could not save Carbon autostart mode: {error}"))?;
    }

    #[cfg(all(
        not(target_os = "windows"),
        not(any(target_os = "android", target_os = "ios"))
    ))]
    {
        return tauri::async_runtime::spawn_blocking(move || {
            use tauri_plugin_autostart::ManagerExt;
            match mode {
                AutostartMode::Off => app.autolaunch().disable(),
                AutostartMode::User => app.autolaunch().enable(),
                AutostartMode::Admin => {
                    return Err(
                        "Administrator autostart is currently supported on Windows only."
                            .to_string(),
                    )
                }
            }
            .map_err(|error| error.to_string())?;
            Ok(mode.as_str().to_string())
        })
        .await
        .map_err(|error| format!("Could not save Carbon autostart mode: {error}"))?;
    }

    #[cfg(any(target_os = "android", target_os = "ios"))]
    {
        let _ = app;
        match mode {
            AutostartMode::Off => Ok(AutostartMode::Off.as_str().to_string()),
            _ => Err("Autostart is not available on mobile platforms.".to_string()),
        }
    }
}

/// Rebuild the native application menu immediately. The frontend owns the dynamic tray model;
/// before it hydrates, the fallback tray is also rebuilt here.
#[tauri::command]
fn set_ui_language(app: tauri::AppHandle, language: String) -> Result<String, String> {
    let language = UiLanguage::parse(&language)?;
    *app.state::<NativeLanguage>().0.lock().unwrap() = language;

    let menu = build_menu(&app, language).map_err(|error| error.to_string())?;
    app.set_menu(menu).map_err(|error| error.to_string())?;

    let dynamic = *app.state::<DynamicTray>().0.lock().unwrap();
    if !dynamic {
        if let Some(tray) = app.tray_by_id("main") {
            let fallback =
                build_fallback_tray_menu(&app, language).map_err(|error| error.to_string())?;
            tray.set_menu(Some(fallback))
                .map_err(|error| error.to_string())?;
            let _ = tray.set_tooltip(Some(native_text(language, PRODUCT_NAME, PRODUCT_NAME)));
        }
    }

    Ok(language.code().to_string())
}

fn portable_mode() -> bool {
    #[cfg(target_os = "windows")]
    {
        std::env::current_exe()
            .ok()
            .is_some_and(|exe| has_portable_marker(&exe))
    }

    #[cfg(not(target_os = "windows"))]
    {
        false
    }
}

#[cfg(target_os = "windows")]
fn has_portable_marker(executable: &Path) -> bool {
    executable.parent().is_some_and(|dir| {
        dir.join(PORTABLE_MARKER).is_file()
                // A pre-Carbon portable archive must remain portable after a desktop-binary
                // upgrade, even though newly built archives only write the Carbon marker.
                || dir.join(LEGACY_PORTABLE_MARKER).is_file()
    })
}

/// A tray menu pushed from the frontend. Rust is a dumb renderer: the UI owns the content
/// and the click logic (clicks come back as a single `tray:menu` event carrying the item id).
#[derive(serde::Deserialize)]
struct TrayItem {
    id: String,
    label: String,
    checked: Option<bool>,
    enabled: Option<bool>,
}

#[derive(serde::Deserialize)]
struct TrayMenu {
    tooltip: String,
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    title: String,
    sections: Vec<Vec<TrayItem>>,
}

/// Rebuild the tray menu from a frontend-supplied model + set the tooltip / macOS menubar
/// title (the attention badge). Sections are separated by a divider.
#[tauri::command]
fn update_tray(app: tauri::AppHandle, menu: TrayMenu) -> Result<(), String> {
    let Some(tray) = app.tray_by_id("main") else {
        return Ok(());
    };
    *app.state::<DynamicTray>().0.lock().unwrap() = true;
    let mut b = MenuBuilder::new(&app);
    for (i, section) in menu.sections.iter().enumerate() {
        if i > 0 {
            b = b.separator();
        }
        for item in section {
            // Drop a stale frontend's retired updater item instead of rendering an entry that
            // could be forwarded back to its old update handler. Carbon updates are manual-only.
            if item.id == "check_updates" {
                continue;
            }
            if let Some(checked) = item.checked {
                let ci = CheckMenuItemBuilder::with_id(&item.id, &item.label)
                    .checked(checked)
                    .build(&app)
                    .map_err(|e| e.to_string())?;
                b = b.item(&ci);
            } else {
                let mi = MenuItemBuilder::with_id(&item.id, &item.label)
                    .enabled(item.enabled.unwrap_or(true))
                    .build(&app)
                    .map_err(|e| e.to_string())?;
                b = b.item(&mi);
            }
        }
    }
    let built = b.build().map_err(|e| e.to_string())?;
    tray.set_menu(Some(built)).map_err(|e| e.to_string())?;
    let _ = tray.set_tooltip(Some(&menu.tooltip));
    #[cfg(target_os = "macos")]
    {
        let _ = tray.set_title(Some(&menu.title));
    }
    Ok(())
}

fn native_text(language: UiLanguage, english: &'static str, chinese: &'static str) -> &'static str {
    match language {
        UiLanguage::English => english,
        UiLanguage::Chinese => chinese,
    }
}

/// Build the application menu. The Edit submenu is what gives macOS webviews working
/// copy/paste/select-all in text inputs. Custom items emit `menu:*` events to the UI.
fn build_menu(
    app: &tauri::AppHandle,
    language: UiLanguage,
) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    let settings =
        MenuItemBuilder::with_id("settings", native_text(language, "Settings…", "设置…"))
            .accelerator("CmdOrCtrl+,")
            .build(app)?;
    let new_task =
        MenuItemBuilder::with_id("new_task", native_text(language, "New Task", "新建任务"))
            .accelerator("CmdOrCtrl+N")
            .build(app)?;
    let open_folder = MenuItemBuilder::with_id(
        "open_folder",
        native_text(language, "Open Folder…", "打开文件夹…"),
    )
    .accelerator("CmdOrCtrl+O")
    .build(app)?;
    let board =
        MenuItemBuilder::with_id("board", native_text(language, "Board", "看板")).build(app)?;
    let graph =
        MenuItemBuilder::with_id("graph", native_text(language, "Graph", "关系图")).build(app)?;
    let app_menu = SubmenuBuilder::new(app, native_text(language, PRODUCT_NAME, PRODUCT_NAME))
        .about(None)
        .item(&settings)
        .separator()
        .services()
        .separator()
        .hide()
        .hide_others()
        .show_all()
        .separator()
        .quit()
        .build()?;
    let file_menu = SubmenuBuilder::new(app, native_text(language, "File", "文件"))
        .item(&new_task)
        .item(&open_folder)
        .build()?;
    let edit_menu = SubmenuBuilder::new(app, native_text(language, "Edit", "编辑"))
        .undo()
        .redo()
        .separator()
        .cut()
        .copy()
        .paste()
        .select_all()
        .build()?;
    let view_menu = SubmenuBuilder::new(app, native_text(language, "View", "视图"))
        .item(&board)
        .item(&graph)
        .build()?;
    let window_menu = SubmenuBuilder::new(app, native_text(language, "Window", "窗口"))
        .minimize()
        .separator()
        .close_window()
        .build()?;

    MenuBuilder::new(app)
        .items(&[&app_menu, &file_menu, &edit_menu, &view_menu, &window_menu])
        .build()
}

/// Map a menu/tray item id to a UI event (or a direct action).
fn handle_menu(app: &tauri::AppHandle, id: &str) {
    match id {
        "tray_open" | "open" => show_main(app),
        "tray_quit" | "quit" => app.exit(0),
        "new_task" | "tray_new_task" => {
            show_main(app);
            let _ = app.emit("menu:new_task", ());
        }
        "settings" | "tray_settings" => {
            show_main(app);
            let _ = app.emit("menu:settings", ());
        }
        "open_folder" => {
            show_main(app);
            let _ = app.emit("menu:open_folder", ());
        }
        "board" => {
            show_main(app);
            let _ = app.emit("menu:board", ());
        }
        "graph" => {
            show_main(app);
            let _ = app.emit("menu:graph", ());
        }
        // Dynamic tray items (task:<id>, filter:<f>, project:<slug>, capture, toggle:dnd, …):
        // the frontend owns the action. Toggles shouldn't steal focus; everything else reveals.
        other => {
            if !other.starts_with("toggle:") {
                show_main(app);
            }
            let _ = app.emit("tray:menu", other);
        }
    }
}

/// Tray icon: left-click opens the live menu (built by the frontend via update_tray); the
/// default menu below is the pre-hydration fallback. "Open Carbon" reveals the window.
fn build_fallback_tray_menu(
    app: &tauri::AppHandle,
    language: UiLanguage,
) -> tauri::Result<tauri::menu::Menu<tauri::Wry>> {
    let open_i = MenuItemBuilder::with_id(
        "tray_open",
        native_text(language, "Open Carbon", "打开 Carbon"),
    )
    .build(app)?;
    let new_i = MenuItemBuilder::with_id(
        "tray_new_task",
        native_text(language, "New Task", "新建任务"),
    )
    .build(app)?;
    let settings_i =
        MenuItemBuilder::with_id("tray_settings", native_text(language, "Settings…", "设置…"))
            .build(app)?;
    let quit_i = MenuItemBuilder::with_id(
        "tray_quit",
        native_text(language, "Quit Carbon", "退出 Carbon"),
    )
    .build(app)?;
    MenuBuilder::new(app)
        .items(&[&open_i, &new_i, &settings_i])
        .separator()
        .item(&quit_i)
        .build()
}

fn build_tray(app: &mut tauri::App, language: UiLanguage) -> tauri::Result<()> {
    let tray_menu = build_fallback_tray_menu(app.handle(), language)?;

    let icon = app
        .default_window_icon()
        .cloned()
        .expect("app must have a default icon");

    TrayIconBuilder::with_id("main")
        .icon(icon)
        .tooltip(native_text(language, PRODUCT_NAME, PRODUCT_NAME))
        .menu(&tray_menu)
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| handle_menu(app, event.id().as_ref()))
        .build(app)?;
    Ok(())
}

/// Show + focus the main window (restoring it if minimized/hidden).
fn show_main(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

fn require_main_window(window: &WebviewWindow<tauri::Wry>) -> Result<(), String> {
    if window.label() == "main" {
        Ok(())
    } else {
        Err("Only Carbon's main window may open the floating board.".into())
    }
}

/// Catalog and task IDs are opaque server identifiers, not user-entered paths.  Keep their
/// grammar deliberately tiny because these values are embedded into a hash route by the native
/// shell.  The frontend validates the same grammar before invoke; this check is the authority.
fn is_safe_floating_metadata(value: &str) -> bool {
    let bytes = value.as_bytes();
    if bytes.is_empty() || bytes.len() > 160 || !bytes[0].is_ascii_alphanumeric() {
        return false;
    }
    bytes
        .iter()
        .all(|byte| byte.is_ascii_alphanumeric() || matches!(*byte, b'.' | b'_' | b'-' | b':'))
}

fn validate_floating_board_target(target: &FloatingBoardTarget) -> Result<(), String> {
    if !is_safe_floating_metadata(&target.workspace_project_id) {
        return Err("Floating board workspace project metadata is invalid.".into());
    }
    if let Some(cluster_id) = target.cluster_id.as_deref() {
        if !is_safe_floating_metadata(cluster_id) {
            return Err("Floating board cluster metadata is invalid.".into());
        }
    }
    match target.project_id.as_deref() {
        Some(project_id) if !is_safe_floating_metadata(project_id) => {
            return Err("Floating board project metadata is invalid.".into())
        }
        // A project feed is always tied to its workspace chrome. Letting them differ would make
        // a task click reopen another project than the one that supplied the task data.
        Some(project_id) if project_id != target.workspace_project_id => {
            return Err("Floating board project metadata does not match its workspace.".into())
        }
        None if target.cluster_id.is_none() => {
            return Err("A floating board needs a project or cluster scope.".into())
        }
        _ => {}
    }
    Ok(())
}

fn validate_floating_task_target(target: &FloatingTaskTarget) -> Result<(), String> {
    validate_floating_board_target(&FloatingBoardTarget {
        cluster_id: target.cluster_id.clone(),
        project_id: target.project_id.clone(),
        workspace_project_id: target.workspace_project_id.clone(),
    })?;
    if !is_safe_floating_metadata(&target.task_id) {
        return Err("Floating board task metadata is invalid.".into());
    }
    Ok(())
}

fn floating_board_fragment(target: &FloatingBoardTarget) -> String {
    let mut query = format!("workspace={}", target.workspace_project_id);
    if let Some(cluster_id) = target.cluster_id.as_deref() {
        query.push_str(&format!("&cluster={cluster_id}"));
    }
    if let Some(project_id) = target.project_id.as_deref() {
        query.push_str(&format!("&project={project_id}"));
    }
    format!("floating-board?{query}")
}

fn main_task_fragment(target: &FloatingTaskTarget) -> String {
    match (target.cluster_id.as_deref(), target.project_id.as_deref()) {
        // A cluster feed is intentionally reopened with its cluster scope. The workspace project
        // supplies surrounding chrome; task lookup itself remains within the selected cluster.
        (Some(cluster_id), None) => format!(
            "carbon/{cluster_id}/{}/task/{}?taskScope=cluster",
            target.workspace_project_id, target.task_id
        ),
        (Some(cluster_id), Some(_)) => format!(
            "carbon/{cluster_id}/{}/task/{}",
            target.workspace_project_id, target.task_id
        ),
        (None, Some(_)) => format!(
            "carbon/project/{}/task/{}",
            target.workspace_project_id, target.task_id
        ),
        // validate_floating_task_target rejects this before route construction. Preserve a
        // harmless fragment for exhaustive matching if this helper is reused in tests.
        (None, None) => "carbon".to_string(),
    }
}

/// The sidecar emits this endpoint over stdout.  Still validate it at the command boundary so
/// only the exact loopback HTTP endpoint can be used to construct a second webview URL.
fn current_server_url(app: &tauri::AppHandle) -> Result<tauri::Url, String> {
    let value = app
        .state::<ServerUrl>()
        .0
        .lock()
        .map_err(|_| "Carbon's local server state is unavailable.".to_string())?
        .clone()
        .ok_or_else(|| "Carbon's local server is still starting.".to_string())?;
    let url = tauri::Url::parse(&value)
        .map_err(|_| "Carbon's local server endpoint is invalid.".to_string())?;
    if url.scheme() != "http"
        || url.host_str() != Some("127.0.0.1")
        || url.port().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return Err("Carbon's local server endpoint is not trusted.".into());
    }
    Ok(url)
}

fn floating_board_url(
    app: &tauri::AppHandle,
    target: &FloatingBoardTarget,
) -> Result<tauri::Url, String> {
    let mut url = current_server_url(app)?;
    url.set_path("/");
    url.set_query(None);
    url.set_fragment(Some(&floating_board_fragment(target)));
    Ok(url)
}

fn main_task_url(
    app: &tauri::AppHandle,
    target: &FloatingTaskTarget,
) -> Result<tauri::Url, String> {
    let mut url = current_server_url(app)?;
    url.set_fragment(Some(&main_task_fragment(target)));
    Ok(url)
}

/// Open (or focus) the quick-capture window at the running server's `#capture` route.
/// Falls back to showing the main window when the server URL isn't known yet (dev).
fn open_capture(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window("capture") {
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let url = app.state::<ServerUrl>().0.lock().unwrap().clone();
    let Some(base) = url else {
        return show_main(app);
    };
    let Ok(parsed) = tauri::Url::parse(&format!("{base}/#capture")) else {
        return;
    };
    let language = *app.state::<NativeLanguage>().0.lock().unwrap();
    let _ = WebviewWindowBuilder::new(app, "capture", WebviewUrl::External(parsed))
        .title(native_text(
            language,
            "Quick add — Carbon",
            "快速添加 — Carbon",
        ))
        .inner_size(560.0, 220.0)
        .resizable(false)
        .always_on_top(true)
        .center()
        .build();
}

/// Extract the URL from a Carbon sidecar handshake. The legacy spelling is accepted only while
/// upgrading an already-installed desktop/sidecar pair.
fn parse_url(line: &str) -> Option<String> {
    line.trim()
        .strip_prefix(CARBON_WEB_URL_PREFIX)
        .or_else(|| line.trim().strip_prefix(LEGACY_CAIRN_WEB_URL_PREFIX))
        .map(|u| u.trim().to_string())
}

fn is_supported_deep_link(value: &str) -> bool {
    value.starts_with("carbon://")
}

/// The desktop UI is served from a dynamic loopback port, so the capability manifest must allow
/// `127.0.0.1:*`. Compensate at the webview boundary: once the sidecar reports its URL, only that
/// exact origin may be loaded. This prevents an unrelated local HTTP service from inheriting
/// native commands (notably the autostart command) if a link tries to navigate the webview.
fn navigation_allowed<R: tauri::Runtime>(webview: &tauri::Webview<R>, url: &tauri::Url) -> bool {
    if url.scheme() == "tauri"
        || url.scheme() == "about"
        || url.host_str() == Some("tauri.localhost")
    {
        return true;
    }
    if cfg!(debug_assertions)
        && url.scheme() == "http"
        && url.host_str() == Some("localhost")
        && url.port() == Some(5173)
    {
        return true;
    }
    let Some(server) = webview.app_handle().try_state::<ServerUrl>() else {
        return false;
    };
    let Some(expected) = server.0.lock().ok().and_then(|value| {
        value
            .as_ref()
            .and_then(|value| tauri::Url::parse(value).ok())
    }) else {
        return false;
    };
    url.origin() == expected.origin()
}

/// Poll the server's /healthz until it answers "ok" (or we give up), then navigate
/// the main window there and show it. On timeout, show the startup error instead.
fn finish_startup(handle: &tauri::AppHandle, url: &str) {
    let mut ready = false;
    for _ in 0..100 {
        if server_ready(url) {
            ready = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    if let Some(w) = handle.get_webview_window("main") {
        if ready {
            if let Ok(u) = tauri::Url::parse(url) {
                let _ = w.navigate(u);
            }
        } else {
            let _ = w.eval(STARTUP_ERROR_JS);
        }
        if !ready || !handle.state::<BackgroundLaunch>().0 {
            let _ = w.show();
        }
    }
}

/// Verified readiness probe: a bare TCP connect only proves the port is busy, so we
/// send a real `GET /healthz` and require Carbon's `ok` body before navigating.
fn server_ready(url: &str) -> bool {
    // Parse the endpoint as a URL instead of treating the raw string as a SocketAddr.  `Url`
    // normalizes an origin such as `http://127.0.0.1:2525` to include a trailing `/`; parsing
    // `127.0.0.1:2525/` directly would fail and make every floating-window open look unready.
    let parsed = match tauri::Url::parse(url) {
        Ok(value) => value,
        Err(_) => return false,
    };
    if parsed.scheme() != "http" {
        return false;
    }
    let Some(host) = parsed.host_str() else {
        return false;
    };
    if host != "127.0.0.1" {
        return false;
    }
    let Some(port) = parsed.port() else {
        return false;
    };
    let authority = format!("{host}:{port}");
    let addr: SocketAddr = match authority.parse() {
        Ok(value) => value,
        Err(_) => return false,
    };
    let mut stream = match TcpStream::connect_timeout(&addr, Duration::from_millis(300)) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let _ = stream.set_read_timeout(Some(Duration::from_millis(500)));
    let _ = stream.set_write_timeout(Some(Duration::from_millis(500)));
    // HTTP/1.0 so the server closes the connection after the body (read terminates).
    let req = format!("GET /healthz HTTP/1.0\r\nHost: {authority}\r\n\r\n");
    if stream.write_all(req.as_bytes()).is_err() {
        return false;
    }
    let mut buf = String::new();
    let _ = stream.read_to_string(&mut buf);
    buf.contains(" 200") && buf.trim_end().ends_with("ok")
}

const BACKGROUND_ARG: &str = "--background";

/// Treat only the exact standalone argument as a background launch. This intentionally ignores
/// prefixes such as `--background=true` so an arbitrary command line cannot accidentally hide
/// a user-initiated launch.
fn has_background_arg(args: &[String]) -> bool {
    args.iter().any(|arg| arg == BACKGROUND_ARG)
}

/// Quote one Windows command-line argument using the `CommandLineToArgvW` / CRT backslash
/// rules. We always quote the executable path, even when it currently has no spaces, so a move
/// into a path containing spaces cannot turn the login item into a different command.
fn quote_windows_argument(value: &str) -> String {
    let mut quoted = String::with_capacity(value.len() + 2);
    quoted.push('"');
    let mut backslashes = 0;
    for character in value.chars() {
        if character == '\\' {
            backslashes += 1;
            continue;
        }
        if character == '"' {
            quoted.extend(std::iter::repeat('\\').take(backslashes * 2 + 1));
            quoted.push('"');
        } else {
            quoted.extend(std::iter::repeat('\\').take(backslashes));
            quoted.push(character);
        }
        backslashes = 0;
    }
    // Backslashes before the closing quote must themselves be escaped.
    quoted.extend(std::iter::repeat('\\').take(backslashes * 2));
    quoted.push('"');
    quoted
}

#[cfg(target_os = "windows")]
const AUTOSTART_HELPER_ARG: &str = "--carbon-autostart-helper";
/// Compatibility-only helper flag accepted from a previously registered elevated launcher.
const LEGACY_AUTOSTART_HELPER_ARG: &str = "--cairn-autostart-helper";

#[cfg(target_os = "windows")]
fn is_autostart_helper_arg(argument: &str) -> bool {
    argument == AUTOSTART_HELPER_ARG || argument == LEGACY_AUTOSTART_HELPER_ARG
}

#[cfg(target_os = "windows")]
fn autostart_helper_mode(args: &[String]) -> Option<(AutostartMode, Option<PathBuf>)> {
    match args {
        [flag, mode] if is_autostart_helper_arg(flag) => {
            let mode = AutostartMode::parse(mode).ok()?;
            (mode != AutostartMode::Admin).then_some((mode, None))
        }
        [flag, mode, data_home_flag, data_home]
            if is_autostart_helper_arg(flag) && data_home_flag == ADMIN_DATA_HOME_ARG =>
        {
            let mode = AutostartMode::parse(mode).ok()?;
            (mode == AutostartMode::Admin).then(|| (mode, Some(PathBuf::from(data_home))))
        }
        _ => None,
    }
}

/// Extract the immutable data home passed by the administrator Task Scheduler entry. The task
/// argv is a security contract, not a general application command line: once this internal flag
/// appears, the entire list must be exactly `--background --carbon-admin-data-home <path>`.
#[cfg(target_os = "windows")]
fn locked_admin_data_home_from_args(args: &[String]) -> Result<Option<PathBuf>, String> {
    if !args.iter().any(|argument| argument == ADMIN_DATA_HOME_ARG) {
        return Ok(None);
    }
    match args {
        [background, flag, data_home]
            if background == BACKGROUND_ARG
                && flag == ADMIN_DATA_HOME_ARG
                && !data_home.is_empty() =>
        {
            Ok(Some(PathBuf::from(data_home)))
        }
        _ => Err(
            "Carbon administrator-autostart must use exactly: --background --carbon-admin-data-home <canonical-path>."
                .to_string(),
        ),
    }
}

#[cfg(target_os = "windows")]
mod windows_autostart {
    use std::ffi::{OsStr, OsString};
    use std::io;
    use std::mem::{size_of, zeroed};
    use std::os::windows::ffi::{OsStrExt, OsStringExt};
    use std::os::windows::fs::MetadataExt;
    use std::os::windows::process::CommandExt;
    use std::path::{Component, Path, PathBuf};
    use std::process::Command;
    use std::ptr::{null, null_mut};

    use windows_sys::Win32::Foundation::{CloseHandle, GetLastError, LocalFree, HANDLE};
    use windows_sys::Win32::Security::Authorization::{
        GetEffectiveRightsFromAclW, GetNamedSecurityInfoW, NO_MULTIPLE_TRUSTEE, SE_FILE_OBJECT,
        TRUSTEE_IS_SID, TRUSTEE_IS_UNKNOWN, TRUSTEE_W,
    };
    use windows_sys::Win32::Security::{
        EqualSid, GetLengthSid, GetTokenInformation, TokenElevation, TokenGroups, TokenOwner,
        TokenUser, DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION, PSID, SID_AND_ATTRIBUTES,
        TOKEN_ELEVATION, TOKEN_GROUPS, TOKEN_OWNER, TOKEN_QUERY, TOKEN_USER,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        DELETE, FILE_APPEND_DATA, FILE_DELETE_CHILD, FILE_GENERIC_WRITE, FILE_WRITE_ATTRIBUTES,
        FILE_WRITE_DATA, FILE_WRITE_EA, WRITE_DAC, WRITE_OWNER,
    };
    use windows_sys::Win32::System::SystemInformation::GetSystemDirectoryW;
    use windows_sys::Win32::System::Threading::{
        CreateMutexW, GetCurrentProcess, GetExitCodeProcess, OpenProcessToken, ReleaseMutex,
        WaitForSingleObject, CREATE_NO_WINDOW, INFINITE,
    };
    use windows_sys::Win32::UI::Shell::{
        CommandLineToArgvW, ShellExecuteExW, SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::SW_HIDE;
    use winreg::enums::{HKEY_CURRENT_USER, KEY_READ};
    use winreg::RegKey;

    use super::{
        data_home_paths_match, is_safe_local_path, quote_windows_argument,
        resolve_existing_data_home, AutostartMode, ADMIN_DATA_HOME_ARG, AUTOSTART_HELPER_ARG,
        BACKGROUND_ARG, LEGACY_PORTABLE_MARKER, PORTABLE_MARKER,
    };

    const RUN_KEY: &str = r"Software\Microsoft\Windows\CurrentVersion\Run";
    // The fixed value name is our ownership boundary. We never enumerate, edit, or delete
    // neighboring Run values.
    const RUN_VALUE: &str = "CarbonPortableAutostart-v1";
    const ADMIN_TASK_NAME: &str = "CarbonPortableAdminAutostart-v1";
    const AUTOSTART_MUTEX: &str = r"Local\CarbonPortableAutostart-v1";
    /// Migration-only registry value written by the pre-Carbon desktop app.
    const LEGACY_RUN_VALUE: &str = "CairnPortableAutostart-v1";
    /// Migration-only scheduler task written by the pre-Carbon desktop app.
    const LEGACY_ADMIN_TASK_NAME: &str = "CairnPortableAdminAutostart-v1";
    const WAIT_OBJECT_0: u32 = 0;
    const WAIT_ABANDONED: u32 = 0x0000_0080;
    const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x0000_0400;
    const HELPER_EXIT_UNSAFE_LOCATION: u32 = 2;
    const HRESULT_FILE_NOT_FOUND: i32 = 0x8007_0002_u32 as i32;
    // `SE_GROUP_*` live in a windows-sys feature we do not otherwise need. Keep the documented
    // values local rather than broadening the desktop binary's Windows API surface.
    const SE_GROUP_ENABLED: u32 = 0x0000_0004;
    const SE_GROUP_USE_FOR_DENY_ONLY: u32 = 0x0000_0010;

    // In addition to ordinary file content writes, these rights let a non-elevated account
    // replace an executable (delete/rename it) or rewrite its ACL. Any one is disqualifying.
    const WRITE_RISK_MASK: u32 = 0x1000_0000 // GENERIC_ALL
        | 0x4000_0000 // GENERIC_WRITE
        | FILE_GENERIC_WRITE
        | FILE_WRITE_DATA
        | FILE_APPEND_DATA
        | FILE_WRITE_EA
        | FILE_WRITE_ATTRIBUTES
        | FILE_DELETE_CHILD
        | DELETE
        | WRITE_DAC
        | WRITE_OWNER;

    // An ancestor need not permit ordinary file creation to be dangerous, but it must not let a
    // low-privilege account delete/rename the protected Carbon directory or rewrite its ACL.
    const PARENT_REPLACE_RISK_MASK: u32 = 0x1000_0000 // GENERIC_ALL
        | 0x4000_0000 // GENERIC_WRITE
        | FILE_DELETE_CHILD
        | DELETE
        | WRITE_DAC
        | WRITE_OWNER;

    #[derive(Debug)]
    enum TaskState {
        Missing,
        /// A current Carbon task with an immutable, structurally valid data-home argument.
        Ours(PathBuf),
        /// A task created before Carbon locked elevated data homes. It is recognized only so a
        /// user can explicitly disable or recreate it; it is never treated as safe to launch.
        LegacyOurs,
        Conflict,
    }

    #[derive(Clone, Debug, Eq, PartialEq)]
    pub(super) struct TokenSubject {
        pub(super) label: String,
        pub(super) sid: Vec<u8>,
    }

    pub fn get_mode() -> Result<AutostartMode, String> {
        match admin_task_state()? {
            TaskState::Ours(data_home) => {
                ensure_admin_data_home_is_safe(&data_home)?;
                Ok(AutostartMode::Admin)
            }
            TaskState::LegacyOurs => Err(
                "Carbon found a legacy administrator-autostart task without a locked data home. It will not be used for elevated startup. Reconfigure administrator autostart, use normal user startup, or choose a protected data home."
                    .to_string(),
            ),
            TaskState::Conflict => Err(format!(
                "管理员自启动任务名“{ADMIN_TASK_NAME}”已被其他配置占用；为避免修改非 Carbon 任务，已拒绝继续操作。"
            )),
            TaskState::Missing => match legacy_admin_task_state()? {
                TaskState::Ours(data_home) => {
                    ensure_admin_data_home_is_safe(&data_home)?;
                    Ok(AutostartMode::Admin)
                }
                TaskState::LegacyOurs => Err(
                    "Carbon found a legacy administrator-autostart task without a locked data home. Reconfigure administrator autostart, use normal user startup, or choose a protected data home."
                        .to_string(),
                ),
                TaskState::Conflict => Err(
                    "Carbon found a legacy administrator-autostart task name owned by another configuration. It will not be modified automatically."
                        .to_string(),
                ),
                TaskState::Missing => {
                    if user_run_value()?.is_some() {
                        Ok(AutostartMode::User)
                    } else if legacy_user_run_value()?.is_some() {
                        migrate_legacy_user_run_value()?;
                        Ok(AutostartMode::User)
                    } else {
                        Ok(AutostartMode::Off)
                    }
                }
            },
        }
    }

    pub fn set_mode(mode: AutostartMode, active_data_home: &Path) -> Result<AutostartMode, String> {
        let legacy_task_state = legacy_admin_task_state()?;
        match mode {
            AutostartMode::Admin => {
                if matches!(&legacy_task_state, TaskState::Conflict) {
                    return Err(
                        "Carbon found a legacy administrator-autostart task name owned by another configuration. It will not be modified automatically."
                            .to_string(),
                    );
                }
                // Give the UI a specific, non-UAC error before asking for elevation. The helper
                // performs the same check again to close the time-of-check/time-of-use window.
                ensure_admin_location_is_safe()?;
                ensure_admin_data_home_is_safe(active_data_home)?;
                run_elevated_helper(mode, Some(active_data_home))?;
            }
            AutostartMode::User | AutostartMode::Off => match (admin_task_state()?, legacy_task_state) {
                (TaskState::Ours(_), _)
                | (TaskState::LegacyOurs, _)
                | (_, TaskState::Ours(_))
                | (_, TaskState::LegacyOurs) => {
                    // The current executable becomes the elevated helper. Re-check that it and
                    // its directory are still protected before displaying UAC, even when the
                    // requested result is a downgrade or disable operation. A previously safe
                    // admin-task directory may have had its ACL loosened since registration.
                    ensure_admin_location_is_safe()?;
                    run_elevated_helper(mode, None)?;
                }
                (TaskState::Conflict, _) | (_, TaskState::Conflict) => {
                    return Err(format!(
                        "管理员自启动任务名“{ADMIN_TASK_NAME}”已被其他配置占用；为避免同时存在两个自启动项，已拒绝修改。"
                    ))
                }
                (TaskState::Missing, TaskState::Missing) => {
                    with_autostart_lock(|| apply_mode_locked(mode, None))?
                }
            },
        }
        get_mode()
    }

    /// Invoked only by the exact `--carbon-autostart-helper <mode>` child launched through the
    /// Windows UAC broker. It deliberately does not create a Tauri window or sidecar.
    pub fn run_elevated_helper_entry(mode: AutostartMode, data_home: Option<&Path>) -> i32 {
        // Repeat the location check inside the elevated process to close the gap between the
        // non-elevated preflight and task mutation. This applies to every mode because all helper
        // modes execute code from the current Carbon binary with administrator privileges.
        let location_check = match mode {
            AutostartMode::Admin => data_home
                .ok_or_else(|| {
                    unsafe_error("Administrator autostart requires a locked data home.".to_string())
                })
                .and_then(|data_home| {
                    ensure_admin_location_is_safe()?;
                    ensure_admin_data_home_is_safe(data_home)
                }),
            AutostartMode::User | AutostartMode::Off => ensure_admin_location_is_safe(),
        };
        if let Err(error) = location_check {
            eprintln!("Carbon autostart helper: {error}");
            return if error.starts_with("UNSAFE_LOCATION:") {
                HELPER_EXIT_UNSAFE_LOCATION as i32
            } else {
                1
            };
        }
        match with_autostart_lock(|| apply_mode_locked(mode, data_home)) {
            Ok(()) => 0,
            Err(error) => {
                eprintln!("Carbon autostart helper: {error}");
                if error.starts_with("UNSAFE_LOCATION:") {
                    HELPER_EXIT_UNSAFE_LOCATION as i32
                } else {
                    1
                }
            }
        }
    }

    fn apply_mode_locked(mode: AutostartMode, data_home: Option<&Path>) -> Result<(), String> {
        match mode {
            AutostartMode::Admin => {
                ensure_admin_location_is_safe()?;
                let data_home = data_home.ok_or_else(|| {
                    unsafe_error("Administrator autostart requires a locked data home.".to_string())
                })?;
                ensure_admin_data_home_is_safe(data_home)?;
                // Remove the non-privileged entry first. A failed task creation can leave no
                // login item, but it can never leave both a normal and an elevated launch.
                remove_user_run_value()?;
                remove_legacy_user_run_value()?;
                delete_legacy_admin_task()?;
                create_or_update_admin_task(data_home)
            }
            AutostartMode::User => {
                delete_own_admin_task()?;
                delete_legacy_admin_task()?;
                remove_legacy_user_run_value()?;
                write_user_run_value()
            }
            AutostartMode::Off => {
                delete_own_admin_task()?;
                delete_legacy_admin_task()?;
                remove_user_run_value()?;
                remove_legacy_user_run_value()
            }
        }
    }

    fn with_autostart_lock<T>(action: impl FnOnce() -> Result<T, String>) -> Result<T, String> {
        let name = wide(OsStr::new(AUTOSTART_MUTEX));
        let handle = unsafe { CreateMutexW(null(), 0, name.as_ptr()) };
        if handle.is_null() {
            return Err(format!(
                "无法锁定自启动设置（Windows 错误 {}）。",
                unsafe { GetLastError() }
            ));
        }
        let wait = unsafe { WaitForSingleObject(handle, 30_000) };
        if wait != WAIT_OBJECT_0 && wait != WAIT_ABANDONED {
            unsafe {
                CloseHandle(handle);
            }
            return Err("另一个 Carbon 自启动操作仍在进行，请稍后再试。".to_string());
        }
        let result = action();
        unsafe {
            ReleaseMutex(handle);
            CloseHandle(handle);
        }
        result
    }

    fn user_run_value() -> Result<Option<String>, String> {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let run = match hkcu.open_subkey_with_flags(RUN_KEY, KEY_READ) {
            Ok(run) => run,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(error) => return Err(format!("无法读取当前用户的启动项：{error}")),
        };
        match run.get_value::<String, _>(RUN_VALUE) {
            Ok(value) => Ok(Some(value)),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(error) => Err(format!("无法读取 Carbon 启动项：{error}")),
        }
    }

    fn legacy_user_run_value() -> Result<Option<String>, String> {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let run = match hkcu.open_subkey_with_flags(RUN_KEY, KEY_READ) {
            Ok(run) => run,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(error) => {
                return Err(format!(
                    "Could not read legacy Carbon startup entry: {error}"
                ))
            }
        };
        match run.get_value::<String, _>(LEGACY_RUN_VALUE) {
            Ok(value) => Ok(Some(value)),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(error) => Err(format!(
                "Could not read legacy Carbon startup entry: {error}"
            )),
        }
    }

    /// Re-emit an old HKCU Run entry with the canonical value name and current executable path.
    /// The historical value is deleted only after the new value has been written successfully.
    fn migrate_legacy_user_run_value() -> Result<(), String> {
        if legacy_user_run_value()?.is_none() {
            return Ok(());
        }
        write_user_run_value()?;
        remove_legacy_user_run_value()
    }

    fn write_user_run_value() -> Result<(), String> {
        let command = login_command()?;
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let (run, _) = hkcu
            .create_subkey(RUN_KEY)
            .map_err(|error| format!("无法创建当前用户的启动项：{error}"))?;
        run.set_value(RUN_VALUE, &command)
            .map_err(|error| format!("无法写入 Carbon 启动项：{error}"))
    }

    fn remove_user_run_value() -> Result<(), String> {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let run = match hkcu.open_subkey_with_flags(RUN_KEY, KEY_READ | 0x0002_0006) {
            Ok(run) => run,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(format!("无法访问当前用户的启动项：{error}")),
        };
        match run.delete_value(RUN_VALUE) {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(format!("无法移除 Carbon 启动项：{error}")),
        }
    }

    fn remove_legacy_user_run_value() -> Result<(), String> {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let run = match hkcu.open_subkey_with_flags(RUN_KEY, KEY_READ | 0x0002_0006) {
            Ok(run) => run,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) => {
                return Err(format!(
                    "Could not access legacy Carbon startup entry: {error}"
                ))
            }
        };
        match run.delete_value(LEGACY_RUN_VALUE) {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(format!(
                "Could not remove legacy Carbon startup entry: {error}"
            )),
        }
    }

    fn login_command() -> Result<String, String> {
        let executable = std::env::current_exe()
            .map_err(|error| format!("无法定位 Carbon 可执行文件：{error}"))?;
        Ok(format!(
            "{} {BACKGROUND_ARG}",
            quote_windows_argument(&executable.to_string_lossy())
        ))
    }

    fn admin_login_command(data_home: &Path) -> Result<String, String> {
        let executable = std::env::current_exe()
            .map_err(|error| format!("Could not locate Carbon executable: {error}"))?;
        let data_home = data_home
            .to_str()
            .ok_or_else(|| "Carbon administrator data home is not valid Unicode.".to_string())?;
        Ok(format!(
            "{} {BACKGROUND_ARG} {ADMIN_DATA_HOME_ARG} {}",
            quote_windows_argument(&executable.to_string_lossy()),
            quote_windows_argument(data_home)
        ))
    }

    fn admin_task_state() -> Result<TaskState, String> {
        task_state(ADMIN_TASK_NAME)
    }

    fn legacy_admin_task_state() -> Result<TaskState, String> {
        task_state(LEGACY_ADMIN_TASK_NAME)
    }

    fn task_state(task_name: &str) -> Result<TaskState, String> {
        let output = scheduler_command()?
            .args(["/Query", "/TN", task_name, "/XML", "/HRESULT"])
            .output()
            .map_err(|error| format!("无法查询 Windows 任务计划：{error}"))?;
        if !output.status.success() {
            // `/HRESULT` makes a missing task distinguishable from access denied, a stopped
            // scheduler service, and other failures without parsing localized stderr. Fail closed
            // for every result except HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND).
            if task_query_code_is_missing(output.status.code()) {
                return Ok(TaskState::Missing);
            }
            return Err(format!(
                "无法可靠查询 Carbon 管理员自启动任务（退出码 {:?}）：{}",
                output.status.code(),
                decode_windows_output(&output.stderr).trim()
            ));
        }
        let xml = decode_windows_output(&output.stdout);
        let executable = std::env::current_exe()
            .map_err(|error| format!("Could not locate Carbon executable: {error}"))?;
        match carbon_task_data_home(&xml, &executable)? {
            Some(data_home) => Ok(TaskState::Ours(data_home)),
            None if is_legacy_carbon_task_xml(&xml, &executable)? => Ok(TaskState::LegacyOurs),
            None => Ok(TaskState::Conflict),
        }
    }

    pub(super) fn task_query_code_is_missing(code: Option<i32>) -> bool {
        code == Some(HRESULT_FILE_NOT_FOUND)
    }

    fn create_or_update_admin_task(data_home: &Path) -> Result<(), String> {
        match admin_task_state()? {
            TaskState::Missing => {}
            TaskState::Ours(_) | TaskState::LegacyOurs => delete_own_admin_task()?,
            TaskState::Conflict => {
                return Err(format!(
                    "管理员自启动任务名“{ADMIN_TASK_NAME}”已被其他配置占用，无法安全覆盖。"
                ))
            }
        }

        let command = admin_login_command(data_home)?;
        let output = scheduler_command()?
            .args([
                "/Create",
                "/TN",
                ADMIN_TASK_NAME,
                "/TR",
                &command,
                "/SC",
                "ONLOGON",
                "/RL",
                "HIGHEST",
                "/IT",
            ])
            .output()
            .map_err(|error| format!("无法创建管理员自启动任务：{error}"))?;
        if !output.status.success() {
            return Err(format!(
                "Windows 任务计划拒绝了管理员自启动：{}",
                decode_windows_output(&output.stderr).trim()
            ));
        }
        match admin_task_state()? {
            TaskState::Ours(actual_data_home)
                if data_home_paths_match(&actual_data_home, data_home) =>
            {
                Ok(())
            }
            TaskState::Ours(_) => Err(
                "The administrator autostart task was created with a different data home."
                    .to_string(),
            ),
            TaskState::LegacyOurs => Err(
                "The administrator autostart task was created without its locked data home."
                    .to_string(),
            ),
            TaskState::Missing => Err("管理员自启动任务创建后未找到。".to_string()),
            TaskState::Conflict => Err("管理员自启动任务创建后未通过完整性校验。".to_string()),
        }
    }

    fn delete_own_admin_task() -> Result<(), String> {
        match admin_task_state()? {
            TaskState::Missing => return Ok(()),
            TaskState::Conflict => {
                return Err(format!(
                    "管理员自启动任务名“{ADMIN_TASK_NAME}”并非 Carbon 创建；不会删除它。"
                ))
            }
            TaskState::Ours(_) | TaskState::LegacyOurs => {}
        }
        let output = scheduler_command()?
            .args(["/Delete", "/TN", ADMIN_TASK_NAME, "/F"])
            .output()
            .map_err(|error| format!("无法移除管理员自启动任务：{error}"))?;
        if output.status.success() {
            Ok(())
        } else {
            Err(format!(
                "Windows 任务计划拒绝移除 Carbon 管理员启动项：{}",
                decode_windows_output(&output.stderr).trim()
            ))
        }
    }

    fn delete_legacy_admin_task() -> Result<(), String> {
        match legacy_admin_task_state()? {
            TaskState::Missing => return Ok(()),
            TaskState::Conflict => {
                return Err(
                    "Carbon found a legacy administrator-autostart task name owned by another configuration. It will not be deleted."
                        .to_string(),
                )
            }
            TaskState::Ours(_) | TaskState::LegacyOurs => {}
        }
        let output = scheduler_command()?
            .args(["/Delete", "/TN", LEGACY_ADMIN_TASK_NAME, "/F"])
            .output()
            .map_err(|error| {
                format!("Could not remove legacy Carbon administrator startup entry: {error}")
            })?;
        if output.status.success() {
            Ok(())
        } else {
            Err(format!(
                "Windows Task Scheduler refused to remove the legacy Carbon administrator startup entry: {}",
                decode_windows_output(&output.stderr).trim()
            ))
        }
    }

    /// Return the data home from a current protected Carbon task, but only when every identity
    /// field matches exactly. A task name is never sufficient ownership proof.
    fn carbon_task_data_home(
        xml: &str,
        expected_executable: &Path,
    ) -> Result<Option<PathBuf>, String> {
        let Some(command) = xml_tag(xml, "Command") else {
            return Ok(None);
        };
        let arguments = xml_tag(xml, "Arguments").unwrap_or_default();
        let expected_command = expected_executable
            .to_str()
            .ok_or_else(|| "Carbon executable path is not valid Unicode.".to_string())?;

        if !task_has_expected_identity(xml, command.as_str(), expected_command) {
            return Ok(None);
        }
        let Some(data_home) = locked_data_home_from_task_arguments(&arguments)? else {
            return Ok(None);
        };
        if !is_safe_local_path(&data_home) {
            return Ok(None);
        }
        Ok(Some(data_home))
    }

    fn is_legacy_carbon_task_xml(xml: &str, expected_executable: &Path) -> Result<bool, String> {
        let Some(command) = xml_tag(xml, "Command") else {
            return Ok(false);
        };
        let expected_command = expected_executable
            .to_str()
            .ok_or_else(|| "Carbon executable path is not valid Unicode.".to_string())?;
        let arguments = xml_tag(xml, "Arguments").unwrap_or_default();
        Ok(
            task_has_expected_identity(xml, command.as_str(), expected_command)
                && arguments.trim() == BACKGROUND_ARG,
        )
    }

    fn task_has_expected_identity(xml: &str, command: &str, expected_command: &str) -> bool {
        const REQUIRED_SINGLETON_TAGS: &[&str] = &[
            "Task",
            "Triggers",
            "LogonTrigger",
            "Principals",
            "Principal",
            "RunLevel",
            "Actions",
            "Exec",
            "Command",
            "Arguments",
        ];

        command.trim().eq_ignore_ascii_case(expected_command)
            && REQUIRED_SINGLETON_TAGS
                .iter()
                .all(|tag| xml_has_exactly_one_element(xml, tag))
            && xml_has_only_logon_trigger(xml)
            && xml_tag(xml, "RunLevel").as_deref() == Some("HighestAvailable")
    }

    #[cfg(test)]
    pub(super) fn task_xml_matches(
        xml: &str,
        expected_executable: &Path,
        expected_data_home: &Path,
    ) -> Result<bool, String> {
        Ok(carbon_task_data_home(xml, expected_executable)?
            .is_some_and(|data_home| data_home_paths_match(&data_home, expected_data_home)))
    }

    fn locked_data_home_from_task_arguments(arguments: &str) -> Result<Option<PathBuf>, String> {
        // Add a controlled argv[0] so CommandLineToArgvW parses the task's Arguments field with
        // the same Windows quoting rules used by Task Scheduler.
        let mut command_line = String::from("carbon.exe ");
        command_line.push_str(arguments);
        let command_line = wide(OsStr::new(&command_line));
        let mut argc = 0;
        let argv = unsafe { CommandLineToArgvW(command_line.as_ptr(), &mut argc) };
        if argv.is_null() || argc < 1 {
            return Err(
                "Could not parse Carbon administrator-autostart task arguments.".to_string(),
            );
        }

        let parsed = (|| {
            let mut values = Vec::with_capacity(argc as usize);
            for index in 0..argc as usize {
                let value = unsafe { *argv.add(index) };
                if value.is_null() {
                    return Err(
                        "Could not parse Carbon administrator-autostart task arguments."
                            .to_string(),
                    );
                }
                let mut length = 0;
                while unsafe { *value.add(length) } != 0 {
                    length += 1;
                }
                values.push(OsString::from_wide(unsafe {
                    std::slice::from_raw_parts(value, length)
                }));
            }
            Ok::<_, String>(values)
        })();
        unsafe {
            LocalFree(argv.cast());
        }
        let values = parsed?;
        let values = values.iter().skip(1).collect::<Vec<_>>();
        match values.as_slice() {
            [background, flag, data_home]
                if background.as_os_str() == OsStr::new(BACKGROUND_ARG)
                    && flag.as_os_str() == OsStr::new(ADMIN_DATA_HOME_ARG) =>
            {
                let data_home = PathBuf::from(data_home);
                if data_home.as_os_str().is_empty() {
                    Err("Carbon administrator-autostart task has an empty data home.".to_string())
                } else {
                    Ok(Some(data_home))
                }
            }
            _ => Ok(None),
        }
    }

    fn xml_tag(xml: &str, tag: &str) -> Option<String> {
        if !xml_has_exactly_one_element(xml, tag) {
            return None;
        }
        let open = format!("<{tag}>");
        let close = format!("</{tag}>");
        let start = xml.find(&open)? + open.len();
        let end = xml[start..].find(&close)? + start;
        Some(xml_unescape(&xml[start..end]))
    }

    fn xml_has_exactly_one_element(xml: &str, tag: &str) -> bool {
        xml_open_tag_count(xml, tag) == 1 && xml.match_indices(&format!("</{tag}>")).count() == 1
    }

    fn xml_open_tag_count(xml: &str, tag: &str) -> usize {
        let needle = format!("<{tag}");
        let mut rest = xml;
        let mut count = 0;
        while let Some(offset) = rest.find(&needle) {
            let after = &rest[offset + needle.len()..];
            let valid_tag_boundary = match after.chars().next() {
                Some('>') | Some('/') => true,
                Some(character) => character.is_ascii_whitespace(),
                None => false,
            };
            if valid_tag_boundary {
                count += 1;
            }
            rest = &after[after.chars().next().map_or(0, char::len_utf8)..];
        }
        count
    }

    /// A task with a second trigger type is not Carbon's one-logon-task contract, even if it
    /// still happens to contain exactly one `<LogonTrigger>`.  This small scanner is deliberately
    /// fail-closed: comments/CDATA containing XML-looking trigger tags are rejected rather than
    /// being interpreted as trustworthy scheduler XML.
    fn xml_has_only_logon_trigger(xml: &str) -> bool {
        let mut rest = xml;
        let mut trigger_count = 0;
        while let Some(offset) = rest.find('<') {
            rest = &rest[offset + 1..];
            let name_length = rest
                .chars()
                .take_while(|character| {
                    character.is_ascii_alphanumeric() || matches!(*character, ':' | '_' | '-' | '.')
                })
                .map(char::len_utf8)
                .sum::<usize>();
            if name_length == 0 {
                continue;
            }
            let name = &rest[..name_length];
            if name.ends_with("Trigger") {
                if name != "LogonTrigger" {
                    return false;
                }
                trigger_count += 1;
            }
        }
        trigger_count == 1
    }

    fn xml_unescape(value: &str) -> String {
        value
            .replace("&quot;", "\"")
            .replace("&apos;", "'")
            .replace("&lt;", "<")
            .replace("&gt;", ">")
            .replace("&amp;", "&")
    }

    fn decode_windows_output(bytes: &[u8]) -> String {
        if bytes.len() >= 2 && (bytes.starts_with(&[0xff, 0xfe]) || bytes[1] == 0) {
            let code_units = bytes
                .chunks_exact(2)
                .map(|pair| u16::from_le_bytes([pair[0], pair[1]]));
            String::from_utf16_lossy(&code_units.collect::<Vec<_>>())
                .trim_start_matches('\u{feff}')
                .to_string()
        } else {
            String::from_utf8_lossy(bytes).into_owned()
        }
    }

    fn scheduler_command() -> Result<Command, String> {
        let mut buffer = vec![0u16; 32_768];
        let count = unsafe { GetSystemDirectoryW(buffer.as_mut_ptr(), buffer.len() as u32) };
        if count == 0 || count as usize >= buffer.len() {
            return Err("无法解析 Windows System32 目录，拒绝调用任务计划程序。".to_string());
        }
        let directory = PathBuf::from(OsString::from_wide(&buffer[..count as usize]));
        let scheduler = directory.join("schtasks.exe");
        if !scheduler.is_file() {
            return Err("Windows 任务计划程序 schtasks.exe 不存在。".to_string());
        }
        // `schtasks.exe` is consulted whenever Settings reads the login-start mode. Without
        // this creation flag, Windows can briefly show a console window for that background
        // query even though Carbon is a GUI app. This only affects the scheduler child; the
        // administrator helper still uses ShellExecuteExW with `runas`, so its UAC flow remains
        // explicit and unchanged.
        let mut command = Command::new(scheduler);
        command.creation_flags(CREATE_NO_WINDOW);
        Ok(command)
    }

    fn run_elevated_helper(mode: AutostartMode, data_home: Option<&Path>) -> Result<(), String> {
        let executable = std::env::current_exe()
            .map_err(|error| format!("无法定位 Carbon 可执行文件：{error}"))?;
        let file = wide(executable.as_os_str());
        let directory = wide(
            executable
                .parent()
                .ok_or_else(|| "Carbon 可执行文件没有父目录。".to_string())?
                .as_os_str(),
        );
        let verb = wide(OsStr::new("runas"));
        let mut parameters = format!("{AUTOSTART_HELPER_ARG} {}", mode.as_str());
        if let Some(data_home) = data_home {
            let data_home = data_home.to_str().ok_or_else(|| {
                "Carbon administrator data home is not valid Unicode.".to_string()
            })?;
            parameters.push(' ');
            parameters.push_str(ADMIN_DATA_HOME_ARG);
            parameters.push(' ');
            parameters.push_str(&quote_windows_argument(data_home));
        }
        let parameters = wide(OsStr::new(&parameters));

        let mut execute: SHELLEXECUTEINFOW = unsafe { zeroed() };
        execute.cbSize = size_of::<SHELLEXECUTEINFOW>() as u32;
        execute.fMask = SEE_MASK_NOCLOSEPROCESS;
        execute.lpVerb = verb.as_ptr();
        execute.lpFile = file.as_ptr();
        execute.lpParameters = parameters.as_ptr();
        execute.lpDirectory = directory.as_ptr();
        execute.nShow = SW_HIDE;

        if unsafe { ShellExecuteExW(&mut execute) } == 0 {
            let error = unsafe { GetLastError() };
            return Err(if error == 1223 {
                "已取消管理员自启动的 UAC 授权。".to_string()
            } else {
                format!("无法请求管理员自启动权限（Windows 错误 {error}）。")
            });
        }
        if execute.hProcess.is_null() {
            return Err("管理员自启动帮助进程没有返回可等待的句柄。".to_string());
        }
        let wait = unsafe { WaitForSingleObject(execute.hProcess, INFINITE) };
        if wait != WAIT_OBJECT_0 {
            unsafe {
                CloseHandle(execute.hProcess);
            }
            return Err("等待管理员自启动帮助进程时失败。".to_string());
        }
        let mut exit_code = 1;
        let got_code = unsafe { GetExitCodeProcess(execute.hProcess, &mut exit_code) } != 0;
        unsafe {
            CloseHandle(execute.hProcess);
        }
        if !got_code {
            return Err("无法获取管理员自启动帮助进程的结果。".to_string());
        }
        match exit_code {
            0 => Ok(()),
            HELPER_EXIT_UNSAFE_LOCATION => Err(
                "管理员自启动被安全检查拒绝：Carbon 程序目录或活动数据主路径可被当前非提升用户/通用用户组修改，或包含重解析点。请改用普通用户自启动，或将程序和主路径移到受保护目录后再试。"
                    .to_string(),
            ),
            code => Err(format!("管理员自启动帮助进程失败（退出码 {code}）。")),
        }
    }

    pub(super) fn ensure_admin_data_home_is_safe(data_home: &Path) -> Result<(), String> {
        ensure_admin_data_home_acl_is_safe(data_home).map_err(|error| {
            if error.starts_with("UNSAFE_LOCATION:") {
                format!(
                    "{error} 管理员自启动只能使用受保护的活动数据主路径；请改用普通用户自启动，或选择受保护主路径后再试。"
                )
            } else {
                error
            }
        })
    }

    /// The elevated sidecar can write and interpret all files under its data home. Treat that
    /// directory like the executable itself: its root must not be user-writable and no ancestor
    /// may let a non-elevated account replace it or rewrite its ACL.
    fn ensure_admin_data_home_acl_is_safe(data_home: &Path) -> Result<(), String> {
        let data_home = resolve_existing_data_home(data_home).map_err(unsafe_error)?;
        let token_subjects = current_token_subjects()?;

        let mut current = data_home.as_path();
        let mut is_data_home = true;
        loop {
            if is_reparse_point(current)? {
                return Err(unsafe_error(format!(
                    "Administrator data home contains a reparse-point component: {}",
                    current.display()
                )));
            }
            ensure_path_acl_is_safe(
                current,
                &token_subjects,
                if is_data_home {
                    WRITE_RISK_MASK
                } else {
                    PARENT_REPLACE_RISK_MASK
                },
            )?;
            if is_windows_volume_root(current) {
                break;
            }
            let parent = current.parent().ok_or_else(|| {
                unsafe_error(format!(
                    "Could not determine a protected ancestor for administrator data home {}.",
                    data_home.display()
                ))
            })?;
            if parent == current {
                return Err(unsafe_error(format!(
                    "Could not determine a protected ancestor for administrator data home {}.",
                    data_home.display()
                )));
            }
            current = parent;
            is_data_home = false;
        }
        Ok(())
    }

    fn is_windows_volume_root(path: &Path) -> bool {
        let mut components = path.components();
        matches!(
            components.next(),
            Some(Component::Prefix(prefix))
                if matches!(
                    prefix.kind(),
                    std::path::Prefix::Disk(_) | std::path::Prefix::VerbatimDisk(_)
                )
        ) && matches!(components.next(), Some(Component::RootDir))
            && components.next().is_none()
    }

    pub(super) fn is_current_process_elevated() -> Result<bool, String> {
        let mut token: HANDLE = null_mut();
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(unsafe_error(format!(
                "Could not inspect Carbon's process token (Windows error {}).",
                unsafe { GetLastError() }
            )));
        }
        let mut elevation = TOKEN_ELEVATION { TokenIsElevated: 0 };
        let mut length = 0;
        let success = unsafe {
            GetTokenInformation(
                token,
                TokenElevation,
                (&mut elevation as *mut TOKEN_ELEVATION).cast(),
                size_of::<TOKEN_ELEVATION>() as u32,
                &mut length,
            )
        };
        unsafe {
            CloseHandle(token);
        }
        if success == 0 {
            return Err(unsafe_error(format!(
                "Could not determine whether Carbon is elevated (Windows error {}).",
                unsafe { GetLastError() }
            )));
        }
        Ok(elevation.TokenIsElevated != 0)
    }

    fn ensure_admin_location_is_safe() -> Result<(), String> {
        let executable = std::env::current_exe()
            .map_err(|error| unsafe_error(format!("无法定位 Carbon 可执行文件：{error}")))?;
        let app_directory = executable
            .parent()
            .ok_or_else(|| unsafe_error("Carbon 可执行文件没有父目录。".to_string()))?
            .to_path_buf();
        let sidecar = app_directory.join("carbon.exe");
        let marker = app_directory.join(PORTABLE_MARKER);
        let legacy_marker = app_directory.join(LEGACY_PORTABLE_MARKER);
        for path in [&app_directory, &executable, &sidecar] {
            if !path.exists() {
                return Err(unsafe_error(format!(
                    "缺少必须保护的文件：{}",
                    path.display()
                )));
            }
            if is_reparse_point(path)? {
                return Err(unsafe_error(format!(
                    "拒绝通过重解析点启动管理员模式：{}",
                    path.display()
                )));
            }
        }
        if marker.exists() && is_reparse_point(&marker)? {
            return Err(unsafe_error(format!(
                "拒绝通过重解析点读取便携标记：{}",
                marker.display()
            )));
        }

        if legacy_marker.exists() && is_reparse_point(&legacy_marker)? {
            return Err(unsafe_error(format!(
                "Refusing to read a legacy portable marker through a reparse point: {}",
                legacy_marker.display()
            )));
        }

        let token_subjects = current_token_subjects()?;
        for path in [&app_directory, &executable, &sidecar] {
            ensure_path_acl_is_safe(path, &token_subjects, WRITE_RISK_MASK)?;
        }
        if marker.exists() {
            ensure_path_acl_is_safe(&marker, &token_subjects, WRITE_RISK_MASK)?;
        }
        if legacy_marker.exists() {
            ensure_path_acl_is_safe(&legacy_marker, &token_subjects, WRITE_RISK_MASK)?;
        }
        // Protecting only the app directory is not enough: a low-privilege account that can
        // delete one of its ancestors can replace the whole portable folder after the task is
        // registered. Check ancestors with a narrower replacement-focused mask so standard
        // root-level "create new files" permissions do not cause a false unsafe result.
        let mut ancestor = app_directory.parent();
        while let Some(path) = ancestor {
            if is_reparse_point(path)? {
                return Err(unsafe_error(format!(
                    "拒绝通过重解析点解析管理员自启动父目录：{}",
                    path.display()
                )));
            }
            ensure_path_acl_is_safe(path, &token_subjects, PARENT_REPLACE_RISK_MASK)?;
            ancestor = path.parent();
        }
        Ok(())
    }

    fn unsafe_error(message: String) -> String {
        format!("UNSAFE_LOCATION: {message}")
    }

    fn is_reparse_point(path: &Path) -> Result<bool, String> {
        let metadata = std::fs::symlink_metadata(path).map_err(|error| {
            unsafe_error(format!("无法读取 {} 的文件属性：{error}", path.display()))
        })?;
        Ok(metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0)
    }

    fn ensure_path_acl_is_safe(
        path: &Path,
        token_subjects: &[TokenSubject],
        risk_mask: u32,
    ) -> Result<(), String> {
        if token_subjects.is_empty() {
            return Err(unsafe_error(
                "Carbon could not determine any enabled token security subjects.".to_string(),
            ));
        }
        let path_wide = wide(path.as_os_str());
        let mut owner: PSID = null_mut();
        let mut dacl = null_mut();
        let mut descriptor = null_mut();
        let status = unsafe {
            GetNamedSecurityInfoW(
                path_wide.as_ptr(),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
                &mut owner,
                null_mut(),
                &mut dacl,
                null_mut(),
                &mut descriptor,
            )
        };
        if status != 0 || descriptor.is_null() || dacl.is_null() {
            if !descriptor.is_null() {
                unsafe {
                    LocalFree(descriptor);
                }
            }
            return Err(unsafe_error(format!(
                "无法安全读取 {} 的访问控制列表（Windows 错误 {status}）。",
                path.display()
            )));
        }

        let result = (|| {
            if !owner.is_null() {
                if let Some(subject) = token_subjects
                    .iter()
                    .find(|subject| (unsafe { EqualSid(owner, subject.sid.as_ptr() as PSID) }) != 0)
                {
                    return Err(unsafe_error(format!(
                        "{} is owned by {}, which can rewrite its ACL.",
                        path.display(),
                        subject.label
                    )));
                }
            }
            if let Some(subject) =
                first_writable_token_subject(token_subjects, risk_mask, |subject| {
                    effective_rights(dacl, &subject.sid)
                })?
            {
                return Err(unsafe_error(format!(
                    "{} grants write/replace rights to {}.",
                    path.display(),
                    subject.label
                )));
            }
            Ok(())
        })();
        unsafe {
            LocalFree(descriptor);
        }
        result
    }

    /// Keep ACL evaluation over every collected token subject in a small, testable unit.  The
    /// real caller obtains each result from Windows; the helper makes it impossible to
    /// accidentally stop at a historical allow-list of well-known groups.
    pub(super) fn first_writable_token_subject<'a>(
        subjects: &'a [TokenSubject],
        risk_mask: u32,
        mut rights_for: impl FnMut(&TokenSubject) -> Result<u32, String>,
    ) -> Result<Option<&'a TokenSubject>, String> {
        for subject in subjects {
            if rights_for(subject)? & risk_mask != 0 {
                return Ok(Some(subject));
            }
        }
        Ok(None)
    }

    fn effective_rights(
        dacl: *mut windows_sys::Win32::Security::ACL,
        sid: &[u8],
    ) -> Result<u32, String> {
        let trustee = TRUSTEE_W {
            pMultipleTrustee: null_mut(),
            MultipleTrusteeOperation: NO_MULTIPLE_TRUSTEE,
            TrusteeForm: TRUSTEE_IS_SID,
            TrusteeType: TRUSTEE_IS_UNKNOWN,
            ptstrName: sid.as_ptr() as *mut u16,
        };
        let mut rights = 0;
        let status = unsafe { GetEffectiveRightsFromAclW(dacl, &trustee, &mut rights) };
        if status == 0 {
            Ok(rights)
        } else {
            // An unparseable inherited-deny ACE or a domain lookup failure must not silently
            // turn into permission to create a highest-privilege scheduled task.
            Err(unsafe_error(format!(
                "无法可靠计算 ACL 的有效写权限（Windows 错误 {status}）。"
            )))
        }
    }

    pub(super) fn token_group_is_enabled(attributes: u32) -> bool {
        attributes & SE_GROUP_ENABLED != 0 && attributes & SE_GROUP_USE_FOR_DENY_ONLY == 0
    }

    fn push_token_subject(subjects: &mut Vec<TokenSubject>, label: String, sid: Vec<u8>) {
        if sid.is_empty() || subjects.iter().any(|subject| subject.sid == sid) {
            return;
        }
        subjects.push(TokenSubject { label, sid });
    }

    pub(super) fn append_enabled_token_group(
        subjects: &mut Vec<TokenSubject>,
        label: String,
        sid: Vec<u8>,
        attributes: u32,
    ) {
        if token_group_is_enabled(attributes) {
            push_token_subject(subjects, label, sid);
        }
    }

    fn current_token_subjects() -> Result<Vec<TokenSubject>, String> {
        let mut token: HANDLE = null_mut();
        if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
            return Err(unsafe_error(format!(
                "Could not open Carbon's current token (Windows error {}).",
                unsafe { GetLastError() }
            )));
        }
        let result = (|| {
            let (user_buffer, _) = token_information_buffer(token, TokenUser, "user")?;
            let user = unsafe { &*(user_buffer.as_ptr() as *const TOKEN_USER) };
            let mut subjects = Vec::new();
            push_token_subject(
                &mut subjects,
                "current token user".to_string(),
                copy_sid(user.User.Sid)?,
            );

            let (owner_buffer, _) = token_information_buffer(token, TokenOwner, "owner")?;
            let owner = unsafe { &*(owner_buffer.as_ptr() as *const TOKEN_OWNER) };
            push_token_subject(
                &mut subjects,
                "current token owner".to_string(),
                copy_sid(owner.Owner)?,
            );

            let (groups_buffer, groups_length) =
                token_information_buffer(token, TokenGroups, "groups")?;
            if groups_length < size_of::<u32>() {
                return Err(unsafe_error("Token group buffer is truncated.".to_string()));
            }
            let groups = unsafe { &*(groups_buffer.as_ptr() as *const TOKEN_GROUPS) };
            let buffer_start = groups_buffer.as_ptr() as usize;
            let buffer_end = buffer_start
                .checked_add(groups_length)
                .ok_or_else(|| unsafe_error("Token group buffer length overflowed.".to_string()))?;
            let first_group_address = buffer_start
                .checked_add(std::mem::offset_of!(TOKEN_GROUPS, Groups))
                .ok_or_else(|| unsafe_error("Token group buffer length overflowed.".to_string()))?;
            if first_group_address < buffer_start || first_group_address > buffer_end {
                return Err(unsafe_error("Token group buffer is malformed.".to_string()));
            }
            let available_groups =
                (buffer_end - first_group_address) / size_of::<SID_AND_ATTRIBUTES>();
            let group_count = groups.GroupCount as usize;
            if group_count > available_groups {
                return Err(unsafe_error("Token group buffer is truncated.".to_string()));
            }
            let first_group = first_group_address as *const SID_AND_ATTRIBUTES;
            for index in 0..group_count {
                let group = unsafe { *first_group.add(index) };
                append_enabled_token_group(
                    &mut subjects,
                    format!("enabled token group #{index}"),
                    copy_sid(group.Sid)?,
                    group.Attributes,
                );
            }
            if subjects.is_empty() {
                return Err(unsafe_error(
                    "Carbon could not determine any enabled token security subjects.".to_string(),
                ));
            }
            Ok(subjects)
        })();
        unsafe {
            CloseHandle(token);
        }
        result
    }

    fn token_information_buffer(
        token: HANDLE,
        information_class: i32,
        label: &str,
    ) -> Result<(Vec<usize>, usize), String> {
        let mut length = 0;
        unsafe {
            GetTokenInformation(token, information_class, null_mut(), 0, &mut length);
        }
        if length == 0 {
            return Err(unsafe_error(format!(
                "Could not determine the current token's {label} buffer length (Windows error {}).",
                unsafe { GetLastError() }
            )));
        }
        let word_size = size_of::<usize>();
        let word_count = (length as usize)
            .checked_add(word_size - 1)
            .ok_or_else(|| {
                unsafe_error("Token information buffer length overflowed.".to_string())
            })?
            / word_size;
        let mut buffer = vec![0usize; word_count.max(1)];
        let mut actual_length = length;
        if unsafe {
            GetTokenInformation(
                token,
                information_class,
                buffer.as_mut_ptr().cast(),
                length,
                &mut actual_length,
            )
        } == 0
        {
            return Err(unsafe_error(format!(
                "Could not read the current token's {label} information (Windows error {}).",
                unsafe { GetLastError() }
            )));
        }
        Ok((buffer, actual_length as usize))
    }

    fn copy_sid(sid: PSID) -> Result<Vec<u8>, String> {
        if sid.is_null() {
            return Err(unsafe_error(
                "The current token contains an invalid null SID.".to_string(),
            ));
        }
        let length = unsafe { GetLengthSid(sid) } as usize;
        if length == 0 {
            return Err(unsafe_error(
                "The current token contains an invalid SID.".to_string(),
            ));
        }
        Ok(unsafe { std::slice::from_raw_parts(sid.cast::<u8>(), length) }.to_vec())
    }

    fn wide(value: &OsStr) -> Vec<u16> {
        value.encode_wide().chain(Some(0)).collect()
    }
}

#[cfg(test)]
mod tests {
    #[cfg(target_os = "windows")]
    use super::windows_autostart::{
        append_enabled_token_group, first_writable_token_subject, task_query_code_is_missing,
        task_xml_matches, token_group_is_enabled, TokenSubject,
    };
    use super::{
        data_home_change_is_allowed, data_home_status_from_paths, desktop_preferences_for_launch,
        floating_board_fragment, has_background_arg, is_safe_floating_metadata, is_safe_local_path,
        is_supported_deep_link, legacy_app_data_dir, locked_data_home_status, main_task_fragment,
        normalize_data_home, parse_url, quote_windows_argument, validate_floating_board_target,
        validate_floating_task_target, AutostartMode, DesktopPreferences, FloatingBoardTarget,
        FloatingTaskTarget, BACKGROUND_ARG,
    };
    #[cfg(target_os = "windows")]
    use super::{
        locked_admin_data_home_from_args, portable_local_drive_path,
        sanitize_window_state_cache_file, sanitize_window_state_contents, ADMIN_DATA_HOME_ARG,
    };
    use std::cell::Cell;
    use std::fs;
    use std::path::{Path, PathBuf};

    #[test]
    fn floating_window_routes_accept_only_opaque_metadata() {
        let target = FloatingBoardTarget {
            cluster_id: Some("studio_2026".into()),
            project_id: Some("project-01H7X".into()),
            workspace_project_id: "project-01H7X".into(),
        };
        assert!(validate_floating_board_target(&target).is_ok());
        assert_eq!(
            floating_board_fragment(&target),
            "floating-board?workspace=project-01H7X&cluster=studio_2026&project=project-01H7X"
        );

        for unsafe_value in [
            "../outside",
            "https://example.test",
            "task?script=1",
            "a b",
            "",
        ] {
            assert!(!is_safe_floating_metadata(unsafe_value));
            assert!(validate_floating_board_target(&FloatingBoardTarget {
                cluster_id: None,
                project_id: Some(unsafe_value.into()),
                workspace_project_id: unsafe_value.into(),
            })
            .is_err());
        }
    }

    #[test]
    fn floating_task_route_is_canonical_and_rejects_route_injection() {
        let target = FloatingTaskTarget {
            cluster_id: None,
            project_id: Some("proj_7".into()),
            workspace_project_id: "proj_7".into(),
            task_id: "CARBON-42".into(),
        };
        assert!(validate_floating_task_target(&target).is_ok());
        assert_eq!(
            main_task_fragment(&target),
            "carbon/project/proj_7/task/CARBON-42"
        );
        assert!(validate_floating_task_target(&FloatingTaskTarget {
            cluster_id: Some("cluster/escape".into()),
            project_id: Some("proj_7".into()),
            workspace_project_id: "proj_7".into(),
            task_id: "CARBON-42".into(),
        })
        .is_err());

        let cluster_target = FloatingTaskTarget {
            cluster_id: Some("studio_2026".into()),
            project_id: None,
            workspace_project_id: "proj_7".into(),
            task_id: "CARBON-42".into(),
        };
        assert!(validate_floating_task_target(&cluster_target).is_ok());
        assert_eq!(
            main_task_fragment(&cluster_target),
            "carbon/studio_2026/proj_7/task/CARBON-42?taskScope=cluster"
        );
    }

    #[test]
    fn quotes_windows_executable_paths_with_spaces() {
        assert_eq!(
            quote_windows_argument(r"D:\Carbon portable\Carbon Portable.exe"),
            r#""D:\Carbon portable\Carbon Portable.exe""#
        );
    }

    #[test]
    fn quotes_backslashes_before_a_closing_quote() {
        assert_eq!(quote_windows_argument(r"C:\folder\\"), r#""C:\folder\\\\""#);
    }

    #[test]
    fn parses_only_supported_autostart_modes() {
        assert_eq!(AutostartMode::parse("off"), Ok(AutostartMode::Off));
        assert_eq!(AutostartMode::parse("user"), Ok(AutostartMode::User));
        assert_eq!(AutostartMode::parse("admin"), Ok(AutostartMode::Admin));
        assert!(AutostartMode::parse("administrator").is_err());
    }

    #[test]
    fn background_requires_an_exact_argument() {
        assert!(has_background_arg(&[BACKGROUND_ARG.to_string()]));
        assert!(!has_background_arg(&["--background=true".to_string()]));
    }

    #[test]
    fn accepts_only_carbon_deep_links() {
        assert!(is_supported_deep_link("carbon://task/PROJ-1"));
        assert!(!is_supported_deep_link("cairn://task/PROJ-1"));
        assert!(!is_supported_deep_link("https://example.test"));
    }

    #[test]
    fn parses_canonical_and_legacy_sidecar_handshakes() {
        assert_eq!(
            parse_url("CARBON_WEB_URL=http://127.0.0.1:2525"),
            Some("http://127.0.0.1:2525".to_string())
        );
        assert_eq!(
            parse_url("CAIRN_WEB_URL=http://127.0.0.1:2526"),
            Some("http://127.0.0.1:2526".to_string())
        );
    }

    #[test]
    fn finds_the_legacy_profile_only_beside_the_carbon_profile() {
        assert_eq!(
            legacy_app_data_dir(Path::new("/data/com.shaho.carbon")),
            Some(PathBuf::from("/data/com.shaho.cairn"))
        );
        assert_eq!(legacy_app_data_dir(Path::new("/data/custom")), None);
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn removes_only_the_clipped_restored_main_window_state() {
        let original = serde_json::json!({
            "main": {
                "width": 1968,
                "height": 741,
                "x": 461,
                "y": 156,
                "maximized": false,
                "fullscreen": false
            },
            "capture": {
                "width": 420,
                "height": 520,
                "maximized": false,
                "fullscreen": false
            },
            "futurePluginMetadata": { "version": 3 }
        });

        let sanitized = sanitize_window_state_contents(&serde_json::to_vec(&original).unwrap())
            .unwrap()
            .expect("the screenshot-sized main window must be reset");
        let cache: serde_json::Value = serde_json::from_slice(&sanitized).unwrap();

        assert!(cache.get("main").is_none());
        assert_eq!(cache["capture"], original["capture"]);
        assert_eq!(
            cache["futurePluginMetadata"],
            original["futurePluginMetadata"]
        );
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn preserves_normal_restored_main_window_sizes() {
        for (width, height) in [
            (1280, 832),
            (880, 600),
            (1920, 720),
            (1920, 800),
            (1960, 740),
            (1984, 754),
            (2952, 1112),
        ] {
            let cache = serde_json::json!({
                "main": {
                    "width": width,
                    "height": height,
                    "maximized": false,
                    "fullscreen": false
                }
            });
            assert!(
                sanitize_window_state_contents(&serde_json::to_vec(&cache).unwrap())
                    .unwrap()
                    .is_none()
            );
        }
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn preserves_wide_main_window_state_when_maximized_or_fullscreen() {
        for (maximized, fullscreen) in [(true, false), (false, true)] {
            let cache = serde_json::json!({
                "main": {
                    "width": 1968,
                    "height": 741,
                    "maximized": maximized,
                    "fullscreen": fullscreen
                }
            });
            assert!(
                sanitize_window_state_contents(&serde_json::to_vec(&cache).unwrap())
                    .unwrap()
                    .is_none()
            );
        }
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn removes_main_window_state_with_invalid_dimensions() {
        let invalid_caches = [
            serde_json::json!({
                "main": { "width": 0, "height": 741, "maximized": false, "fullscreen": false }
            }),
            serde_json::json!({
                "main": { "width": 1968, "height": 0, "maximized": true, "fullscreen": false }
            }),
            serde_json::json!({
                "main": { "width": "1968", "height": 741, "maximized": false, "fullscreen": false }
            }),
        ];

        for cache in invalid_caches {
            let sanitized = sanitize_window_state_contents(&serde_json::to_vec(&cache).unwrap())
                .unwrap()
                .expect("invalid dimensions must reset only the main entry");
            let sanitized: serde_json::Value = serde_json::from_slice(&sanitized).unwrap();
            assert!(sanitized.get("main").is_none());
        }
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn corrupt_window_state_cache_is_not_overwritten_by_the_sanitizer() {
        let path = std::env::temp_dir().join(format!(
            "carbon-window-state-test-{}-{}.json",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let original = br#"{ definitely not JSON"#;
        fs::write(&path, original).unwrap();

        assert!(sanitize_window_state_cache_file(&path).is_err());
        assert_eq!(fs::read(&path).unwrap(), original.to_vec());

        fs::remove_file(path).unwrap();
    }

    #[test]
    fn desktop_preferences_ignore_retired_window_settings() {
        let mut saved = serde_json::Map::new();
        saved.insert(
            ["floa", "ting"].concat(),
            serde_json::json!({ "alwaysOnTop": true, "size": { "width": 420, "height": 520 } }),
        );
        saved.insert(
            "notificationSound".into(),
            serde_json::json!({ "name": "tone", "extension": "wav" }),
        );

        let preferences: DesktopPreferences =
            serde_json::from_value(serde_json::Value::Object(saved)).unwrap();

        assert_eq!(preferences.notification_sound.unwrap().name, "tone");
    }

    #[test]
    fn data_home_status_keeps_the_launch_home_active_until_restart() {
        let default = Path::new("/carbon/default-home");
        let selected_for_next_launch = Path::new("/carbon/selected-home");

        let pending = data_home_status_from_paths(default, default, selected_for_next_launch);
        assert_eq!(pending.path, "/carbon/default-home");
        assert!(pending.is_default);
        assert_eq!(
            pending.pending_path.as_deref(),
            Some("/carbon/selected-home")
        );
        assert!(pending.restart_required);

        let after_restart = data_home_status_from_paths(
            selected_for_next_launch,
            default,
            selected_for_next_launch,
        );
        assert_eq!(after_restart.path, "/carbon/selected-home");
        assert!(!after_restart.is_default);
        assert_eq!(after_restart.pending_path, None);
        assert!(!after_restart.restart_required);
    }

    #[test]
    fn locked_admin_launch_uses_defaults_without_loading_user_preferences() {
        let loaded = Cell::new(false);
        let locked = Path::new(r"C:\ProgramData\Carbon\Locked");
        let preferences = desktop_preferences_for_launch(Some(locked), || {
            loaded.set(true);
            Ok(DesktopPreferences {
                data_home: Some(r"C:\Users\user\attacker-controlled".to_string()),
                ..DesktopPreferences::default()
            })
        })
        .unwrap();

        assert!(!loaded.get());
        assert_eq!(preferences.data_home, None);
        let status = locked_data_home_status(locked);
        assert_eq!(status.path, r"C:\ProgramData\Carbon\Locked");
        assert!(!status.is_default);
        assert_eq!(status.pending_path, None);
        assert!(!status.restart_required);
        assert!(!data_home_change_is_allowed(Some(locked)));
        assert!(data_home_change_is_allowed(None));
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn local_path_guard_rejects_unc_and_traversal() {
        assert!(is_safe_local_path(Path::new(r"C:\Carbon\sound.wav")));
        assert!(is_safe_local_path(Path::new(r"\\?\C:\Carbon\sound.wav")));
        assert!(!is_safe_local_path(Path::new(r"\\server\share\sound.wav")));
        assert!(!is_safe_local_path(Path::new(
            r"\\?\UNC\server\share\sound.wav"
        )));
        assert!(!is_safe_local_path(Path::new(r"\\.\C:\Carbon\sound.wav")));
        assert!(!is_safe_local_path(Path::new(r"C:\Carbon\..\sound.wav")));
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn canonical_local_drive_path_is_portable_for_the_go_sidecar() {
        assert_eq!(
            portable_local_drive_path(Path::new(r"\\?\D:\Carbon Home\项目")).unwrap(),
            PathBuf::from(r"D:\Carbon Home\项目")
        );
        assert_eq!(
            portable_local_drive_path(Path::new(r"D:\Carbon Home")).unwrap(),
            PathBuf::from(r"D:\Carbon Home")
        );
        assert!(portable_local_drive_path(Path::new(r"\\?\UNC\server\share")).is_err());
        assert!(portable_local_drive_path(Path::new(r"\\?\GLOBALROOT\Device\Harddisk0")).is_err());
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn administrator_data_home_argument_is_single_and_background_only() {
        let args = vec![
            BACKGROUND_ARG.to_string(),
            ADMIN_DATA_HOME_ARG.to_string(),
            r"C:\ProgramData\Carbon".to_string(),
        ];
        assert_eq!(
            locked_admin_data_home_from_args(&args).unwrap().as_deref(),
            Some(Path::new(r"C:\ProgramData\Carbon"))
        );
        assert!(locked_admin_data_home_from_args(&[
            ADMIN_DATA_HOME_ARG.to_string(),
            r"C:\ProgramData\Carbon".to_string(),
        ])
        .is_err());
        assert!(locked_admin_data_home_from_args(&[
            BACKGROUND_ARG.to_string(),
            ADMIN_DATA_HOME_ARG.to_string(),
            r"C:\ProgramData\Carbon".to_string(),
            ADMIN_DATA_HOME_ARG.to_string(),
            r"D:\Other".to_string(),
        ])
        .is_err());
        assert!(locked_admin_data_home_from_args(&[
            BACKGROUND_ARG.to_string(),
            ADMIN_DATA_HOME_ARG.to_string(),
            r"C:\ProgramData\Carbon".to_string(),
            "--unexpected".to_string(),
        ])
        .is_err());
        assert!(locked_admin_data_home_from_args(&[
            ADMIN_DATA_HOME_ARG.to_string(),
            r"C:\ProgramData\Carbon".to_string(),
            BACKGROUND_ARG.to_string(),
        ])
        .is_err());
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn every_enabled_custom_token_group_is_retained_for_acl_checks() {
        let mut subjects = vec![TokenSubject {
            label: "current token user".to_string(),
            sid: vec![9, 9, 9],
        }];
        append_enabled_token_group(
            &mut subjects,
            "custom build operators".to_string(),
            vec![1, 2, 3],
            0x0000_0004,
        );
        append_enabled_token_group(
            &mut subjects,
            "disabled group".to_string(),
            vec![4, 5, 6],
            0,
        );
        append_enabled_token_group(
            &mut subjects,
            "deny-only group".to_string(),
            vec![7, 8, 9],
            0x0000_0004 | 0x0000_0010,
        );

        assert!(token_group_is_enabled(0x0000_0004));
        assert!(!token_group_is_enabled(0));
        assert!(!token_group_is_enabled(0x0000_0004 | 0x0000_0010));
        assert_eq!(subjects.len(), 2);
        assert_eq!(subjects[1].label, "custom build operators");
        assert_eq!(subjects[1].sid, vec![1, 2, 3]);

        let writable = first_writable_token_subject(
            &subjects,
            0x0004_0000, // WRITE_DAC
            |subject| {
                Ok(if subject.label == "custom build operators" {
                    0x0004_0000 // WRITE_DAC
                } else {
                    0
                })
            },
        )
        .unwrap()
        .expect("the custom enabled group must participate in ACL write checks");
        assert_eq!(writable.label, "custom build operators");
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn normalized_data_home_returns_the_canonical_path() {
        let unique = format!(
            "carbon-data-home-test-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        );
        let temporary = std::env::temp_dir().join(unique);
        fs::create_dir_all(&temporary).unwrap();
        let expected = fs::canonicalize(&temporary).unwrap();
        let actual = normalize_data_home(&temporary).unwrap();
        assert_eq!(actual, expected);
        fs::remove_dir_all(&temporary).unwrap();
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn scheduled_task_ownership_requires_the_full_executable_path() {
        let xml = r#"<Task><Triggers><LogonTrigger></LogonTrigger></Triggers><Principals><Principal><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Actions><Exec><Command>D:\Carbon\Carbon Portable.exe</Command><Arguments>--background --carbon-admin-data-home &quot;D:\ProgramData\Carbon Home&quot;</Arguments></Exec></Actions></Task>"#;
        assert!(task_xml_matches(
            xml,
            Path::new(r"D:\Carbon\Carbon Portable.exe"),
            Path::new(r"D:\ProgramData\Carbon Home")
        )
        .unwrap());
        assert!(!task_xml_matches(
            xml,
            Path::new(r"D:\Other\Carbon Portable.exe"),
            Path::new(r"D:\ProgramData\Carbon Home")
        )
        .unwrap());
        assert!(!task_xml_matches(
            xml,
            Path::new(r"D:\Carbon\Carbon Portable.exe"),
            Path::new(r"D:\ProgramData\Other")
        )
        .unwrap());
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn scheduled_task_ownership_rejects_multiple_actions_execs_principals_and_triggers() {
        let xml = r#"<Task><Triggers><LogonTrigger></LogonTrigger></Triggers><Principals><Principal><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Actions><Exec><Command>D:\Carbon\Carbon Portable.exe</Command><Arguments>--background --carbon-admin-data-home &quot;D:\ProgramData\Carbon Home&quot;</Arguments></Exec></Actions></Task>"#;
        let executable = Path::new(r"D:\Carbon\Carbon Portable.exe");
        let data_home = Path::new(r"D:\ProgramData\Carbon Home");
        let variants = [
            xml.replacen(
                "</Exec>",
                "</Exec><Exec><Command>D:\\Other.exe</Command><Arguments>--background</Arguments></Exec>",
                1,
            ),
            xml.replacen(
                "</Actions>",
                "</Actions><Actions><Exec><Command>D:\\Other.exe</Command><Arguments>--background</Arguments></Exec></Actions>",
                1,
            ),
            xml.replacen(
                "</Principal>",
                "</Principal><Principal><RunLevel>HighestAvailable</RunLevel></Principal>",
                1,
            ),
            xml.replacen(
                "</LogonTrigger>",
                "</LogonTrigger><LogonTrigger></LogonTrigger>",
                1,
            ),
            xml.replacen(
                "</LogonTrigger>",
                "</LogonTrigger><BootTrigger></BootTrigger>",
                1,
            ),
        ];

        for candidate in variants {
            assert!(!task_xml_matches(&candidate, executable, data_home).unwrap());
        }
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn only_file_not_found_is_treated_as_a_missing_scheduled_task() {
        assert!(task_query_code_is_missing(Some(0x8007_0002_u32 as i32)));
        assert!(!task_query_code_is_missing(Some(0x8007_0005_u32 as i32)));
        assert!(!task_query_code_is_missing(Some(1)));
        assert!(!task_query_code_is_missing(None));
    }
}
