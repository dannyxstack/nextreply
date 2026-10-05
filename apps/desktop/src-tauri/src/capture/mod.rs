//! 截图：先截取所有显示器到内存（冻结画面），再在冻结画面上框选、裁剪。
//! 截图数据只存在内存中，不写入磁盘。

pub mod crop;
pub mod frames;

use std::sync::Arc;

use image::RgbaImage;

use crate::geom::Rect;

pub struct MonitorFrame {
    /// 全局虚拟桌面中的物理像素区域
    pub bounds: Rect,
    pub image: Arc<RgbaImage>,
}

pub trait ScreenCapturer {
    fn capture_all(&self) -> Result<Vec<MonitorFrame>, String>;
}

/// 基于 xcap 的实现：Windows 下走 DXGI/GDI，macOS 下走 ScreenCaptureKit。
pub struct XcapCapturer;

impl ScreenCapturer for XcapCapturer {
    fn capture_all(&self) -> Result<Vec<MonitorFrame>, String> {
        let monitors = xcap::Monitor::all().map_err(|e| format!("enumerate monitors: {e}"))?;
        let mut frames = Vec::with_capacity(monitors.len());
        for m in monitors {
            let image = m.capture_image().map_err(|e| format!("capture monitor: {e}"))?;
            let x = m.x().map_err(|e| e.to_string())?;
            let y = m.y().map_err(|e| e.to_string())?;
            // TODO(macOS): xcap 在 mac 上返回的是逻辑坐标，届时需要乘以 m.scale_factor() 换算成物理坐标
            let bounds = Rect::new(x, y, image.width() as i32, image.height() as i32);
            frames.push(MonitorFrame { bounds, image: Arc::new(image) });
        }
        if frames.is_empty() {
            return Err("no monitors found".into());
        }
        Ok(frames)
    }
}
