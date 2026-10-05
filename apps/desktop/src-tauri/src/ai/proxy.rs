//! 中转服务客户端：设备注册 + `/v1/reply`。

use std::time::Duration;

use async_trait::async_trait;
use base64::Engine;
use reqwest::StatusCode;
use serde::Deserialize;
use serde_json::json;

use super::types::{AiError, AiErrorCode, ImagePayload, ReplyResult, ReplyStatus, UserContext};
use super::AiProvider;
use crate::storage::secrets::TokenStore;

pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

pub struct ProxyProvider {
    pub http: reqwest::Client,
    pub base_url: String,
    pub device_id: String,
    pub tokens: TokenStore,
}

#[derive(Deserialize)]
struct TokenResponse {
    token: String,
}

#[derive(Deserialize)]
struct ErrorBody {
    error: ErrorInner,
}

#[derive(Deserialize)]
struct ErrorInner {
    code: String,
    #[serde(default)]
    message: String,
}

fn network_err(e: reqwest::Error) -> AiError {
    // 超时说明网络是通的，只是服务端太慢，按"服务不可用"提示用户重试
    let code = if e.is_timeout() { AiErrorCode::Unavailable } else { AiErrorCode::Network };
    AiError::new(code, e.to_string())
}

impl ProxyProvider {
    fn url(&self, path: &str) -> String {
        format!("{}{}", self.base_url.trim_end_matches('/'), path)
    }

    async fn register(&self) -> Result<String, AiError> {
        let resp = self
            .http
            .post(self.url("/v1/device/register"))
            .json(&json!({ "device_id": self.device_id }))
            .timeout(Duration::from_secs(10))
            .send()
            .await
            .map_err(network_err)?;
        if !resp.status().is_success() {
            return Err(map_error_response(resp).await);
        }
        let token = resp.json::<TokenResponse>().await.map_err(network_err)?.token;
        self.tokens.set(&token);
        Ok(token)
    }

    async fn token(&self) -> Result<String, AiError> {
        match self.tokens.get() {
            Some(t) => Ok(t),
            None => self.register().await,
        }
    }

    async fn post_reply(&self, token: &str, body: &serde_json::Value) -> Result<reqwest::Response, AiError> {
        self.http
            .post(self.url("/v1/reply"))
            .bearer_auth(token)
            .json(body)
            .timeout(REQUEST_TIMEOUT)
            .send()
            .await
            .map_err(network_err)
    }
}

async fn map_error_response(resp: reqwest::Response) -> AiError {
    let status = resp.status();
    let body = resp.json::<ErrorBody>().await.ok();
    let (code, message) = body.map(|b| (b.error.code, b.error.message)).unwrap_or_default();
    match (status, code.as_str()) {
        (StatusCode::TOO_MANY_REQUESTS, _) | (_, "quota_exceeded") => AiError::new(AiErrorCode::QuotaExceeded, message),
        _ => AiError::new(AiErrorCode::Unavailable, format!("{status} {code} {message}")),
    }
}

#[async_trait]
impl AiProvider for ProxyProvider {
    async fn analyze_and_reply(&self, image: ImagePayload, ctx: UserContext) -> Result<ReplyResult, AiError> {
        let body = json!({
            "image": base64::engine::general_purpose::STANDARD.encode(&image.jpeg),
            "media_type": "image/jpeg",
            "locale": ctx.locale,
            "display_name": ctx.display_name,
            "client_version": env!("CARGO_PKG_VERSION"),
        });
        drop(image);

        let mut token = self.token().await?;
        let mut resp = self.post_reply(&token, &body).await?;
        if resp.status() == StatusCode::UNAUTHORIZED {
            // token 失效（例如服务端换了密钥）：重新注册后重试一次
            self.tokens.clear();
            token = self.register().await?;
            resp = self.post_reply(&token, &body).await?;
        }
        if !resp.status().is_success() {
            return Err(map_error_response(resp).await);
        }

        let result = resp
            .json::<ReplyResult>()
            .await
            .map_err(|e| AiError::new(AiErrorCode::Unavailable, format!("bad response: {e}")))?;
        match result.status {
            ReplyStatus::Ok if !result.replies.is_empty() => Ok(result),
            ReplyStatus::Ok => Err(AiError::new(AiErrorCode::Unavailable, "empty replies")),
            ReplyStatus::InsufficientContext => Err(AiError::new(AiErrorCode::InsufficientContext, "")),
            ReplyStatus::NotAConversation => Err(AiError::new(AiErrorCode::NotAConversation, "")),
        }
    }
}
