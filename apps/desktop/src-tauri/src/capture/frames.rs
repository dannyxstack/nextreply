//! 冻结画面存储 + `frame://` 自定义协议：把内存中的整屏截图直接交给 selector WebView 显示，不落盘。

use std::sync::{Arc, OnceLock};

use image::RgbaImage;
use tauri::{
    http::{header, Request, Response, StatusCode},
    Manager, Runtime, UriSchemeContext, UriSchemeResponder,
};

use super::MonitorFrame;
use crate::state::AppState;

#[derive(Default)]
pub struct FrameStore {
    pub session: u64,
    pub frames: Vec<MonitorFrame>,
}

impl FrameStore {
    pub fn clear(&mut self) {
        self.frames.clear();
        self.frames.shrink_to_fit();
    }
}

/// 32 位自上而下的 BMP。只用于显示，不压缩：编码只是一次内存拷贝（4K 屏约几十毫秒），
/// 比 JPEG 编码快一个数量级，冻结画面能更快出现。裁剪始终使用原始 RGBA。
pub fn encode_bmp(img: &RgbaImage) -> Vec<u8> {
    const HEADER: usize = 14 + 40;
    let (w, h) = img.dimensions();
    let pixels = img.as_raw();
    let mut out = Vec::with_capacity(HEADER + pixels.len());
    // BITMAPFILEHEADER
    out.extend_from_slice(b"BM");
    out.extend_from_slice(&((HEADER + pixels.len()) as u32).to_le_bytes());
    out.extend_from_slice(&0u32.to_le_bytes());
    out.extend_from_slice(&(HEADER as u32).to_le_bytes());
    // BITMAPINFOHEADER：高度取负表示自上而下，BI_RGB 32bpp
    out.extend_from_slice(&40u32.to_le_bytes());
    out.extend_from_slice(&(w as i32).to_le_bytes());
    out.extend_from_slice(&(-(h as i32)).to_le_bytes());
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&32u16.to_le_bytes());
    out.extend_from_slice(&[0u8; 24]);
    // RGBA → BGRA
    for px in pixels.chunks_exact(4) {
        out.extend_from_slice(&[px[2], px[1], px[0], 255]);
    }
    out
}

fn respond_status(responder: UriSchemeResponder, status: StatusCode) {
    responder.respond(Response::builder().status(status).body(Vec::new()).unwrap());
}

/// URL 形如 `frame://localhost/<monitor>?s=<session>`（Windows 上为 `http://frame.localhost/<monitor>?s=..`）。
pub fn protocol<R: Runtime>(ctx: UriSchemeContext<'_, R>, request: Request<Vec<u8>>, responder: UriSchemeResponder) {
    let app = ctx.app_handle().clone();
    let index: Option<usize> = request.uri().path().trim_start_matches('/').parse().ok();

    let frame = index.and_then(|i| {
        let state = app.state::<AppState>();
        let store = state.frames.lock().unwrap();
        store.frames.get(i).map(|f| (f.image.clone(), f.display.clone()))
    });
    let Some((image, display)) = frame else {
        return respond_status(responder, StatusCode::NOT_FOUND);
    };

    tauri::async_runtime::spawn_blocking(move || {
        // 同一帧可能被请求多次（例如 React 重新挂载），编码结果缓存在帧上
        let bytes = display.get_or_init(|| Arc::new(encode_bmp(&image))).clone();
        drop(image);
        responder.respond(
            Response::builder()
                .status(StatusCode::OK)
                .header(header::CONTENT_TYPE, "image/bmp")
                .header(header::CACHE_CONTROL, "no-store")
                .header(header::ACCESS_CONTROL_ALLOW_ORIGIN, "*")
                .body(bytes.to_vec())
                .unwrap(),
        );
    });
}

/// 帧的显示用编码缓存。
pub type DisplayCache = Arc<OnceLock<Arc<Vec<u8>>>>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bmp_header_and_pixel_order() {
        let img = RgbaImage::from_pixel(3, 2, image::Rgba([10, 20, 30, 255]));
        let bmp = encode_bmp(&img);
        assert_eq!(&bmp[..2], b"BM");
        assert_eq!(bmp.len(), 54 + 3 * 2 * 4);
        assert_eq!(i32::from_le_bytes(bmp[22..26].try_into().unwrap()), -2);
        assert_eq!(&bmp[54..58], &[30, 20, 10, 255]);
    }
}
