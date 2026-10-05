//! 流程状态机：整个"快捷键 → 框选 → AI → overlay → 复制"流程的唯一入口和出口。
//!
//! ```text
//! Idle ─hotkey─► Selecting ─mouseup─► Analyzing ─ok/err─► Showing ─click─► Copied ─► Idle
//!                   │ESC                  │ESC                │ESC/失焦
//!                   └──────────────────► Idle ◄──────────────┘
//! ```
//!
//! 每次流程有一个 session 编号；异步结果回来时如果 session 已变化就直接丢弃。

use std::time::{Duration, Instant};

use serde::Serialize;
use tauri::{async_runtime::JoinHandle, AppHandle, Emitter, Manager};
use tauri_plugin_clipboard_manager::ClipboardExt;

use crate::{
    ai::{
        proxy::ProxyProvider,
        types::{AiErrorCode, ImagePayload, ReplyResult, ReplySuggestion, UserContext},
        AiProvider,
    },
    capture::{
        self,
        crop::{self, CssRect},
        ScreenCapturer, XcapCapturer,
    },
    geom::Rect,
    hotkey, overlay, platform, selector,
    state::AppState,
};

/// 发给 AI 的截图长边上限和 JPEG 质量
const MAX_LONG_EDGE: u32 = 1568;
const JPEG_QUALITY: u8 = 90;
/// 小于这个尺寸（物理像素）的选区视为误点
const MIN_SELECTION: i32 = 24;
const COPIED_FEEDBACK: Duration = Duration::from_millis(700);
/// 等 ESC 松开的最长时间，以及切回焦点前后各保持拦截的时长
const ESC_RELEASE_TIMEOUT: Duration = Duration::from_secs(2);
const ESC_GRACE: Duration = Duration::from_millis(120);

pub const EVT_SELECTOR_FRAME: &str = "selector:frame";
pub const EVT_OVERLAY_STATE: &str = "overlay:state";

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Phase {
    #[default]
    Idle,
    Selecting,
    Analyzing,
    Showing,
    Copied,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CloseReason {
    Escape,
    Blur,
    Restart,
    Copied,
}

impl CloseReason {
    fn as_str(self) -> &'static str {
        match self {
            CloseReason::Escape => "escape",
            CloseReason::Blur => "blur",
            CloseReason::Restart => "restart",
            CloseReason::Copied => "copied",
        }
    }
}

/// overlay 页面渲染所需的状态（事件推送 + 页面加载时主动拉取）。
#[derive(Clone, Debug, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum OverlayPayload {
    Loading { session: u64 },
    Result { session: u64, summary: String, replies: Vec<ReplySuggestion> },
    Error { session: u64, code: AiErrorCode },
    Copied { session: u64 },
}

#[derive(Clone, Copy, Debug, Serialize)]
pub struct SelectorFrame {
    pub session: u64,
    pub monitor: usize,
}

#[derive(Default)]
pub struct Flow {
    pub phase: Phase,
    pub session: u64,
    prev_foreground: Option<isize>,
    started: Option<Instant>,
    task: Option<JoinHandle<()>>,
    selection: Option<Rect>,
    result: Option<ReplyResult>,
    pub overlay: Option<OverlayPayload>,
    overlay_height: Option<f64>,
}

fn emit_overlay(app: &AppHandle, payload: OverlayPayload) {
    app.state::<AppState>().flow.lock().unwrap().overlay = Some(payload.clone());
    let _ = app.emit_to(overlay::LABEL, EVT_OVERLAY_STATE, payload);
}

fn elapsed_ms(since: Option<Instant>) -> Option<u128> {
    since.map(|t| t.elapsed().as_millis())
}

/// 快捷键触发：截取所有显示器 → 显示框选窗口。
pub fn start(app: &AppHandle) {
    // 必须在我们的窗口获取焦点前记录前台窗口
    let prev_foreground = platform::foreground_window();
    cancel(app, CloseReason::Restart);

    let state = app.state::<AppState>();
    let session = state.next_session();
    let started = Instant::now();
    state.telemetry.track("capture_started", session, None, None);

    let frames = match XcapCapturer.capture_all() {
        Ok(f) => f,
        Err(e) => {
            log::error!("capture failed: {e}");
            state.telemetry.track("capture_failed", session, None, Some("capture"));
            return;
        }
    };
    log::debug!("capture done in {:?}", started.elapsed());
    // 立即在后台准备显示用的编码，和窗口定位并行，selector 请求时直接取缓存
    for f in &frames {
        let (image, display) = (f.image.clone(), f.display.clone());
        tauri::async_runtime::spawn_blocking(move || {
            display.get_or_init(|| std::sync::Arc::new(capture::frames::encode_bmp(&image)));
        });
    }
    let bounds: Vec<Rect> = frames.iter().map(|f| f.bounds).collect();
    {
        let mut store = state.frames.lock().unwrap();
        store.session = session;
        store.frames = frames;
    }
    {
        let mut flow = state.flow.lock().unwrap();
        *flow = Flow { phase: Phase::Selecting, session, prev_foreground, started: Some(started), ..Default::default() };
    }
    hotkey::set_escape(app, true);

    for (i, b) in bounds.iter().enumerate() {
        match selector::ensure(app, i) {
            Ok(w) => {
                if let Err(e) = selector::cover(&w, *b) {
                    log::warn!("position selector {i}: {e}");
                }
                let _ = app.emit_to(selector::label(i).as_str(), EVT_SELECTOR_FRAME, SelectorFrame { session, monitor: i });
            }
            Err(e) => log::error!("create selector {i}: {e}"),
        }
    }
    // 显示器数量减少时，多余的窗口保持隐藏
    for (label, w) in app.webview_windows() {
        if let Some(i) = label.strip_prefix(selector::LABEL_PREFIX).and_then(|s| s.parse::<usize>().ok()) {
            if i >= bounds.len() {
                let _ = w.hide();
            }
        }
    }
}

/// selector 页面加载时主动拉取当前帧（避免错过 `selector:frame` 事件）。
pub fn current_selector_frame(app: &AppHandle, monitor: usize) -> Option<SelectorFrame> {
    let state = app.state::<AppState>();
    let flow = state.flow.lock().unwrap();
    let store = state.frames.lock().unwrap();
    (flow.phase == Phase::Selecting && store.session == flow.session && monitor < store.frames.len())
        .then_some(SelectorFrame { session: flow.session, monitor })
}

/// 冻结画面加载完成后再显示窗口，避免闪出上一次的旧画面。
/// `loaded` 是页面实际加载的帧编号，必须和窗口所在显示器一致，否则会显示错屏的画面。
pub fn on_selector_ready(app: &AppHandle, monitor: usize, session: u64, loaded: usize) {
    if loaded != monitor {
        log::error!("selector {monitor} loaded frame of monitor {loaded}, refusing to show");
        return;
    }
    let state = app.state::<AppState>();
    {
        let flow = state.flow.lock().unwrap();
        if flow.phase != Phase::Selecting || flow.session != session {
            return;
        }
    }
    let bounds = state.frames.lock().unwrap().frames.get(monitor).map(|f| f.bounds);
    let (Some(bounds), Some(win)) = (bounds, app.get_webview_window(&selector::label(monitor))) else {
        return;
    };
    let _ = win.show();
    log::debug!("selector {monitor} shown {:?} after hotkey", state.flow.lock().unwrap().started.map(|t| t.elapsed()));
    let under_cursor = app
        .cursor_position()
        .map(|p| bounds.contains_point(p.x as i32, p.y as i32))
        .unwrap_or(monitor == 0);
    if under_cursor {
        let _ = win.set_focus();
    }
}

/// 用户松开鼠标：裁剪 → 显示 Loading → 后台请求 AI。
pub fn on_selection(app: &AppHandle, monitor: usize, session: u64, rect: CssRect, viewport_w: f64, viewport_h: f64) {
    let state = app.state::<AppState>();
    {
        let flow = state.flow.lock().unwrap();
        if flow.phase != Phase::Selecting || flow.session != session {
            return;
        }
    }

    let picked = {
        let mut store = state.frames.lock().unwrap();
        let picked = store.frames.get(monitor).map(|f| (f.image.clone(), f.bounds));
        store.clear(); // 整屏帧用完即释放
        picked
    };
    let Some((image, bounds)) = picked else {
        return cancel(app, CloseReason::Escape);
    };

    let local = crop::css_to_physical(rect, viewport_w, viewport_h, image.width(), image.height());
    if local.w < MIN_SELECTION || local.h < MIN_SELECTION {
        return cancel(app, CloseReason::Escape);
    }
    let global = local.offset(bounds.x, bounds.y);

    let (started, overlay_height) = {
        let mut flow = state.flow.lock().unwrap();
        flow.phase = Phase::Analyzing;
        flow.selection = Some(global);
        (flow.started, flow.overlay_height)
    };
    state.telemetry.track("capture_completed", session, elapsed_ms(started), None);

    // 先显示 overlay 并获取焦点，再隐藏框选窗口（否则 Windows 可能拒绝把焦点给 overlay）
    emit_overlay(app, OverlayPayload::Loading { session });
    match overlay::place(app, global, overlay_height.unwrap_or(overlay::INITIAL_HEIGHT)) {
        Ok(win) => {
            let _ = win.show();
            let _ = win.set_focus();
        }
        Err(e) => log::error!("show overlay: {e}"),
    }
    selector::hide_all(app);

    let provider = {
        let s = state.settings.lock().unwrap();
        ProxyProvider {
            http: state.http.clone(),
            base_url: s.server_url.clone(),
            device_id: s.device_id.clone(),
            tokens: state.tokens.clone(),
        }
    };
    let ctx = {
        let s = state.settings.lock().unwrap();
        UserContext {
            display_name: Some(s.display_name.trim().to_string()).filter(|n| !n.is_empty()),
            locale: sys_locale::get_locale(),
        }
    };

    let app_for_task = app.clone();
    let task = tauri::async_runtime::spawn(async move {
        let app = app_for_task;
        let encoded = tauri::async_runtime::spawn_blocking(move || crop::crop_and_encode(&image, local, MAX_LONG_EDGE, JPEG_QUALITY)).await;
        let jpeg = match encoded {
            Ok(Ok(j)) => j,
            other => {
                log::error!("encode failed: {:?}", other.err().map(|e| e.to_string()));
                return on_result(&app, session, Err(AiErrorCode::Unavailable), None);
            }
        };

        let req_started = Instant::now();
        app.state::<AppState>().telemetry.track("ai_request_started", session, None, None);
        let result = provider.analyze_and_reply(ImagePayload { jpeg }, ctx).await;
        let result = result.map_err(|e| {
            log::warn!("ai request failed: {:?} {}", e.code, e.detail);
            e.code
        });
        on_result(&app, session, result, Some(req_started));
    });

    let mut flow = state.flow.lock().unwrap();
    if flow.session == session && flow.phase == Phase::Analyzing {
        flow.task = Some(task);
    } else {
        task.abort();
    }
}

fn on_result(app: &AppHandle, session: u64, result: Result<ReplyResult, AiErrorCode>, req_started: Option<Instant>) {
    let state = app.state::<AppState>();
    let err_code = result.as_ref().err().map(|c| format!("{c:?}"));
    state.telemetry.track("ai_request_completed", session, elapsed_ms(req_started), err_code.as_deref());

    let payload = {
        let mut flow = state.flow.lock().unwrap();
        if flow.session != session || flow.phase != Phase::Analyzing {
            return; // 已取消或已开始新流程
        }
        flow.phase = Phase::Showing;
        flow.task = None;
        match result {
            Ok(r) => {
                let payload = OverlayPayload::Result { session, summary: r.analysis.summary.clone(), replies: r.replies.clone() };
                flow.result = Some(r);
                payload
            }
            Err(code) => OverlayPayload::Error { session, code },
        }
    };
    let shown = matches!(payload, OverlayPayload::Result { .. });
    emit_overlay(app, payload);
    if shown {
        state.telemetry.track("reply_shown", session, None, None);
    }
}

/// 点击回复：复制 → 还原聊天窗口焦点 → 显示 "Copied ✓" → 关闭。
pub fn copy_reply(app: &AppHandle, index: usize) -> Result<(), String> {
    let state = app.state::<AppState>();
    let (text, session, prev_foreground, started) = {
        let mut flow = state.flow.lock().unwrap();
        if flow.phase != Phase::Showing {
            return Err("no replies to copy".into());
        }
        let text = flow
            .result
            .as_ref()
            .and_then(|r| r.replies.get(index))
            .map(|r| r.text.clone())
            .ok_or("reply index out of range")?;
        flow.phase = Phase::Copied;
        (text, flow.session, flow.prev_foreground, flow.started)
    };

    app.clipboard().write_text(text).map_err(|e| e.to_string())?;
    state.telemetry.track("reply_clicked", session, elapsed_ms(started), None);
    emit_overlay(app, OverlayPayload::Copied { session });
    // 焦点马上还给聊天软件，方便直接 Ctrl+V；全局 ESC 等 overlay 关闭后再注销，
    // 这段时间里按 ESC 仍由我们处理，不会漏给聊天软件
    if let Some(h) = prev_foreground {
        platform::restore_foreground(h);
    }

    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(COPIED_FEEDBACK).await;
        let state = app.state::<AppState>();
        let done = {
            let mut flow = state.flow.lock().unwrap();
            let done = flow.session == session && flow.phase == Phase::Copied;
            if done {
                flow.phase = Phase::Idle;
                flow.result = None;
                flow.overlay = None;
            }
            done
        };
        if done {
            overlay::hide(&app);
            hotkey::set_escape(&app, false);
            state.telemetry.track("overlay_closed", session, None, Some(CloseReason::Copied.as_str()));
        }
    });
    Ok(())
}

fn still_idle(app: &AppHandle, session: u64) -> bool {
    let state = app.state::<AppState>();
    let flow = state.flow.lock().unwrap();
    flow.session == session && flow.phase == Phase::Idle
}

/// ESC 取消后：等 ESC 松开 → 把焦点还给聊天软件 → 再保持拦截一小段时间 → 注销全局 ESC。
///
/// 如果立刻注销 ESC 并切回焦点，用户还按着的 ESC（以及按键重复）会发给聊天软件；
/// 微信、Telegram 等在前台收到 ESC 会隐藏主窗口或关闭当前会话。
fn release_escape_after_keyup(app: &AppHandle, session: u64, restore: Option<isize>) {
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        let deadline = Instant::now() + ESC_RELEASE_TIMEOUT;
        while platform::is_escape_down() && Instant::now() < deadline {
            tokio::time::sleep(Duration::from_millis(15)).await;
        }
        tokio::time::sleep(ESC_GRACE).await;
        // 期间已开始新流程：ESC 和焦点归新流程管
        if !still_idle(&app, session) {
            return;
        }
        if let Some(h) = restore {
            platform::restore_foreground(h);
        }
        tokio::time::sleep(ESC_GRACE).await;
        if still_idle(&app, session) {
            hotkey::set_escape(&app, false);
        }
    });
}

/// 取消 / 关闭整个流程。任何状态下调用都是安全的。
pub fn cancel(app: &AppHandle, reason: CloseReason) {
    let state = app.state::<AppState>();
    let (prev_phase, session, task, prev_foreground) = {
        let mut flow = state.flow.lock().unwrap();
        let prev = flow.phase;
        flow.phase = Phase::Idle;
        flow.result = None;
        flow.overlay = None;
        flow.selection = None;
        (prev, flow.session, flow.task.take(), flow.prev_foreground)
    };
    if let Some(t) = task {
        t.abort();
    }
    state.frames.lock().unwrap().clear();
    selector::hide_all(app);
    overlay::hide(app);

    if prev_phase == Phase::Idle {
        // 空闲时收到的 ESC 是取消后残留的按键：直接吞掉，由 release_escape_after_keyup 负责注销
        if !matches!(reason, CloseReason::Restart | CloseReason::Escape) {
            hotkey::set_escape(app, false);
        }
        return;
    }
    if matches!(prev_phase, Phase::Analyzing | Phase::Showing) {
        state.telemetry.track("overlay_closed", session, None, Some(reason.as_str()));
    }
    match reason {
        // ESC 取消：等按键松开后再还焦点、再注销 ESC
        CloseReason::Escape => release_escape_after_keyup(app, session, prev_foreground),
        // 新流程会立即重新接管 ESC
        CloseReason::Restart => {}
        // 失焦关闭说明用户已经点到别处，不抢焦点
        CloseReason::Blur | CloseReason::Copied => hotkey::set_escape(app, false),
    }
}

/// overlay 失去焦点：只在展示结果 / 错误时关闭；Loading 期间用户切走不打断请求。
pub fn on_overlay_blur(app: &AppHandle) {
    let phase = app.state::<AppState>().flow.lock().unwrap().phase;
    if phase == Phase::Showing {
        cancel(app, CloseReason::Blur);
    }
}

/// 前端测量到内容高度后调整 overlay 尺寸并重新定位。
pub fn resize_overlay(app: &AppHandle, logical_height: f64) {
    let state = app.state::<AppState>();
    let selection = {
        let mut flow = state.flow.lock().unwrap();
        flow.overlay_height = Some(logical_height);
        if matches!(flow.phase, Phase::Analyzing | Phase::Showing | Phase::Copied) {
            flow.selection
        } else {
            None
        }
    };
    if let Some(sel) = selection {
        if let Err(e) = overlay::place(app, sel, logical_height) {
            log::warn!("resize overlay: {e}");
        }
    }
}
