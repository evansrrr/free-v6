#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, Mutex,
};
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager,
};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

struct HelperState {
    child: Mutex<Option<CommandChild>>,
    stopping: Arc<AtomicBool>,
}

// Set when the app is exiting for real (tray 退出 → app.exit). Close requests
// arriving during teardown must not be swallowed by the close-to-tray handler.
static QUITTING: AtomicBool = AtomicBool::new(false);

// Custom window controls for the frameless (decorations: false) window.
// Invoked from the web UI through __TAURI_INTERNALS__.invoke.

#[tauri::command]
fn window_minimize(app: tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.minimize();
    }
}

#[tauri::command]
fn window_toggle_maximize(app: tauri::AppHandle) -> bool {
    if let Some(window) = app.get_webview_window("main") {
        // WebviewWindow has no toggle_maximize in this version — flip manually
        let maximized = window.is_maximized().unwrap_or(false);
        if maximized {
            let _ = window.unmaximize();
        } else {
            let _ = window.maximize();
        }
        return !maximized;
    }
    false
}

#[tauri::command]
fn window_maximized(app: tauri::AppHandle) -> bool {
    app.get_webview_window("main")
        .and_then(|window| window.is_maximized().ok())
        .unwrap_or(false)
}

#[tauri::command]
fn window_close(app: tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.close();
    }
}

#[tauri::command]
fn window_start_dragging(app: tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.start_dragging();
    }
}

// Un-hide the window so the quit confirmation is visible even when the app
// runs tray-only (close-to-tray, --minimized, 静默启动).
#[tauri::command]
fn window_show(app: tauri::AppHandle) {
    show_main_window(&app);
}

#[tauri::command]
fn app_quit(app: tauri::AppHandle) {
    app.exit(0);
}

// In-app update: spawn the downloaded NSIS package detached (/S = silent,
// /FREEV6REL = our POSTINSTALL hook relaunched the new build with the token
// this elevated process passes on — no UAC, single instance), then kill the
// helper so freev6-helper.exe is unlocked long before NSIS reaches its File
// copies, then exit (the desktop exe lock drops within ms; the stock silent
// check force-kills it as a last resort). Spawn happens first: if it fails
// the app and its helper stay alive so the UI can show the error.
#[tauri::command]
fn update_apply(app: tauri::AppHandle, installer: String) -> Result<String, String> {
    use std::os::windows::process::CommandExt;

    if !std::path::Path::new(&installer).is_file() {
        return Err(format!("安装包不存在: {installer}"));
    }
    std::process::Command::new(&installer)
        .args(["/S", "/FREEV6REL"])
        // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP — installer survives us
        .creation_flags(0x0000_0008 | 0x0000_0200)
        .spawn()
        .map_err(|e| format!("启动安装器失败: {e}"))?;
    stop_helper(&app);
    app.exit(0);
    Ok("ok".into())
}

// Show (or restore) the main GUI window — shared by the tray menu item and
// the tray left-click handler.
fn show_main_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

// The ONLOGON autostart task launches the exe with --autostart; Task
// Manager's Startup tab toggle lives in StartupApproved\Run\FreeV6 (first
// byte 02 = enabled, 03 = user-disabled). A missing value means the toggle
// was never used → allow (Windows treats absence as enabled). Read through
// reg.exe: std-only, no extra crate.
fn task_manager_allows_autostart() -> bool {
    let output = std::process::Command::new("reg")
        .args([
            "query",
            r"HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run",
            "/v",
            "FreeV6",
        ])
        .output();
    match output {
        Ok(out) if out.status.success() => {
            let text = String::from_utf8_lossy(&out.stdout);
            text.lines()
                .find_map(|line| line.split("REG_BINARY").nth(1))
                .map(|hex| hex.trim().starts_with("02"))
                .unwrap_or(true)
        }
        _ => true,
    }
}

// Tray-only launch decision: the autostart task passes --minimized/--autostart,
// and 静默启动 (设置 → 通用) makes EVERY launch skip the window. The setting is
// read from config/settings.json next to the exe (the helper persists the
// silentStart field there); whitespace is stripped first so hand-edited
// spacing still matches. Manual launches without it show the window.
fn should_show_window() -> bool {
    let flagged = std::env::args().any(|arg| {
        let arg = arg.as_str();
        arg == "--minimized" || arg == "--autostart"
    });
    if flagged {
        return false;
    }
    let silent = std::env::current_exe()
        .ok()
        .and_then(|exe| {
            exe.parent()
                .map(|dir| dir.join("config").join("settings.json"))
        })
        .and_then(|path| std::fs::read_to_string(path).ok())
        .map(|raw| {
            let compact: String = raw.chars().filter(|c| !c.is_whitespace()).collect();
            compact.contains("\"silentStart\":true")
        })
        .unwrap_or(false);
    !silent
}

fn main() {
    // Launched by the autostart logon task: honor Task Manager's Startup tab
    // toggle before any window or tray icon exists. Manual launches have no
    // --autostart flag and always run.
    if std::env::args().any(|arg| arg.as_str() == "--autostart") && !task_manager_allows_autostart()
    {
        return;
    }
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            start_helper(app)?;
            let show = MenuItem::with_id(app, "show", "打开 freev6", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show, &quit])?;
            TrayIconBuilder::new()
                .menu(&menu)
                .icon(
                    app.default_window_icon()
                        .expect("default window icon")
                        .clone(),
                )
                .tooltip("freev6")
                // Left click opens the GUI directly; the context menu (打开/退出)
                // stays available on right click.
                .show_menu_on_left_click(false)
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        show_main_window(tray.app_handle());
                    }
                })
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => show_main_window(app),
                    // Hand off to the web UI: it confirms with the user when
                    // 免流 is still running — mihomo is an independent TUN
                    // process that would outlive the app — then invokes
                    // app_quit. If the webview isn't ready yet the eval is a
                    // no-op (quitting within ms of launch is a non-issue).
                    "quit" => {
                        if let Some(window) = app.get_webview_window("main") {
                            let _ = window.eval("window.handleTrayQuit && window.handleTrayQuit()");
                        } else {
                            app.exit(0);
                        }
                    }
                    _ => {}
                })
                .build(app)?;
            // Tray-only launch: autostart flags (--minimized/--autostart) or
            // 静默启动 on → show nothing but the tray icon; otherwise the
            // window starts hidden (tauri.conf visible:false) and appears here.
            if should_show_window() {
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.show();
                    let _ = window.set_focus();
                }
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            window_minimize,
            window_toggle_maximize,
            window_maximized,
            window_close,
            window_start_dragging,
            window_show,
            app_quit,
            update_apply
        ])
        .build(tauri::generate_context!())
        .expect("error while building freev6")
        .run(|app, event| match event {
            // Closing the window hides it to the tray; quitting lives in the
            // tray menu (退出). Verified against tauri 2.11: CloseRequested
            // carries CloseRequestApi with prevent_close().
            tauri::RunEvent::WindowEvent {
                event: tauri::WindowEvent::CloseRequested { api, .. },
                ..
            } => {
                if !QUITTING.load(Ordering::Relaxed) {
                    api.prevent_close();
                    if let Some(window) = app.get_webview_window("main") {
                        let _ = window.hide();
                    }
                }
            }
            // Programmatic exit (tray 退出) — let any late close through.
            tauri::RunEvent::ExitRequested { code: Some(_), .. } => {
                QUITTING.store(true, Ordering::Release);
            }
            tauri::RunEvent::Exit => stop_helper(app),
            _ => {}
        });
}

fn start_helper(app: &mut tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let root = std::env::current_exe()?
        .parent()
        .ok_or("freev6 executable has no parent directory")?
        .to_path_buf();
    let (mut events, child) = app
        .shell()
        .sidecar("freev6-helper")?
        .env("FREEV6_ROOT", root)
        .spawn()?;
    let stopping = Arc::new(AtomicBool::new(false));
    app.manage(HelperState {
        child: Mutex::new(Some(child)),
        stopping: stopping.clone(),
    });
    let app_handle = app.handle().clone();
    tauri::async_runtime::spawn(async move {
        while let Some(event) = events.recv().await {
            match event {
                CommandEvent::Stderr(line) => {
                    eprintln!("[freev6-helper] {}", String::from_utf8_lossy(&line))
                }
                CommandEvent::Terminated(status) => {
                    eprintln!("freev6-helper exited: {status:?}");
                    if !stopping.load(Ordering::Acquire) {
                        app_handle.exit(1);
                    }
                    break;
                }
                _ => {}
            }
        }
    });
    Ok(())
}

fn stop_helper(app: &tauri::AppHandle) {
    if let Some(state) = app.try_state::<HelperState>() {
        state.stopping.store(true, Ordering::Release);
        if let Ok(mut child) = state.child.lock() {
            if let Some(child) = child.take() {
                let _ = child.kill();
            }
        }
    }
}
