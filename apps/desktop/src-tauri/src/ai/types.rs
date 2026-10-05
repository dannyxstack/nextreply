use serde::{Deserialize, Serialize};

/// 与服务端 `/v1/reply` 的响应保持一致（见 server/src/ai/schema.ts）。
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ReplyResult {
    pub status: ReplyStatus,
    pub analysis: Analysis,
    pub replies: Vec<ReplySuggestion>,
    #[serde(default)]
    pub meta: Option<ReplyMeta>,
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
