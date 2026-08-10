fn main() {
    // Declare our app commands so the ACL can authorize them for the remote webview origin
    // (the UI runs from the Go server's http://127.0.0.1:* URL, not tauri://). Without this,
    // invoke() of a custom command is rejected with "not allowed by ACL".
    let mut attributes =
        tauri_build::Attributes::new().app_manifest(tauri_build::AppManifest::new().commands(&[
            "is_portable",
            "get_autostart_mode",
            "set_autostart_mode",
            "set_ui_language",
            "update_tray",
            "is_notification_owner",
            "choose_notification_sound",
            "get_notification_sound",
            "clear_notification_sound",
            "play_notification_sound",
            "get_data_home",
            "set_data_home",
        ]));

    // GNU windres cannot open an icon through a path containing non-ASCII characters.
    // Copy it into Cargo's output directory, which the portable build script deliberately
    // places under an ASCII-only path when required.
    #[cfg(windows)]
    {
        let out_dir = std::path::PathBuf::from(
            std::env::var_os("OUT_DIR").expect("Cargo did not provide OUT_DIR"),
        );
        let build_icon = out_dir.join("carbon.ico");
        std::fs::copy("icons/icon.ico", &build_icon)
            .expect("failed to copy the Windows icon into Cargo's output directory");
        attributes = attributes
            .windows_attributes(tauri_build::WindowsAttributes::new().window_icon_path(build_icon));
    }

    tauri_build::try_build(attributes).expect("failed to run tauri-build");
}
