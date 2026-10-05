//! 框选窗口：每个显示器一个，覆盖整个显示器，显示冻结画面。窗口复用，只隐藏不销毁。

use tauri::{AppHandle, Manager, PhysicalPosition, PhysicalSize, WebviewUrl, WebviewWindow, WebviewWindowBuilder};

use crate::geom::Rect;

pub const LABEL_PREFIX: &str = "selector-";

pub fn label(index: usize) -> String {
    format!("{LABEL_PREFIX}{index}")
}

pub fn ensure(app: &AppHandle, index: usize) -> tauri::Result<WebviewWindow> {
    let label = label(index);
    if let Some(w) = app.get_webview_window(&label) {
        return Ok(w);
    }
    WebviewWindowBuilder::new(app, &label, WebviewUrl::App("selector.html".into()))
        .title("NextReply Selector")
        .decorations(false)
        .resizable(false)
        .shadow(false)
        .always_on_top(true)
        .skip_taskbar(true)
        .visible(false)
        .focused(false)
        .content_protected(true)
        .visible_on_all_workspaces(true)
        .build()
}

/// 把窗口铺满指定显示器（物理像素）。
pub fn cover(win: &WebviewWindow, bounds: Rect) -> tauri::Result<()> {
    let pos = PhysicalPosition::new(bounds.x, bounds.y);
    win.set_position(pos)?;
    win.set_size(PhysicalSize::new(bounds.w as u32, bounds.h as u32))?;
    win.set_position(pos)?;
    Ok(())
}

pub fn hide_all(app: &AppHandle) {
    for (label, w) in app.webview_windows() {
        if label.starts_with(LABEL_PREFIX) {
            let _ = w.hide();
        }
    }
}

/// 应用启动时为当前所有显示器预先创建窗口，避免第一次按快捷键时等待 WebView 冷启动。
pub fn prewarm(app: &AppHandle) {
    let count = app.available_monitors().map(|m| m.len()).unwrap_or(1).max(1);
    for i in 0..count {
        if let Err(e) = ensure(app, i) {
            log::warn!("prewarm selector {i}: {e}");
        }
    }
}
