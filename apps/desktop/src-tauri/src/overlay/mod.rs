//! 回复卡片窗口：预先创建、隐藏复用；透明、置顶、不出现在任务栏、不被截图捕获。

pub mod positioning;

use tauri::{AppHandle, Manager, PhysicalPosition, PhysicalSize, WebviewUrl, WebviewWindow, WebviewWindowBuilder};

use crate::geom::Rect;

pub const LABEL: &str = "overlay";
pub const WIDTH: f64 = 360.0;
pub const INITIAL_HEIGHT: f64 = 140.0;
const GAP: i32 = 12;

pub fn ensure(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    if let Some(w) = app.get_webview_window(LABEL) {
        return Ok(w);
    }
    WebviewWindowBuilder::new(app, LABEL, WebviewUrl::App("overlay.html".into()))
        .title("NextReply")
        .inner_size(WIDTH, INITIAL_HEIGHT)
        .decorations(false)
        .transparent(true)
        .shadow(false)
        .resizable(false)
        .always_on_top(true)
        .skip_taskbar(true)
        .visible(false)
        .focused(false)
        .content_protected(true)
        .visible_on_all_workspaces(true)
        .build()
}

/// 找到包含该点的显示器，返回 (工作区, 缩放比例)。
fn monitor_for(app: &AppHandle, x: i32, y: i32) -> Option<(Rect, f64)> {
    let monitors = app.available_monitors().ok()?;
    let pick = monitors
        .iter()
        .find(|m| {
            let p = m.position();
            let s = m.size();
            Rect::new(p.x, p.y, s.width as i32, s.height as i32).contains_point(x, y)
        })
        .or_else(|| monitors.first())?;
    let wa = pick.work_area();
    Some((
        Rect::new(wa.position.x, wa.position.y, wa.size.width as i32, wa.size.height as i32),
        pick.scale_factor(),
    ))
}

/// 按选区放置 overlay。`logical_height` 为前端测量的内容高度（CSS 像素）。
pub fn place(app: &AppHandle, selection: Rect, logical_height: f64) -> tauri::Result<WebviewWindow> {
    let win = ensure(app)?;
    let (cx, cy) = selection.center();
    let (work_area, scale) = monitor_for(app, cx, cy).unwrap_or((Rect::new(0, 0, 1920, 1080), 1.0));
    let size = ((WIDTH * scale).round() as i32, (logical_height * scale).round() as i32);
    let (x, y) = positioning::place_overlay(selection, size, work_area, (GAP as f64 * scale) as i32);
    // 先定位再设尺寸、再定位一次：跨 DPI 显示器移动时系统可能会自动缩放窗口
    win.set_position(PhysicalPosition::new(x, y))?;
    win.set_size(PhysicalSize::new(size.0 as u32, size.1 as u32))?;
    win.set_position(PhysicalPosition::new(x, y))?;
    Ok(win)
}

pub fn hide(app: &AppHandle) {
    if let Some(w) = app.get_webview_window(LABEL) {
        let _ = w.hide();
    }
}
