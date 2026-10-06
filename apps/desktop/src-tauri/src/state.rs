use std::{
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc, Mutex,
    },
};

use crate::{
    account::{Credentials, Session},
    capture::frames::FrameStore,
    flow::Flow,
    hotkey::HotkeyState,
    storage::{secrets::TokenStore, settings::Settings},
    telemetry::Telemetry,
};

/// 全局状态。各个 Mutex 只在同步代码中短暂持有，不跨 await。
pub struct AppState {
    pub flow: Mutex<Flow>,
    pub frames: Mutex<FrameStore>,
    pub settings: Mutex<Settings>,
    pub settings_path: PathBuf,
    pub hotkeys: Mutex<HotkeyState>,
    pub telemetry: Telemetry,
    pub http: reqwest::Client,
    pub device_tokens: TokenStore,
    pub refresh_tokens: TokenStore,
    pub session: Arc<Mutex<Session>>,
    /// 同一时间只允许一个浏览器登录流程
    pub login_in_progress: AtomicBool,
    session_counter: AtomicU64,
}

impl AppState {
    pub fn new(settings: Settings, settings_path: PathBuf, data_dir: PathBuf) -> Self {
        Self {
            flow: Mutex::new(Flow::default()),
            frames: Mutex::new(FrameStore::default()),
            settings: Mutex::new(settings),
            settings_path,
            hotkeys: Mutex::new(HotkeyState::default()),
            telemetry: Telemetry::new(data_dir.join("telemetry")),
            http: reqwest::Client::builder()
                .user_agent(concat!("NextReply/", env!("CARGO_PKG_VERSION")))
                .build()
                .expect("http client"),
            device_tokens: TokenStore::new("device-token", &data_dir),
            refresh_tokens: TokenStore::new("refresh-token", &data_dir),
            session: Arc::new(Mutex::new(Session::default())),
            login_in_progress: AtomicBool::new(false),
            session_counter: AtomicU64::new(0),
        }
    }

    pub fn next_session(&self) -> u64 {
        self.session_counter.fetch_add(1, Ordering::SeqCst) + 1
    }

    /// 按当前设置（服务端地址、设备 ID）构造调用服务端的凭据。
    pub fn credentials(&self) -> Credentials {
        let s = self.settings.lock().unwrap();
        Credentials {
            http: self.http.clone(),
            base_url: s.server_url.clone(),
            device_id: s.device_id.clone(),
            device_tokens: self.device_tokens.clone(),
            refresh_tokens: self.refresh_tokens.clone(),
            session: self.session.clone(),
        }
    }
}
