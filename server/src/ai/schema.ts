import { z } from "zod";

// 结构化输出用的 JSON Schema。字段顺序有意义：analysis 在 replies 前面，
// 模型先写出对上下文的简短理解，再写回复。
export const REPLY_JSON_SCHEMA = {
  type: "object",
  additionalProperties: false,
  required: ["status", "analysis", "replies"],
  properties: {
    status: {
      type: "string",
      enum: ["ok", "insufficient_context", "not_a_conversation"],
    },
    analysis: {
      type: "object",
      additionalProperties: false,
      required: ["language", "latest_message", "summary", "emotion", "intent", "strategy"],
      properties: {
        language: { type: "string", description: "BCP-47 tag of the conversation's main language, e.g. zh-CN, en" },
        latest_message: { type: "string", description: "The other person's most recent message, verbatim" },
        summary: { type: "string", description: "One short sentence (max ~20 chars in CJK / 12 words) describing the situation, in the conversation language" },
        emotion: { type: "string", description: "The other person's likely emotion, a few words" },
        intent: { type: "string", description: "What the other person likely wants, a few words" },
        strategy: { type: "string", description: "Reply approach, a few words" },
      },
    },
    replies: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["style", "label", "text"],
        properties: {
          style: { type: "string", enum: ["empathetic", "funny", "direct"] },
          label: { type: "string" },
          text: { type: "string" },
        },
      },
    },
  },
} as const;

export const ReplyResultSchema = z
  .object({
    status: z.enum(["ok", "insufficient_context", "not_a_conversation"]),
    analysis: z.object({
      language: z.string(),
      latest_message: z.string(),
      summary: z.string(),
      emotion: z.string(),
      intent: z.string(),
      strategy: z.string(),
    }),
    replies: z.array(
      z.object({
        style: z.enum(["empathetic", "funny", "direct"]),
        label: z.string().min(1),
        text: z.string().min(1),
      }),
    ),
  })
  .refine((r) => r.status !== "ok" || r.replies.length > 0, { message: "status ok but no replies" })
  .transform((r) => ({
    ...r,
    // 非 ok 状态不返回回复；ok 状态最多保留 3 条
    replies: r.status === "ok" ? r.replies.slice(0, 3) : [],
  }));

export type ReplyResult = z.infer<typeof ReplyResultSchema>;
