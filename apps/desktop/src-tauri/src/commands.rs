//! 前端可调用的 IPC 命令。业务逻辑都在 flow / hotkey 等模块里，这里只做参数转换。

use std::sync::atomic::Ordering;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, Manager, WebviewWindow};
use tauri_plugin_autostart::ManagerExt;
use tauri_plugin_opener::OpenerExt;

use crate::{
    account::{login, AuthError},
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
pub fn selector_ready(app: AppHandle, window: WebviewWindow, session: u64, monitor: usize) {
    if let Some(i) = monitor_index(&window) {
        flow::on_selector_ready(&app, i, session, monitor);
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

// ---------- account ----------

pub const EVT_ACCOUNT_CHANGED: &str = "account:changed";

/// 账户信息：服务端 /v1/me 的结果，附加本地登录状态。
#[tauri::command]
pub async fn account_status(app: AppHandle) -> Result<serde_json::Value, String> {
    let creds = app.state::<AppState>().credentials();
    let mut me = creds.me().await.map_err(|e| e.to_string())?;
    me["logged_in"] = serde_json::Value::Bool(creds.is_logged_in());
    Ok(me)
}

/// 用系统浏览器登录。返回时登录已完成（或失败 / 超时）。
#[tauri::command]
pub async fn account_login(app: AppHandle) -> Result<(), String> {
    let (creds, busy) = {
        let state = app.state::<AppState>();
        (state.credentials(), state.login_in_progress.swap(true, Ordering::SeqCst))
    };
    if busy {
        return Err("登录正在进行中，请在浏览器里完成".into());
    }
    let result = async {
        // 确保设备已在服务端登记（绑定账号、判断注册赠送都以设备为单位）
        creds.device_token().await?;
        let req = login::prepare(&creds)?;
        #[cfg(debug_assertions)]
        log::debug!("login url: {}", req.url);
        app.opener().open_url(req.url.clone(), None::<&str>).map_err(|e| AuthError::Server(format!("无法打开浏览器：{e}")))?;
        login::complete(&creds, req).await
    }
    .await;
    app.state::<AppState>().login_in_progress.store(false, Ordering::SeqCst);
    result.map_err(|e| e.to_string())?;

    let _ = app.emit(EVT_ACCOUNT_CHANGED, ());
    show_settings(&app);
    Ok(())
}

#[tauri::command]
pub async fn account_logout(app: AppHandle) -> Result<(), String> {
    let creds = app.state::<AppState>().credentials();
    creds.logout().await;
    let _ = app.emit(EVT_ACCOUNT_CHANGED, ());
    Ok(())
}

/// 在浏览器中打开结账（purpose = "checkout"，需要 plan）或订阅管理（purpose = "portal"）页面。
#[tauri::command]
pub async fn billing_open(app: AppHandle, purpose: String, plan: Option<String>) -> Result<(), String> {
    let creds = app.state::<AppState>().credentials();
    let url = creds.billing_link(&purpose, plan.as_deref()).await.map_err(|e| e.to_string())?;
    app.opener().open_url(url, None::<&str>).map_err(|e| format!("无法打开浏览器：{e}"))
}

/// overlay 上的"去登录 / 去升级"：先关闭当前流程，再打开设置页。
#[tauri::command]
pub fn open_account(app: AppHandle) {
    flow::cancel(&app, CloseReason::Escape);
    show_settings(&app);
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
