//! 前端可调用的 IPC 命令。业务逻辑都在 flow / hotkey 等模块里，这里只做参数转换。

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, WebviewWindow};
use tauri_plugin_autostart::ManagerExt;

use crate::{
    capture::crop::CssRect,
    flow::{self, CloseReason, OverlayPayload, SelectorFrame},
    hotkey, selector,
    state::AppState,
    storage::settings::{Settings, DEFAULT_SHORTCUT},
};

pub const SETTINGS_LABEL: &str = "settings";

pub fn show_settings(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(SETTINGS_LABEL) {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
    }
}

fn monitor_index(window: &WebviewWindow) -> Option<usize> {
    window.label().strip_prefix(selector::LABEL_PREFIX)?.parse().ok()
}

// ---------- selector ----------

#[tauri::command]
pub fn selector_frame(app: AppHandle, window: WebviewWindow) -> Option<SelectorFrame> {
    flow::current_selector_frame(&app, monitor_index(&window)?)
}

#[tauri::command]
pub fn selector_ready(app: AppHandle, window: WebviewWindow, session: u64) {
    if let Some(i) = monitor_index(&window) {
        flow::on_selector_ready(&app, i, session);
    }
}

#[tauri::command]
pub fn selection_done(app: AppHandle, window: WebviewWindow, session: u64, rect: CssRect, viewport_w: f64, viewport_h: f64) {
    if let Some(i) = monitor_index(&window) {
        flow::on_selection(&app, i, session, rect, viewport_w, viewport_h);
    }
}

#[tauri::command]
pub fn cancel_flow(app: AppHandle) {
    flow::cancel(&app, CloseReason::Escape);
}

// ---------- overlay ----------

#[tauri::command]
pub fn overlay_state(app: AppHandle) -> Option<OverlayPayload> {
    app.state::<AppState>().flow.lock().unwrap().overlay.clone()
}

#[tauri::command]
pub fn overlay_resize(app: AppHandle, height: f64) {
    flow::resize_overlay(&app, height.clamp(60.0, 800.0));
}

#[tauri::command]
pub fn copy_reply(app: AppHandle, index: usize) -> Result<(), String> {
    flow::copy_reply(&app, index)
}

// ---------- settings ----------

#[derive(Serialize)]
pub struct SettingsView {
    shortcut: String,
    default_shortcut: &'static str,
    server_url: String,
    display_name: String,
    launch_at_login: bool,
    version: &'static str,
}

#[derive(Deserialize)]
pub struct SettingsInput {
    shortcut: String,
    server_url: String,
    display_name: String,
    launch_at_login: bool,
}

fn view(s: &Settings) -> SettingsView {
    SettingsView {
        shortcut: s.shortcut.clone(),
        default_shortcut: DEFAULT_SHORTCUT,
        server_url: s.server_url.clone(),
        display_name: s.display_name.clone(),
        launch_at_login: s.launch_at_login,
        version: env!("CARGO_PKG_VERSION"),
    }
}

#[tauri::command]
pub fn get_settings(app: AppHandle) -> SettingsView {
    view(&app.state::<AppState>().settings.lock().unwrap())
}

#[tauri::command]
pub fn save_settings(app: AppHandle, input: SettingsInput) -> Result<SettingsView, String> {
    let server_url = input.server_url.trim().trim_end_matches('/').to_string();
    if !(server_url.starts_with("http://") || server_url.starts_with("https://")) {
        return Err("服务端地址必须以 http:// 或 https:// 开头".into());
    }
    let shortcut = input.shortcut.trim().to_string();
    hotkey::register_main(&app, &shortcut)?;

    let state = app.state::<AppState>();
    let current_autostart = state.settings.lock().unwrap().launch_at_login;
    if current_autostart != input.launch_at_login {
        let al = app.autolaunch();
        let r = if input.launch_at_login { al.enable() } else { al.disable() };
        r.map_err(|e| format!("开机启动设置失败：{e}"))?;
    }

    let mut s = state.settings.lock().unwrap();
    s.shortcut = shortcut;
    s.server_url = server_url;
    s.display_name = input.display_name.trim().chars().take(64).collect();
    s.launch_at_login = input.launch_at_login;
    s.save(&state.settings_path)?;
    Ok(view(&s))
}
