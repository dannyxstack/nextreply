//! 设备 token 存储：优先系统凭据库（Windows 凭据管理器 / macOS Keychain），失败时退回到应用数据目录下的文件。

use std::{fs, path::PathBuf};

const SERVICE: &str = "app.nextreply.desktop";
const ACCOUNT: &str = "device-token";

#[derive(Clone)]
pub struct TokenStore {
    fallback_path: PathBuf,
}

impl TokenStore {
    pub fn new(fallback_path: PathBuf) -> Self {
        Self { fallback_path }
    }

    fn entry() -> Option<keyring::Entry> {
        keyring::Entry::new(SERVICE, ACCOUNT).ok()
    }

    pub fn get(&self) -> Option<String> {
        if let Some(t) = Self::entry().and_then(|e| e.get_password().ok()) {
            return Some(t);
        }
        fs::read_to_string(&self.fallback_path).ok().map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
    }

    pub fn set(&self, token: &str) {
        if Self::entry().map(|e| e.set_password(token).is_ok()).unwrap_or(false) {
            return;
        }
        log::warn!("keyring unavailable, storing device token in app data dir");
        if let Some(dir) = self.fallback_path.parent() {
            let _ = fs::create_dir_all(dir);
        }
        let _ = fs::write(&self.fallback_path, token);
    }

    pub fn clear(&self) {
        if let Some(e) = Self::entry() {
            let _ = e.delete_credential();
        }
        let _ = fs::remove_file(&self.fallback_path);
    }
}
