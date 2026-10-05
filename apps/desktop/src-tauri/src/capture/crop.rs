use std::io::Cursor;

use image::{codecs::jpeg::JpegEncoder, imageops::FilterType, DynamicImage, RgbaImage};
use serde::Deserialize;

use crate::geom::Rect;

/// 前端上报的选区（CSS 像素，相对 selector 窗口左上角）。
#[derive(Clone, Copy, Debug, Deserialize)]
pub struct CssRect {
    pub x: f64,
    pub y: f64,
    pub w: f64,
    pub h: f64,
}

/// 把 CSS 像素选区换算成图片内的物理像素区域，并限制在图片范围内。
/// 缩放比例用 `图片宽 / 视口宽` 计算，不依赖 devicePixelRatio，混合 DPI 下也正确。
pub fn css_to_physical(sel: CssRect, viewport_w: f64, viewport_h: f64, img_w: u32, img_h: u32) -> Rect {
    let sx = if viewport_w > 0.0 { img_w as f64 / viewport_w } else { 1.0 };
    let sy = if viewport_h > 0.0 { img_h as f64 / viewport_h } else { 1.0 };
    let x0 = (sel.x * sx).round().clamp(0.0, img_w as f64);
    let y0 = (sel.y * sy).round().clamp(0.0, img_h as f64);
    let x1 = ((sel.x + sel.w) * sx).round().clamp(0.0, img_w as f64);
    let y1 = ((sel.y + sel.h) * sy).round().clamp(0.0, img_h as f64);
    Rect::new(x0 as i32, y0 as i32, (x1 - x0).max(0.0) as i32, (y1 - y0).max(0.0) as i32)
}

/// 裁剪 → 长边缩到不超过 `max_long_edge` → JPEG 编码。全程只在内存中。
pub fn crop_and_encode(img: &RgbaImage, rect: Rect, max_long_edge: u32, quality: u8) -> Result<Vec<u8>, String> {
    if rect.w <= 0 || rect.h <= 0 {
        return Err("empty selection".into());
    }
    let cropped = image::imageops::crop_imm(img, rect.x as u32, rect.y as u32, rect.w as u32, rect.h as u32).to_image();
    let mut dyn_img = DynamicImage::ImageRgba8(cropped);

    let long_edge = dyn_img.width().max(dyn_img.height());
    if long_edge > max_long_edge {
        let ratio = max_long_edge as f64 / long_edge as f64;
        let w = ((dyn_img.width() as f64) * ratio).round().max(1.0) as u32;
        let h = ((dyn_img.height() as f64) * ratio).round().max(1.0) as u32;
        dyn_img = dyn_img.resize_exact(w, h, FilterType::Lanczos3);
    }

    let rgb = dyn_img.to_rgb8();
    let mut buf = Cursor::new(Vec::with_capacity(512 * 1024));
    JpegEncoder::new_with_quality(&mut buf, quality)
        .encode_image(&rgb)
        .map_err(|e| format!("jpeg encode: {e}"))?;
    Ok(buf.into_inner())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scales_css_to_physical_at_150_percent() {
        // 2560x1440 物理像素的屏幕，150% 缩放 → 视口 1706.67x960
        let r = css_to_physical(CssRect { x: 100.0, y: 200.0, w: 400.0, h: 300.0 }, 2560.0 / 1.5, 960.0, 2560, 1440);
        assert_eq!(r, Rect::new(150, 300, 600, 450));
    }

    #[test]
    fn clamps_to_image_bounds() {
        let r = css_to_physical(CssRect { x: -10.0, y: -10.0, w: 5000.0, h: 5000.0 }, 1920.0, 1080.0, 1920, 1080);
        assert_eq!(r, Rect::new(0, 0, 1920, 1080));
    }

    #[test]
    fn encodes_and_downscales() {
        let img = RgbaImage::from_pixel(4000, 1000, image::Rgba([200, 100, 50, 255]));
        let jpeg = crop_and_encode(&img, Rect::new(0, 0, 4000, 1000), 1568, 90).unwrap();
        let decoded = image::load_from_memory(&jpeg).unwrap();
        assert_eq!((decoded.width(), decoded.height()), (1568, 392));
    }
}
