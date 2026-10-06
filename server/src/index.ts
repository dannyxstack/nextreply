import { Hono } from "hono";
import { clientIp, accountRoutes } from "./account/routes";
import { resolveCaller } from "./account/identity";
import { generateReplies, type ImageMediaType, type ReplyOutput } from "./ai/anthropic";
import { deviceHash } from "./auth";
import { billingRoutes } from "./billing/routes";
import { credits, dailyRefillFor } from "./credits/client";
import { PLANS, RATE_LIMIT, REPLY_COST } from "./credits/plans";
import { intVar, isDev, type Env } from "./env";
import { ApiError } from "./errors";
import { ipKeys, readCount, bump } from "./quota";

export { CreditAccount } from "./credits/do";

type AppEnv = { Bindings: Env };

const app = new Hono<AppEnv>();

// base64 后约 6MB，对应 ~4.5MB 原图；客户端缩图后通常只有几百 KB
const MAX_IMAGE_B64_CHARS = 6_000_000;
const MEDIA_TYPES = new Set<ImageMediaType>(["image/jpeg", "image/png", "image/webp"]);
const IDEMPOTENCY_RE = /^[A-Za-z0-9_-]{8,80}$/;

function optionalString(v: unknown, max: number): string | undefined {
  return typeof v === "string" && v.trim() ? v.trim().slice(0, max) : undefined;
}

app.onError((err, c) => {
  const apiErr = err instanceof ApiError ? err : new ApiError("internal", "Internal server error.");
  if (!(err instanceof ApiError)) console.error("[unhandled]", err);
  return c.json(apiErr.toJSON(), apiErr.status as 400);
});

app.notFound((c) => c.json(new ApiError("bad_request", "Not found.").toJSON(), 404));

app.get("/v1/health", (c) => c.json({ ok: true, model: c.env.MODEL }));

app.route("/", accountRoutes);
app.route("/", billingRoutes);

/** 开发测试用的固定结果：测试积分扣减时不调用模型、不花钱 */
const mockReply = (): ReplyOutput => ({
  result: {
    status: "ok",
    analysis: { language: "zh-CN", latest_message: "（模拟）", summary: "这是模拟结果（MOCK_AI）", emotion: "-", intent: "-", strategy: "-" },
    replies: [
      { style: "empathetic", label: "得体", text: "（模拟回复 1）" },
      { style: "funny", label: "轻松", text: "（模拟回复 2）" },
      { style: "direct", label: "简洁", text: "（模拟回复 3）" },
    ],
  },
  model: "mock",
  usage: { input: 0, output: 0, cacheRead: 0 },
  fallbackUsed: false,
});

app.post("/v1/reply", async (c) => {
  const started = Date.now();
  const requestId = crypto.randomUUID();
  const caller = await resolveCaller(c);

  const body = (await c.req.json().catch(() => null)) as Record<string, unknown> | null;
  if (!body) throw new ApiError("bad_request", "Request body must be a JSON object.");
  const image = body.image;
  const mediaType = (body.media_type ?? "image/jpeg") as ImageMediaType;
  if (typeof image !== "string" || image.length === 0) throw new ApiError("bad_request", "Missing image.");
  if (image.length > MAX_IMAGE_B64_CHARS) throw new ApiError("bad_request", "Image is too large.");
  if (!MEDIA_TYPES.has(mediaType)) throw new ApiError("bad_request", "Unsupported media_type.");

  // 外层防线：同一 IP 每日请求上限（防止大量账号 / 设备从同一来源刷量）
  const ip = clientIp(c);
  if ((await readCount(c.env.QUOTA, ipKeys.reply(ip))) >= intVar(c.env.IP_DAILY_QUOTA, 200)) {
    throw new ApiError("rate_limited", "Too many requests from this network today.");
  }

  // 预扣积分（含频率限制、每日上限）。幂等键由客户端提供，网络重试不会重复扣费
  const plan = PLANS[caller.plan];
  const headerKey = c.req.header("idempotency-key") ?? "";
  const holdKey = IDEMPOTENCY_RE.test(headerKey) ? headerKey : requestId;
  const account = credits(c.env, caller.owner);
  const hold = await account.reserve({
    key: holdKey,
    cost: REPLY_COST,
    dailyCap: plan.dailyCap,
    perMinute: RATE_LIMIT.perMinute,
    maxConcurrent: RATE_LIMIT.maxConcurrent,
    dailyRefill: dailyRefillFor(caller.plan),
  });
  if (!hold.ok) {
    const details = { plan: caller.plan, remaining: hold.remaining, login_required: caller.kind === "device" };
    const msg = { insufficient_credits: "Not enough credits.", rate_limited: "Too many requests, slow down.", daily_cap: "Daily limit reached." }[hold.code];
    throw new ApiError(hold.code, msg, details);
  }

  const hash = await deviceHash(caller.deviceId);
  let out: ReplyOutput;
  try {
    out =
      isDev(c.env) && c.env.MOCK_AI === "true"
        ? mockReply()
        : await generateReplies(
            c.env,
            {
              imageBase64: image,
              mediaType,
              locale: optionalString(body.locale, 20),
              displayName: optionalString(body.display_name, 64),
            },
            c.req.raw.signal,
          );
  } catch (err) {
    await account.release(holdKey);
    const code = err instanceof ApiError ? err.code : "internal";
    console.log(JSON.stringify({ evt: "reply_error", request_id: requestId, device: hash, plan: caller.plan, code, latency_ms: Date.now() - started }));
    throw err;
  }

  // 只有成功生成回复才扣积分；看不懂截图（非聊天、内容不足）不扣
  const charged = out.result.status === "ok";
  if (charged) {
    await account.commit(holdKey);
    await bump(c.env.QUOTA, ipKeys.reply(ip));
  } else {
    await account.release(holdKey);
  }
  const balance = await account.balance(dailyRefillFor(caller.plan));
  const latency = Date.now() - started;

  // 只记录元数据：不记录图片、聊天内容、回复内容
  console.log(
    JSON.stringify({
      evt: "reply", request_id: requestId, device: hash, plan: caller.plan, status: out.result.status, model: out.model,
      fallback: out.fallbackUsed, latency_ms: latency, tokens_in: out.usage.input, tokens_out: out.usage.output,
      cache_read: out.usage.cacheRead, client_version: optionalString(body.client_version, 20),
    }),
  );
  c.executionCtx.waitUntil(
    c.env.DB.prepare("INSERT INTO usage_events (request_id, owner, plan, status, credits, model, latency_ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
      .bind(requestId, caller.owner, caller.plan, out.result.status, charged ? REPLY_COST : 0, out.model, latency, Date.now())
      .run(),
  );

  return c.json({
    ...out.result,
    meta: { request_id: requestId, latency_ms: latency, remaining_today: balance.total },
    credits: { remaining: balance.total, plan: caller.plan },
  });
});

export default app;
