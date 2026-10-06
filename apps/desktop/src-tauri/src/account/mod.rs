//! 账号与凭据：匿名设备 token、登录后的 access / refresh token。
//!
//! - 未登录：用设备 token 调用服务端（体验额度）。
//! - 已登录：用 access token（15 分钟有效，只在内存中）；过期时用 refresh token（存在系统凭据库）换新。
//! - refresh token 失效（过期、在别处被作废）时自动回到未登录状态。

pub mod login;

use std::{
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use reqwest::StatusCode;
use serde::Deserialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use crate::{platform, storage::secrets::TokenStore};

/// access token 提前这么久视为过期，避免请求途中过期
const ACCESS_SKEW: Duration = Duration::from_secs(30);
const HTTP_TIMEOUT: Duration = Duration::from_secs(15);

#[derive(Default)]
pub struct Session {
    access: Option<(String, Instant)>,
}

/// 调用服务端所需的一切；从 AppState 构造，克隆成本很低（Session 是共享的）。
#[derive(Clone)]
pub struct Credentials {
    pub http: reqwest::Client,
    pub base_url: String,
    pub device_id: String,
    pub device_tokens: TokenStore,
    pub refresh_tokens: TokenStore,
    pub session: Arc<Mutex<Session>>,
}

#[derive(Debug)]
pub enum AuthError {
    Network(String),
    Server(String),
}

impl std::fmt::Display for AuthError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AuthError::Network(e) => write!(f, "Network unavailable: {e}"),
            AuthError::Server(e) => write!(f, "{e}"),
        }
    }
}

fn net(e: reqwest::Error) -> AuthError {
    AuthError::Network(e.to_string())
}

#[derive(Deserialize)]
pub struct TokenResponse {
    pub access_token: String,
    pub expires_in: u64,
    pub refresh_token: String,
}

#[derive(Deserialize)]
struct DeviceTokenResponse {
    token: String,
}

/// 发给服务端的硬件特征：加盐哈希，服务端看不到原始机器标识。
fn hw_hash() -> Option<String> {
    let id = platform::machine_id()?;
    let digest = Sha256::digest(format!("nextreply-hw-v1:{id}").as_bytes());
    Some(digest.iter().map(|b| format!("{b:02x}")).collect())
}

pub enum Bearer {
    User(String),
    Device(String),
}

impl Bearer {
    pub fn token(&self) -> &str {
        match self {
            Bearer::User(t) | Bearer::Device(t) => t,
        }
    }
}

impl Credentials {
    pub fn url(&self, path: &str) -> String {
        format!("{}{}", self.base_url.trim_end_matches('/'), path)
    }

    pub fn is_logged_in(&self) -> bool {
        self.refresh_tokens.get().is_some()
    }

    pub async fn register_device(&self) -> Result<String, AuthError> {
        let resp = self
            .http
            .post(self.url("/v1/device/register"))
            .json(&json!({ "device_id": self.device_id, "hw_hash": hw_hash() }))
            .timeout(HTTP_TIMEOUT)
            .send()
            .await
            .map_err(net)?;
        if !resp.status().is_success() {
            return Err(AuthError::Server(format!("Device registration failed ({})", resp.status())));
        }
        let token = resp.json::<DeviceTokenResponse>().await.map_err(net)?.token;
        self.device_tokens.set(&token);
        Ok(token)
    }

    pub async fn device_token(&self) -> Result<String, AuthError> {
        match self.device_tokens.get() {
            Some(t) => Ok(t),
            None => self.register_device().await,
        }
    }

    pub fn store_tokens(&self, t: &TokenResponse) {
        self.refresh_tokens.set(&t.refresh_token);
        self.session.lock().unwrap().access = Some((t.access_token.clone(), Instant::now() + Duration::from_secs(t.expires_in)));
    }

    pub fn clear_login(&self) {
        self.refresh_tokens.clear();
        self.session.lock().unwrap().access = None;
    }

    pub fn invalidate_access(&self) {
        self.session.lock().unwrap().access = None;
    }

    /// 有效的 access token；未登录或登录已失效返回 None。
    pub async fn access_token(&self) -> Result<Option<String>, AuthError> {
        if let Some((t, exp)) = self.session.lock().unwrap().access.clone() {
            if exp > Instant::now() + ACCESS_SKEW {
                return Ok(Some(t));
            }
        }
        let Some(refresh) = self.refresh_tokens.get() else { return Ok(None) };
        let resp = self
            .http
            .post(self.url("/v1/auth/refresh"))
            .json(&json!({ "refresh_token": refresh, "device_id": self.device_id }))
            .timeout(HTTP_TIMEOUT)
            .send()
            .await
            .map_err(net)?;
        if resp.status() == StatusCode::UNAUTHORIZED {
            log::info!("refresh token rejected, signing out");
            self.clear_login();
            return Ok(None);
        }
        if !resp.status().is_success() {
            return Err(AuthError::Server(format!("Couldn't refresh your session ({})", resp.status())));
        }
        let t = resp.json::<TokenResponse>().await.map_err(net)?;
        self.store_tokens(&t);
        Ok(Some(t.access_token))
    }

    /// 当前请求应使用的凭据：已登录用 access token，否则用设备 token。
    pub async fn bearer(&self) -> Result<Bearer, AuthError> {
        match self.access_token().await? {
            Some(t) => Ok(Bearer::User(t)),
            None => Ok(Bearer::Device(self.device_token().await?)),
        }
    }

    /// 带凭据发送 GET/POST；401 时刷新凭据重试一次。
    pub async fn send_authed(&self, method: reqwest::Method, path: &str, body: Option<&Value>) -> Result<reqwest::Response, AuthError> {
        let mut retried = false;
        loop {
            let bearer = self.bearer().await?;
            let mut req = self.http.request(method.clone(), self.url(path)).bearer_auth(bearer.token()).timeout(HTTP_TIMEOUT);
            if let Some(b) = body {
                req = req.json(b);
            }
            let resp = req.send().await.map_err(net)?;
            if resp.status() == StatusCode::UNAUTHORIZED && !retried {
                retried = true;
                match bearer {
                    Bearer::User(_) => self.invalidate_access(),
                    Bearer::Device(_) => self.device_tokens.clear(),
                }
                continue;
            }
            return Ok(resp);
        }
    }

    /// 账户信息（/v1/me），原样交给设置页渲染。
    pub async fn me(&self) -> Result<Value, AuthError> {
        let resp = self.send_authed(reqwest::Method::GET, "/v1/me", None).await?;
        if !resp.status().is_success() {
            return Err(AuthError::Server(format!("Couldn't load account info ({})", resp.status())));
        }
        resp.json::<Value>().await.map_err(net)
    }

    /// 申请结账 / 管理订阅的一次性网页链接。
    pub async fn billing_link(&self, purpose: &str, plan: Option<&str>) -> Result<String, AuthError> {
        let resp = self
            .send_authed(reqwest::Method::POST, "/v1/billing/link", Some(&json!({ "purpose": purpose, "plan": plan })))
            .await?;
        if resp.status() == StatusCode::UNAUTHORIZED {
            return Err(AuthError::Server("Please sign in first".into()));
        }
        if !resp.status().is_success() {
            return Err(AuthError::Server(format!("Couldn't open the checkout page ({})", resp.status())));
        }
        let v = resp.json::<Value>().await.map_err(net)?;
        v.get("url").and_then(|u| u.as_str()).map(str::to_string).ok_or_else(|| AuthError::Server("Unexpected server response".into()))
    }

    pub async fn logout(&self) {
        if let Some(refresh) = self.refresh_tokens.get() {
            let _ = self
                .http
                .post(self.url("/v1/auth/logout"))
                .json(&json!({ "refresh_token": refresh }))
                .timeout(HTTP_TIMEOUT)
                .send()
                .await;
        }
        self.clear_login();
    }
}
