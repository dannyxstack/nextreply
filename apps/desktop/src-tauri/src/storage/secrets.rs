//! 令牌存储：优先系统凭据库（Windows 凭据管理器 / macOS Keychain），失败时退回到应用数据目录下的文件。

use std::{fs, path::PathBuf};

const SERVICE: &str = "app.nextreply.desktop";

#[derive(Clone)]
pub struct TokenStore {
    /// 凭据库里的账户名，如 "device-token"、"refresh-token"
    account: &'static str,
    fallback_path: PathBuf,
}

impl TokenStore {
    pub fn new(account: &'static str, data_dir: &std::path::Path) -> Self {
        Self { account, fallback_path: data_dir.join(account) }
    }

    fn entry(&self) -> Option<keyring::Entry> {
        keyring::Entry::new(SERVICE, self.account).ok()
    }

    pub fn get(&self) -> Option<String> {
        if let Some(t) = self.entry().and_then(|e| e.get_password().ok()) {
            return Some(t);
        }
        fs::read_to_string(&self.fallback_path).ok().map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
    }

    pub fn set(&self, token: &str) {
        if self.entry().map(|e| e.set_password(token).is_ok()).unwrap_or(false) {
            return;
        }
        log::warn!("keyring unavailable, storing {} in app data dir", self.account);
        if let Some(dir) = self.fallback_path.parent() {
            let _ = fs::create_dir_all(dir);
        }
        let _ = fs::write(&self.fallback_path, token);
    }

    pub fn clear(&self) {
        if let Some(e) = self.entry() {
            let _ = e.delete_credential();
        }
        let _ = fs::remove_file(&self.fallback_path);
    }
}
