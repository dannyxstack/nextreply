//! 冻结画面存储 + `frame://` 自定义协议：把内存中的整屏截图直接交给 selector WebView 显示，不落盘。

use std::io::Cursor;

use image::{buffer::ConvertBuffer, codecs::jpeg::JpegEncoder, RgbImage};
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

fn respond_status(responder: UriSchemeResponder, status: StatusCode) {
    responder.respond(Response::builder().status(status).body(Vec::new()).unwrap());
}

/// URL 形如 `frame://localhost/<monitor>?s=<session>`（Windows 上为 `http://frame.localhost/<monitor>?s=..`）。
pub fn protocol<R: Runtime>(ctx: UriSchemeContext<'_, R>, request: Request<Vec<u8>>, responder: UriSchemeResponder) {
    let app = ctx.app_handle().clone();
    let index: Option<usize> = request.uri().path().trim_start_matches('/').parse().ok();

    let image = index.and_then(|i| {
        let state = app.state::<AppState>();
        let store = state.frames.lock().unwrap();
        store.frames.get(i).map(|f| f.image.clone())
    });
    let Some(image) = image else {
        return respond_status(responder, StatusCode::NOT_FOUND);
    };

    // JPEG 编码放到阻塞线程，避免卡住 IPC
    tauri::async_runtime::spawn_blocking(move || {
        // 只用于显示，质量 85 足够；裁剪始终使用原始 RGBA
        let rgb: RgbImage = image.convert();
        drop(image);
        let mut buf = Cursor::new(Vec::new());
        match JpegEncoder::new_with_quality(&mut buf, 85).encode_image(&rgb) {
            Ok(()) => responder.respond(
                Response::builder()
                    .status(StatusCode::OK)
                    .header(header::CONTENT_TYPE, "image/jpeg")
                    .header(header::CACHE_CONTROL, "no-store")
                    .header(header::ACCESS_CONTROL_ALLOW_ORIGIN, "*")
                    .body(buf.into_inner())
                    .unwrap(),
            ),
            Err(_) => respond_status(responder, StatusCode::INTERNAL_SERVER_ERROR),
        }
    });
}
