pub mod proxy;
pub mod types;

use async_trait::async_trait;

use types::{AiError, ImagePayload, ReplyResult, UserContext};

/// AI 提供方抽象。MVP 只有中转服务一个实现（`proxy::ProxyProvider`）；
/// 以后可以增加 BYOK 直连（Anthropic / OpenAI）或本地模型。
#[async_trait]
pub trait AiProvider: Send + Sync {
    /// 一次请求同时完成对话分析和回复生成（省掉一轮网络往返）。
    async fn analyze_and_reply(&self, image: ImagePayload, ctx: UserContext) -> Result<ReplyResult, AiError>;
}
