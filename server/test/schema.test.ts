import { describe, expect, it } from "vitest";
import { ReplyResultSchema } from "../src/ai/schema";

const analysis = { language: "zh-CN", latest_message: "你最近是不是很忙？", summary: "对方有点失落", emotion: "失落", intent: "求关注", strategy: "先共情再解释" };
const reply = (style: "empathetic" | "funny" | "direct") => ({ style, label: "x", text: "y" });

describe("ReplyResultSchema", () => {
  it("accepts an ok result and trims to 3 replies", () => {
    const r = ReplyResultSchema.parse({ status: "ok", analysis, replies: [reply("empathetic"), reply("funny"), reply("direct"), reply("direct")] });
    expect(r.replies).toHaveLength(3);
  });

  it("rejects ok with no replies", () => {
    expect(ReplyResultSchema.safeParse({ status: "ok", analysis, replies: [] }).success).toBe(false);
  });

  it("drops replies for non-ok statuses", () => {
    const r = ReplyResultSchema.parse({ status: "insufficient_context", analysis, replies: [reply("direct")] });
    expect(r.replies).toEqual([]);
  });
});
