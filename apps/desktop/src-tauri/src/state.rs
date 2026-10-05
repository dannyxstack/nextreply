use std::{
    path::PathBuf,
    sync::{
        atomic::{AtomicU64, Ordering},
        Mutex,
    },
};

use crate::{
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
    pub tokens: TokenStore,
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
            tokens: TokenStore::new(data_dir.join("device-token")),
            session_counter: AtomicU64::new(0),
        }
    }

    pub fn next_session(&self) -> u64 {
        self.session_counter.fetch_add(1, Ordering::SeqCst) + 1
    }
}
