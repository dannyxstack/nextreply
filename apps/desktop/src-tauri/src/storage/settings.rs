use std::{fs, path::Path};

use serde::{Deserialize, Serialize};

/// 构建时可通过 `NEXTREPLY_SERVER_URL` 环境变量指定默认服务端地址。
pub const DEFAULT_SERVER_URL: &str = match option_env!("NEXTREPLY_SERVER_URL") {
    Some(url) => url,
    None => "http://127.0.0.1:8787",
};

pub const DEFAULT_SHORTCUT: &str = "CommandOrControl+Shift+R";

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    pub shortcut: String,
    pub server_url: String,
    pub display_name: String,
    pub launch_at_login: bool,
    /// 匿名设备 ID，用于中转服务的额度统计
    pub device_id: String,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            shortcut: DEFAULT_SHORTCUT.into(),
            server_url: DEFAULT_SERVER_URL.into(),
            display_name: String::new(),
            launch_at_login: false,
            device_id: uuid::Uuid::new_v4().to_string(),
        }
    }
}

impl Settings {
    /// 读取设置；返回 (设置, 是否首次运行)。
    pub fn load(path: &Path) -> (Settings, bool) {
        match fs::read_to_string(path) {
            Ok(text) => {
                let mut s: Settings = serde_json::from_str(&text).unwrap_or_default();
                if s.device_id.is_empty() {
                    s.device_id = uuid::Uuid::new_v4().to_string();
                }
                (s, false)
            }
            Err(_) => (Settings::default(), true),
        }
    }

    pub fn save(&self, path: &Path) -> Result<(), String> {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let text = serde_json::to_string_pretty(self).map_err(|e| e.to_string())?;
        fs::write(path, text).map_err(|e| e.to_string())
    }
}
