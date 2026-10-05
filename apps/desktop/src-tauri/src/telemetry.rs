//! 本地埋点：追加写入 `<app_data>/telemetry/events.jsonl`。只记录事件、时间和错误码，不记录任何内容。

use std::{
    fs::{self, OpenOptions},
    io::Write,
    path::PathBuf,
    sync::Mutex,
    time::{SystemTime, UNIX_EPOCH},
};

use serde_json::json;

pub struct Telemetry {
    path: PathBuf,
    lock: Mutex<()>,
}

impl Telemetry {
    pub fn new(dir: PathBuf) -> Self {
        let _ = fs::create_dir_all(&dir);
        Self { path: dir.join("events.jsonl"), lock: Mutex::new(()) }
    }

    pub fn track(&self, event: &str, session: u64, duration_ms: Option<u128>, error_code: Option<&str>) {
        let ts = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0);
        let line = json!({
            "event": event,
            "ts": ts,
            "session_id": session,
            "duration_ms": duration_ms,
            "error_code": error_code,
        });
        log::info!("telemetry {line}");
        let _guard = self.lock.lock().unwrap();
        if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(&self.path) {
            let _ = writeln!(f, "{line}");
        }
    }
}
