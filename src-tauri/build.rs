fn main() {
    // The production manifest demands requireAdministrator, so `cargo test`
    // would fail unelevated with os error 740 before running a single test.
    // FREEV6_TEST_MANIFEST=1 swaps in asInvoker for that build only; every
    // normal build (tauri dev/build) leaves it unset and keeps the UAC prompt.
    println!("cargo:rerun-if-env-changed=FREEV6_TEST_MANIFEST");
    let manifest = if std::env::var_os("FREEV6_TEST_MANIFEST").is_some() {
        include_str!("windows-app-manifest.xml").replace("requireAdministrator", "asInvoker")
    } else {
        include_str!("windows-app-manifest.xml").to_string()
    };

    let windows = tauri_build::WindowsAttributes::new()
        .window_icon_path("icons/icon.ico")
        .app_manifest(manifest);
    let attributes = tauri_build::Attributes::new().windows_attributes(windows);
    tauri_build::try_build(attributes).expect("failed to run Tauri build script");
}
