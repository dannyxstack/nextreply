import { Hono, type Context } from "hono";
import { generateReplies, type ImageMediaType } from "./ai/anthropic";
import { deviceHash, isValidDeviceId, issueToken, verifyToken } from "./auth";
import { intVar, type Env } from "./env";
import { ApiError } from "./errors";
import { allowRegister, consume, remaining, type QuotaLimits } from "./quota";

type AppEnv = { Bindings: Env };

const app = new Hono<AppEnv>();

// base64 后约 6MB，对应 ~4.5MB 原图；客户端缩图后通常只有几百 KB
const MAX_IMAGE_B64_CHARS = 6_000_000;
const MEDIA_TYPES = new Set<ImageMediaType>(["image/jpeg", "image/png", "image/webp"]);

function clientIp(c: Context<AppEnv>): string {
  return c.req.header("cf-connecting-ip") ?? "127.0.0.1";
}

function limits(env: Env): QuotaLimits {
  return { device: intVar(env.DAILY_QUOTA, 30), ip: intVar(env.IP_DAILY_QUOTA, 200) };
}

async function readJson(c: Context<AppEnv>): Promise<Record<string, unknown>> {
  try {
    const body = await c.req.json();
    if (body && typeof body === "object") return body as Record<string, unknown>;
  } catch {
    // fallthrough
  }
  throw new ApiError("bad_request", "Request body must be a JSON object.");
}

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

app.post("/v1/device/register", async (c) => {
  const body = await readJson(c);
  if (!isValidDeviceId(body.device_id)) throw new ApiError("bad_request", "Invalid device_id.");
  if (!(await allowRegister(c.env.QUOTA, clientIp(c), intVar(c.env.REGISTER_PER_IP, 20)))) {
    throw new ApiError("quota_exceeded", "Too many devices registered from this network today.");
  }
  return c.json({ token: await issueToken(c.env.TOKEN_SECRET, body.device_id) });
});

app.post("/v1/reply", async (c) => {
  const started = Date.now();
  const requestId = crypto.randomUUID();

  const auth = c.req.header("authorization") ?? "";
  const token = auth.startsWith("Bearer ") ? auth.slice(7) : "";
  const deviceId = token ? await verifyToken(c.env.TOKEN_SECRET, token) : null;
  if (!deviceId) throw new ApiError("unauthorized", "Invalid or missing device token.");

  const body = await readJson(c);
  const image = body.image;
  const mediaType = (body.media_type ?? "image/jpeg") as ImageMediaType;
  if (typeof image !== "string" || image.length === 0) throw new ApiError("bad_request", "Missing image.");
  if (image.length > MAX_IMAGE_B64_CHARS) throw new ApiError("bad_request", "Image is too large.");
  if (!MEDIA_TYPES.has(mediaType)) throw new ApiError("bad_request", "Unsupported media_type.");

  const ip = clientIp(c);
  const lim = limits(c.env);
  if ((await remaining(c.env.QUOTA, deviceId, ip, lim)) <= 0) {
    throw new ApiError("quota_exceeded", "Daily free quota used up.");
  }

  const hash = await deviceHash(deviceId);
  try {
    const out = await generateReplies(
      c.env,
      {
        imageBase64: image,
        mediaType,
        locale: optionalString(body.locale, 20),
        displayName: optionalString(body.display_name, 64),
      },
      c.req.raw.signal,
    );

    const left =
      out.result.status === "ok" ? await consume(c.env.QUOTA, deviceId, ip, lim) : await remaining(c.env.QUOTA, deviceId, ip, lim);
    const latency = Date.now() - started;

    // 只记录元数据：不记录图片、聊天内容、回复内容
    console.log(
      JSON.stringify({
        evt: "reply",
        request_id: requestId,
        device: hash,
        status: out.result.status,
        model: out.model,
        fallback: out.fallbackUsed,
        latency_ms: latency,
        tokens_in: out.usage.input,
        tokens_out: out.usage.output,
        cache_read: out.usage.cacheRead,
        client_version: optionalString(body.client_version, 20),
      }),
    );

    return c.json({ ...out.result, meta: { request_id: requestId, latency_ms: latency, remaining_today: left } });
  } catch (err) {
    const code = err instanceof ApiError ? err.code : "internal";
    console.log(JSON.stringify({ evt: "reply_error", request_id: requestId, device: hash, code, latency_ms: Date.now() - started }));
    throw err;
  }
});

export default app;
