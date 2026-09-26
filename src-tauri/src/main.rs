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

// Show (or restore) the main GUI window — shared by the tray menu item and
// the tray left-click handler.
fn show_main_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

fn main() {
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
                    "quit" => app.exit(0),
                    _ => {}
                })
                .build(app)?;
            // Tray-only launch (autostart Run entry passes --minimized): show
            // nothing but the tray icon; normal launches show the window.
            if !std::env::args_os().any(|arg| arg == "--minimized") {
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
            window_start_dragging
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
