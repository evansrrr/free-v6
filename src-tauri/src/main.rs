#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, Mutex,
};
use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
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
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => {
                        if let Some(window) = app.get_webview_window("main") {
                            let _ = window.show();
                            let _ = window.set_focus();
                        }
                    }
                    "quit" => app.exit(0),
                    _ => {}
                })
                .build(app)?;
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building freev6")
        .run(|app, event| {
            if let tauri::RunEvent::Exit = event {
                stop_helper(app);
            }
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
