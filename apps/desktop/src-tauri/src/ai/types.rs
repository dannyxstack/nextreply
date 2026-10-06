use serde::{Deserialize, Serialize};

/// 与服务端 `/v1/reply` 的响应保持一致（见 server-go/internal/ai/schema.go）。
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ReplyResult {
    pub status: ReplyStatus,
    pub analysis: Analysis,
    pub replies: Vec<ReplySuggestion>,
    #[serde(default)]
    pub meta: Option<ReplyMeta>,
    #[serde(default)]
    pub credits: Option<CreditsInfo>,
}

/// 本次请求后的剩余积分
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct CreditsInfo {
    pub remaining: i64,
    pub plan: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ReplyStatus {
    Ok,
    InsufficientContext,
    NotAConversation,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Analysis {
    #[serde(default)]
    pub language: String,
    #[serde(default)]
    pub latest_message: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub emotion: String,
    #[serde(default)]
    pub intent: String,
    #[serde(default)]
    pub strategy: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ReplySuggestion {
    pub style: String,
    pub label: String,
    pub text: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ReplyMeta {
    pub request_id: String,
    pub latency_ms: u64,
    pub remaining_today: Option<u32>,
}

/// 客户端统一错误。`code` 决定 overlay 显示的文案。
#[derive(Clone, Debug, Serialize)]
pub struct AiError {
    pub code: AiErrorCode,
    pub detail: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AiErrorCode {
    Network,
    QuotaExceeded,
    Unavailable,
    InsufficientContext,
    NotAConversation,
    /// 体验额度用完，登录可领取注册赠送
    LoginRequired,
    /// 已登录但积分用完，需要升级或等每日补充
    InsufficientCredits,
    /// 达到套餐的每日上限
    DailyCap,
    /// 请求太频繁
    RateLimited,
}

impl AiError {
    pub fn new(code: AiErrorCode, detail: impl Into<String>) -> Self {
        Self { code, detail: detail.into() }
    }
}

pub struct ImagePayload {
    pub jpeg: Vec<u8>,
}

pub struct UserContext {
    pub display_name: Option<String>,
    pub locale: Option<String>,
}
