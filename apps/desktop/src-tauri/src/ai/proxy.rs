//! 中转服务客户端：`/v1/reply`。凭据（设备 token / 登录令牌）由 `account::Credentials` 管理。

use std::time::Duration;

use async_trait::async_trait;
use base64::Engine;
use reqwest::StatusCode;
use serde::Deserialize;
use serde_json::json;

use super::types::{AiError, AiErrorCode, ImagePayload, ReplyResult, ReplyStatus, UserContext};
use super::AiProvider;
use crate::account::{AuthError, Bearer, Credentials};

pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

pub struct ProxyProvider {
    pub creds: Credentials,
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
    #[serde(default)]
    details: Option<serde_json::Value>,
}

fn network_err(e: reqwest::Error) -> AiError {
    // 超时说明网络是通的，只是服务端太慢，按"服务不可用"提示用户重试
    let code = if e.is_timeout() { AiErrorCode::Unavailable } else { AiErrorCode::Network };
    AiError::new(code, e.to_string())
}

fn auth_err(e: AuthError) -> AiError {
    match e {
        AuthError::Network(d) => AiError::new(AiErrorCode::Network, d),
        AuthError::Server(d) => AiError::new(AiErrorCode::Unavailable, d),
    }
}

async fn map_error_response(resp: reqwest::Response) -> AiError {
    let status = resp.status();
    let body = resp.json::<ErrorBody>().await.ok().map(|b| b.error);
    let (code, message, details) = body.map(|e| (e.code, e.message, e.details)).unwrap_or_default();
    let login_required = details.as_ref().and_then(|d| d.get("login_required")).and_then(|v| v.as_bool()).unwrap_or(false);
    let mapped = match code.as_str() {
        "insufficient_credits" if login_required => AiErrorCode::LoginRequired,
        "insufficient_credits" => AiErrorCode::InsufficientCredits,
        "daily_cap" => AiErrorCode::DailyCap,
        "rate_limited" => AiErrorCode::RateLimited,
        "quota_exceeded" => AiErrorCode::QuotaExceeded,
        _ if status == StatusCode::TOO_MANY_REQUESTS => AiErrorCode::RateLimited,
        _ => AiErrorCode::Unavailable,
    };
    AiError::new(mapped, format!("{status} {code} {message}"))
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
        // 同一次分析的重试共用一个幂等键，服务端不会重复扣费
        let idempotency_key = uuid::Uuid::new_v4().simple().to_string();

        let mut retried = false;
        let resp = loop {
            let bearer = self.creds.bearer().await.map_err(auth_err)?;
            let resp = self
                .creds
                .http
                .post(self.creds.url("/v1/reply"))
                .bearer_auth(bearer.token())
                .header("idempotency-key", &idempotency_key)
                .json(&body)
                .timeout(REQUEST_TIMEOUT)
                .send()
                .await
                .map_err(network_err)?;
            if resp.status() == StatusCode::UNAUTHORIZED && !retried {
                // 令牌失效：登录令牌则刷新，设备 token 则重新注册，然后重试一次
                retried = true;
                match bearer {
                    Bearer::User(_) => self.creds.invalidate_access(),
                    Bearer::Device(_) => self.creds.device_tokens.clear(),
                }
                continue;
            }
            break resp;
        };
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
