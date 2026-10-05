import Anthropic from "@anthropic-ai/sdk";
import type { Env } from "../env";
import { intVar } from "../env";
import { ApiError } from "../errors";
import { SYSTEM_PROMPT, buildUserText } from "./prompt";
import { REPLY_JSON_SCHEMA, ReplyResultSchema, type ReplyResult } from "./schema";

export type ImageMediaType = "image/jpeg" | "image/png" | "image/webp";

export interface ReplyInput {
  imageBase64: string;
  mediaType: ImageMediaType;
  locale?: string;
  displayName?: string;
}

export interface ReplyOutput {
  result: ReplyResult;
  model: string;
  usage: { input: number; output: number; cacheRead: number };
  fallbackUsed: boolean;
}

// 服务端 fallbacks: "default" 只在这些模型上可用
const FALLBACK_MODELS = new Set(["claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5"]);
const EFFORTS = new Set(["low", "medium", "high", "xhigh", "max"]);
const REQUEST_TIMEOUT_MS = 25_000;

type Effort = "low" | "medium" | "high" | "xhigh" | "max";

function thinkingConfig(env: Env): Anthropic.Beta.Messages.BetaThinkingConfigParam | undefined {
  switch (env.THINKING) {
    case "between_tools":
      return { type: "between_tools" };
    case "adaptive":
      return { type: "adaptive" };
    default:
      return undefined; // auto：不传，由模型默认行为决定
  }
}

export async function generateReplies(env: Env, input: ReplyInput, signal?: AbortSignal): Promise<ReplyOutput> {
  if (!env.ANTHROPIC_API_KEY) {
    console.error("[anthropic] ANTHROPIC_API_KEY is not configured (.dev.vars / wrangler secret); restart wrangler after editing .dev.vars");
    throw new ApiError("internal", "Server AI credentials are not configured.");
  }
  const client = new Anthropic({ apiKey: env.ANTHROPIC_API_KEY, maxRetries: 1, timeout: REQUEST_TIMEOUT_MS });
  const model = env.MODEL || "claude-opus-5-5";
  const effort = EFFORTS.has(env.EFFORT) ? (env.EFFORT as Effort) : undefined; // "none" 或空：不传（Haiku 4.5 不支持 effort）
  const thinking = thinkingConfig(env);
  const useFallbacks = env.FALLBACKS !== "off" && FALLBACK_MODELS.has(model);

  let message: Anthropic.Beta.Messages.BetaMessage;
  try {
    const stream = client.beta.messages.stream(
      {
        model,
        max_tokens: intVar(env.MAX_TOKENS, 8000),
        system: [{ type: "text", text: SYSTEM_PROMPT, cache_control: { type: "ephemeral" } }],
        messages: [
          {
            role: "user",
            content: [
              { type: "image", source: { type: "base64", media_type: input.mediaType, data: input.imageBase64 } },
              { type: "text", text: buildUserText(input.displayName, input.locale) },
            ],
          },
        ],
        output_config: {
          ...(effort ? { effort } : {}),
          format: { type: "json_schema", schema: REPLY_JSON_SCHEMA },
        },
        ...(thinking ? { thinking } : {}),
        ...(useFallbacks ? { betas: ["server-side-fallback-2026-07-01"], fallbacks: "default" as const } : {}),
      },
      { signal },
    );
    message = await stream.finalMessage();
  } catch (err) {
    throw mapSdkError(err);
  }

  if (message.stop_reason === "refusal") {
    throw new ApiError("refusal", "The model declined to analyze this conversation.");
  }
  if (message.stop_reason === "max_tokens") {
    throw new ApiError("invalid_output", "The model response was truncated.");
  }

  const text = message.content
    .filter((b): b is Anthropic.Beta.Messages.BetaTextBlock => b.type === "text")
    .map((b) => b.text)
    .join("");

  let json: unknown;
  try {
    json = JSON.parse(text);
  } catch {
    throw new ApiError("invalid_output", "The model returned malformed JSON.");
  }
  const parsed = ReplyResultSchema.safeParse(json);
  if (!parsed.success) {
    throw new ApiError("invalid_output", "The model returned an unexpected shape.");
  }

  const fallbackUsed = (message.usage.iterations ?? []).some((it) => it.type === "fallback_message");
  return {
    result: parsed.data,
    model: message.model,
    usage: {
      input: message.usage.input_tokens,
      output: message.usage.output_tokens,
      cacheRead: message.usage.cache_read_input_tokens ?? 0,
    },
    fallbackUsed,
  };
}

function mapSdkError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  // 上游错误信息只写日志（不含用户内容），不透传给客户端
  if (err instanceof Anthropic.APIError) console.error(`[anthropic] ${err.status ?? "-"} ${err.message}`);
  if (err instanceof Anthropic.APIConnectionTimeoutError) return new ApiError("timeout", "The AI request timed out.");
  if (err instanceof Anthropic.APIUserAbortError) return new ApiError("timeout", "The AI request was aborted.");
  if (err instanceof Anthropic.AuthenticationError) return new ApiError("internal", "Server AI credentials are invalid.");
  if (err instanceof Anthropic.RateLimitError) return new ApiError("upstream_error", "The AI service is busy.");
  if (err instanceof Anthropic.BadRequestError) return new ApiError("upstream_error", "The AI service rejected the request.");
  if (err instanceof Anthropic.APIError) return new ApiError("upstream_error", `AI service error (${err.status ?? "?"}).`);
  console.error("[anthropic] unexpected error:", err instanceof Error ? `${err.name}: ${err.message}` : String(err));
  return new ApiError("internal", "Unexpected error calling the AI service.");
}
