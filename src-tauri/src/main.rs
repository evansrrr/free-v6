#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::sync::{
    atomic::{AtomicBool, AtomicU32, Ordering},
    Arc, Mutex,
};
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager,
};
use tauri_plugin_opener::OpenerExt;
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

// 关于页「使用说明」：open README.md shipped next to the exe via
// bundle.resources ("../README.md" -> "README.md" under resource_dir). 
#[tauri::command]
fn open_readme(app: tauri::AppHandle) -> Result<(), String> {
    let dir = app.path().resource_dir().map_err(|e| e.to_string())?;
    let file = dir.join("README.md");
    if !file.is_file() {
        return Err(format!("README.md 不存在: {}", file.display()));
    }
    app.opener()
        .open_path(file.to_string_lossy().into_owned(), None::<&str>)
        .map_err(|e| e.to_string())
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
// and 静默启动 (设置 → 通用) makes EVERY launch skip the window. Both this and
// the F6 hotkey read their flag from config/settings.json next to the exe (the
// helper persists the fields there); whitespace is stripped first so
// hand-edited spacing still matches. Manual launches without it show the window.
fn settings_flag(name: &str) -> bool {
    std::env::current_exe()
        .ok()
        .and_then(|exe| {
            exe.parent()
                .map(|dir| dir.join("config").join("settings.json"))
        })
        .and_then(|path| std::fs::read_to_string(path).ok())
        .map(|raw| {
            let compact: String = raw.chars().filter(|c| !c.is_whitespace()).collect();
            compact.contains(&format!("\"{name}\":true"))
        })
        .unwrap_or(false)
}

fn should_show_window() -> bool {
    let flagged = std::env::args().any(|arg| {
        let arg = arg.as_str();
        arg == "--minimized" || arg == "--autostart"
    });
    if flagged {
        return false;
    }
    !settings_flag("silentStart")
}

/* ── Global hotkey (设置 → 通用「快捷键」) ─────────────────────────── */
// F6 opens the main window even while it is hidden (close-to-tray,
// --minimized, 静默启动). RegisterHotKey binds the hot key to the thread
// that registers it, so a dedicated thread owns BOTH the registration and
// the GetMessage loop that receives WM_HOTKEY. The web UI toggles it at
// runtime via hotkey_set → PostThreadMessageW to that thread; the
// acknowledgment travels back over a channel so a busy F6 (already taken by
// another app) fails the GUI switch instead of silently doing nothing.
// Raw Win32 FFI instead of a crate: nothing else from user32 is needed and
// Cargo.lock stays untouched.
const HOTKEY_ID: i32 = 0x4635_0001; // 'F'6… arbitrary unique id
const HOTKEY_VK_F6: u32 = 0x75;
const HOTKEY_MOD_NOREPEAT: u32 = 0x4000;
const WM_HOTKEY: u32 = 0x0312;
// WM_APP + 1: wParam 1 → register, 0 → unregister (payload from hotkey_set)
const WM_HOTKEY_CMD: u32 = 0x8001;

static HOTKEY_THREAD_ID: AtomicU32 = AtomicU32::new(0);
static HOTKEY_ACK: Mutex<Option<std::sync::mpsc::Sender<bool>>> = Mutex::new(None);

// Win32 MSG. Only message/wParam are read; layout is asserted below and the
// struct is deliberately LARGER than the SDK's MSG so GetMessageW can never
// write past our buffer. Field names mirror the SDK (hence non_snake_case).
#[repr(C)]
#[derive(Default)]
#[allow(non_snake_case)]
struct Win32Msg {
    hwnd: isize,
    message: u32,
    wParam: usize,
    lParam: isize,
    time: u32,
    pt_x: i32,
    pt_y: i32,
    l_private: u32,
    _extra: [u32; 8],
}

const _: () = assert!(std::mem::offset_of!(Win32Msg, message) == 8);
const _: () = assert!(std::mem::offset_of!(Win32Msg, wParam) == 16);

#[link(name = "user32")]
extern "system" {
    fn RegisterHotKey(hWnd: isize, id: i32, fsModifiers: u32, vk: u32) -> i32;
    fn UnregisterHotKey(hWnd: isize, id: i32) -> i32;
    fn GetMessageW(lpMsg: *mut Win32Msg, hWnd: isize, filterMin: u32, filterMax: u32) -> i32;
    fn PeekMessageW(lpMsg: *mut Win32Msg, hWnd: isize, filterMin: u32, filterMax: u32, remove: u32) -> i32;
    fn PostThreadMessageW(idThread: u32, msg: u32, wParam: usize, lParam: isize) -> i32;
}

#[link(name = "kernel32")]
extern "system" {
    fn GetCurrentThreadId() -> u32;
}

// Runs on its own thread: registers `vk` (when initially_enabled) and pumps
// WM_HOTKEY forever, invoking `on_hotkey` on every press. `vk` is a parameter
// so tests can grab an unused key (VK_F24) instead of fighting over F6.
fn start_hotkey_thread(
    on_hotkey: impl Fn() + Send + 'static,
    initially_enabled: bool,
    vk: u32,
) {
    std::thread::spawn(move || {
        let mut msg = Win32Msg::default();
        unsafe {
            // PeekMessageW creates this thread's message queue. PostThreadMessageW
            // fails against a thread with no queue, so the id is published only
            // AFTER this call.
            PeekMessageW(&mut msg, 0, 0, 0, 0);
        }
        HOTKEY_THREAD_ID.store(unsafe { GetCurrentThreadId() }, Ordering::SeqCst);

        let mut registered = false;
        if initially_enabled {
            registered = unsafe { RegisterHotKey(0, HOTKEY_ID, HOTKEY_MOD_NOREPEAT, vk) } != 0;
            if !registered {
                eprintln!("hotkey: registration failed (key already taken by another app?)");
            }
        }

        while unsafe { GetMessageW(&mut msg, 0, 0, 0) } > 0 {
            if msg.message == WM_HOTKEY && msg.wParam == HOTKEY_ID as usize {
                on_hotkey();
            } else if msg.message == WM_HOTKEY_CMD {
                let want = msg.wParam != 0;
                let ok = if want == registered {
                    true // idempotent — nothing to do
                } else if want {
                    registered = unsafe { RegisterHotKey(0, HOTKEY_ID, HOTKEY_MOD_NOREPEAT, vk) } != 0;
                    registered
                } else {
                    unsafe { UnregisterHotKey(0, HOTKEY_ID) };
                    registered = false;
                    true
                };
                // Hand the result back to hotkey_set (None if it gave up waiting).
                if let Some(tx) = HOTKEY_ACK.lock().ok().and_then(|mut g| g.take()) {
                    let _ = tx.send(ok);
                }
            }
        }
    });
}

// Register/unregister F6 at runtime (设置 → 通用「快捷键」). Blocking wait on
// the hotkey thread's ack, but it normally answers in well under a millisecond;
// the timeout only bounds the pathological "thread not pumping" case.
#[tauri::command]
fn hotkey_set(enabled: bool) -> Result<(), String> {
    let tid = HOTKEY_THREAD_ID.load(Ordering::SeqCst);
    if tid == 0 {
        return Err("热键线程未就绪".into());
    }
    let (tx, rx) = std::sync::mpsc::channel();
    {
        let mut guard = HOTKEY_ACK.lock().map_err(|_| "热键状态锁不可用".to_string())?;
        *guard = Some(tx);
    }
    if unsafe { PostThreadMessageW(tid, WM_HOTKEY_CMD, enabled as usize, 0) } == 0 {
        if let Ok(mut guard) = HOTKEY_ACK.lock() {
            *guard = None;
        }
        return Err("热键设置消息投递失败".into());
    }
    match rx.recv_timeout(std::time::Duration::from_millis(1500)) {
        Ok(true) => Ok(()),
        Ok(false) => Err("F6 已被其他软件占用，快捷键注册失败".into()),
        Err(_) => {
            if let Ok(mut guard) = HOTKEY_ACK.lock() {
                *guard = None;
            }
            Err("热键线程未响应".into())
        }
    }
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
            // F6 hotkey must work even when the window never shows (tray-only /
            // 静默启动), so it lives outside the webview: register per the
            // persisted setting before any UI exists.
            let handle = app.handle().clone();
            start_hotkey_thread(
                move || show_main_window(&handle),
                settings_flag("hotkeyEnabled"),
                HOTKEY_VK_F6,
            );
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
            hotkey_set,
            update_apply,
            open_readme
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicUsize;

    // End-to-end over the real Win32 thread: enable → register, WM_HOTKEY →
    // callback, disable → unregister. Uses VK_F24 (0x87) instead of F6 so a
    // parallel test run — or any app that grabbed F6 — can't make this flaky.
    // One test only: the thread id / ack channel are process-wide statics.
    #[test]
    fn hotkey_thread_enable_press_disable() {
        const VK_F24: u32 = 0x87;
        let presses = Arc::new(AtomicUsize::new(0));
        let counter = presses.clone();
        start_hotkey_thread(move || { counter.fetch_add(1, Ordering::SeqCst); }, false, VK_F24);

        // The thread publishes its id only after PeekMessageW created its queue.
        let mut tid = 0u32;
        for _ in 0..200 {
            tid = HOTKEY_THREAD_ID.load(Ordering::SeqCst);
            if tid != 0 {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
        assert_ne!(tid, 0, "hotkey thread never published its id");

        // Disabled at start → first enable must really call RegisterHotKey.
        hotkey_set(true).expect("VK_F24 registration should succeed");

        // Synthesize the WM_HOTKEY Windows sends on a press.
        unsafe {
            assert_ne!(
                PostThreadMessageW(tid, WM_HOTKEY, HOTKEY_ID as usize, 0),
                0,
                "PostThreadMessageW(WM_HOTKEY) failed"
            );
        }
        let mut got = 0;
        for _ in 0..200 {
            got = presses.load(Ordering::SeqCst);
            if got > 0 {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
        assert_eq!(got, 1, "WM_HOTKEY must reach the callback exactly once");

        // Toggle off and back on — both are idempotent against live state.
        hotkey_set(false).expect("unregister should succeed");
        hotkey_set(true).expect("re-register should succeed");
        hotkey_set(true).expect("second enable is a no-op, still Ok");
        hotkey_set(false).expect("final unregister should succeed");
    }
}
