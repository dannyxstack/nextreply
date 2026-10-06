// 设备注册、邮箱验证码登录（浏览器 + PKCE + 本机回调）、令牌刷新、/v1/me。

import { Hono, type Context } from "hono";
import { isValidDeviceId, issueToken } from "../auth";
import { credits, dailyRefillFor, grantTo, userOwner } from "../credits/client";
import { ensureDevice } from "./devices";
import {
  MAX_DEVICES_PER_USER,
  PLANS,
  SIGNUP_BONUS,
  SIGNUP_BONUS_DAYS,
} from "../credits/plans";
import { intVar, isDev, type Env } from "../env";
import { ApiError } from "../errors";
import { loginPage } from "../pages/login";
import { messagePage } from "../pages/layout";
import { ipKeys, takeIfUnder } from "../quota";
import { pkceChallenge, randomDigits, randomToken, safeEqual, sha256Hex } from "./crypto";
import { isDisposable, isValidEmail, normalizeEmail, sendLoginCode, verifyTurnstile } from "./email";
import { getSubscription, planFromSubscription, resolveCaller } from "./identity";
import { ACCESS_TTL_S, issueRefresh, revokeByToken, revokeDevice, rotateRefresh, signAccess } from "./tokens";

type AppEnv = { Bindings: Env };
type Ctx = Context<AppEnv>;

const OTP_TTL_MS = 10 * 60 * 1000;
const OTP_MAX_ATTEMPTS = 5;
const OTP_RESEND_MS = 60 * 1000;
const OTP_PER_IP_PER_DAY = 10;
const AUTH_CODE_TTL_MS = 5 * 60 * 1000;
/** 只允许跳回本机回环地址（RFC 8252），防止授权码被重定向到第三方网站 */
const LOOPBACK_RE = /^http:\/\/(127\.0\.0\.1|localhost):\d{2,5}\/callback$/;
const HW_HASH_RE = /^[a-f0-9]{32,64}$/;

export const clientIp = (c: Ctx) => c.req.header("cf-connecting-ip") ?? "127.0.0.1";

async function readJson(c: Ctx): Promise<Record<string, unknown>> {
  try {
    const body = await c.req.json();
    if (body && typeof body === "object") return body as Record<string, unknown>;
  } catch {
    // fallthrough
  }
  throw new ApiError("bad_request", "Request body must be a JSON object.");
}

const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");

const otpHash = (env: Env, emailNorm: string, code: string) => sha256Hex(`${env.TOKEN_SECRET}:otp:${emailNorm}:${code}`);

export const accountRoutes = new Hono<AppEnv>();

// ---------- 设备注册 + 体验额度 ----------

accountRoutes.post("/v1/device/register", async (c) => {
  const body = await readJson(c);
  const deviceId = body.device_id;
  if (!isValidDeviceId(deviceId)) throw new ApiError("bad_request", "Invalid device_id.");
  const hwHash = HW_HASH_RE.test(str(body.hw_hash)) ? str(body.hw_hash) : null;
  const ip = clientIp(c);

  const known = await c.env.DB.prepare("SELECT 1 FROM devices WHERE id = ?").bind(deviceId).first();
  if (!known && !(await takeIfUnder(c.env.QUOTA, ipKeys.register(ip), intVar(c.env.REGISTER_PER_IP, 20)))) {
    throw new ApiError("quota_exceeded", "Too many devices registered from this network today.");
  }
  await ensureDevice(c.env, deviceId, hwHash, ip);
  return c.json({ token: await issueToken(c.env.TOKEN_SECRET, deviceId) });
});

// ---------- 浏览器登录页 ----------

accountRoutes.get("/auth/login", (c) => {
  const q = c.req.query();
  const deviceId = q.device_id ?? "";
  const redirectUri = q.redirect_uri ?? "";
  if (!isValidDeviceId(deviceId) || !LOOPBACK_RE.test(redirectUri) || !q.state || !q.code_challenge) {
    return c.html(messagePage("登录链接无效", "请回到 NextReply 重新点击登录。"), 400);
  }
  return c.html(
    loginPage({ deviceId, redirectUri, state: q.state, codeChallenge: q.code_challenge, turnstileSiteKey: c.env.TURNSTILE_SITE_KEY }),
  );
});

accountRoutes.post("/auth/email/start", async (c) => {
  const body = await readJson(c);
  const email = str(body.email);
  if (!isValidEmail(email)) throw new ApiError("bad_request", "邮箱格式不正确。");
  if (isDisposable(email)) throw new ApiError("bad_request", "暂不支持临时邮箱，请换一个常用邮箱。");
  const ip = clientIp(c);
  if (!(await verifyTurnstile(c.env, str(body.turnstile) || undefined, ip))) {
    throw new ApiError("bad_request", "人机验证失败，请重试。");
  }

  const emailNorm = normalizeEmail(email);
  const db = c.env.DB;
  const now = Date.now();
  const prev = await db.prepare("SELECT created_at FROM otp_codes WHERE email_norm = ?").bind(emailNorm).first<{ created_at: number }>();
  if (prev && now - prev.created_at < OTP_RESEND_MS) throw new ApiError("rate_limited", "发送太频繁，请 1 分钟后再试。");
  if (!(await takeIfUnder(c.env.QUOTA, ipKeys.otp(ip), OTP_PER_IP_PER_DAY))) {
    throw new ApiError("rate_limited", "今天请求验证码的次数太多了，请明天再试。");
  }

  const code = randomDigits(6);
  await db
    .prepare(
      "INSERT INTO otp_codes (email_norm, code_hash, attempts, expires_at, created_at) VALUES (?, ?, 0, ?, ?) " +
        "ON CONFLICT(email_norm) DO UPDATE SET code_hash = excluded.code_hash, attempts = 0, expires_at = excluded.expires_at, created_at = excluded.created_at",
    )
    .bind(emailNorm, await otpHash(c.env, emailNorm, code), now + OTP_TTL_MS, now)
    .run();

  let result;
  try {
    result = await sendLoginCode(c.env, email, code, isDev(c.env));
  } catch (e) {
    console.error("[auth] send code failed:", e instanceof Error ? e.message : e);
    throw new ApiError("internal", "验证码发送失败，请稍后再试。");
  }
  return c.json(result.sent ? { ok: true } : { ok: true, dev_code: result.devCode });
});

/** 找到或创建用户；把设备绑定到该用户；发放注册赠送（每台设备、每个账号各一次）。 */
async function signIn(env: Env, email: string, deviceId: string, ip: string): Promise<string> {
  const db = env.DB;
  const now = Date.now();
  const emailNorm = normalizeEmail(email);

  let user = await db.prepare("SELECT id, status FROM users WHERE email_norm = ?").bind(emailNorm).first<{ id: string; status: string }>();
  if (!user) {
    user = { id: crypto.randomUUID(), status: "active" };
    await db.prepare("INSERT INTO users (id, email, email_norm, created_at) VALUES (?, ?, ?, ?)").bind(user.id, email, emailNorm, now).run();
  }
  if (user.status !== "active") throw new ApiError("unauthorized", "该账号已被停用，请联系客服。");

  const device = await db.prepare("SELECT id, signup_bonus_claimed FROM devices WHERE id = ?").bind(deviceId).first<{ signup_bonus_claimed: number }>();
  if (!device) {
    await db.prepare("INSERT INTO devices (id, user_id, first_ip, created_at, last_seen) VALUES (?, ?, ?, ?, ?)").bind(deviceId, user.id, ip, now, now).run();
  } else {
    await db.prepare("UPDATE devices SET user_id = ?, last_seen = ? WHERE id = ?").bind(user.id, now, deviceId).run();
  }

  // 每个账号最多 N 台设备：超出时让最早的设备下线
  const devices = await db.prepare("SELECT id FROM devices WHERE user_id = ? ORDER BY last_seen DESC").bind(user.id).all<{ id: string }>();
  for (const d of devices.results.slice(MAX_DEVICES_PER_USER)) {
    await db.prepare("UPDATE devices SET user_id = NULL WHERE id = ?").bind(d.id).run();
    await revokeDevice(db, user.id, d.id);
  }

  // 注册赠送：DO 内按 sourceRef 幂等保证每个账号一次；devices 标记保证每台设备一次（防止一台机器注册多个号刷额度）
  if (!device?.signup_bonus_claimed) {
    const granted = await grantTo(env, userOwner(user.id), {
      bucket: "bonus",
      amount: SIGNUP_BONUS,
      expiresAt: now + SIGNUP_BONUS_DAYS * 24 * 60 * 60 * 1000,
      sourceRef: "signup_bonus",
      reason: "signup_bonus",
    });
    if (granted) await db.prepare("UPDATE devices SET signup_bonus_claimed = 1 WHERE id = ?").bind(deviceId).run();
  }
  return user.id;
}

accountRoutes.post("/auth/email/verify", async (c) => {
  const body = await readJson(c);
  const email = str(body.email);
  const code = str(body.code);
  const deviceId = str(body.device_id);
  const redirectUri = str(body.redirect_uri);
  const state = str(body.state);
  const codeChallenge = str(body.code_challenge);
  if (!isValidEmail(email) || !/^\d{6}$/.test(code)) throw new ApiError("bad_request", "验证码不正确。");
  if (!isValidDeviceId(deviceId) || !LOOPBACK_RE.test(redirectUri) || !state || !codeChallenge) {
    throw new ApiError("bad_request", "登录链接无效，请回到 NextReply 重新点击登录。");
  }

  const db = c.env.DB;
  const emailNorm = normalizeEmail(email);
  const otp = await db
    .prepare("SELECT code_hash, attempts, expires_at FROM otp_codes WHERE email_norm = ?")
    .bind(emailNorm)
    .first<{ code_hash: string; attempts: number; expires_at: number }>();
  if (!otp || otp.expires_at < Date.now()) throw new ApiError("bad_request", "验证码已过期，请重新获取。");
  if (otp.attempts >= OTP_MAX_ATTEMPTS) throw new ApiError("rate_limited", "错误次数太多，请重新获取验证码。");
  if (!safeEqual(otp.code_hash, await otpHash(c.env, emailNorm, code))) {
    await db.prepare("UPDATE otp_codes SET attempts = attempts + 1 WHERE email_norm = ?").bind(emailNorm).run();
    throw new ApiError("bad_request", "验证码不正确。");
  }
  await db.prepare("DELETE FROM otp_codes WHERE email_norm = ?").bind(emailNorm).run();

  const userId = await signIn(c.env, email, deviceId, clientIp(c));
  const authCode = randomToken();
  await db
    .prepare("INSERT INTO auth_codes (code_hash, user_id, device_id, code_challenge, expires_at) VALUES (?, ?, ?, ?, ?)")
    .bind(await sha256Hex(authCode), userId, deviceId, codeChallenge, Date.now() + AUTH_CODE_TTL_MS)
    .run();

  const redirect = new URL(redirectUri);
  redirect.searchParams.set("code", authCode);
  redirect.searchParams.set("state", state);
  return c.json({ redirect: redirect.toString() });
});

// ---------- 令牌 ----------

async function tokenResponse(env: Env, userId: string, deviceId: string, refreshToken: string) {
  const user = await env.DB.prepare("SELECT email FROM users WHERE id = ?").bind(userId).first<{ email: string }>();
  return {
    access_token: await signAccess(env, { sub: userId, did: deviceId }),
    expires_in: ACCESS_TTL_S,
    refresh_token: refreshToken,
    user: { id: userId, email: user?.email ?? "" },
  };
}

accountRoutes.post("/v1/auth/token", async (c) => {
  const body = await readJson(c);
  const code = str(body.code);
  const verifier = str(body.code_verifier);
  const deviceId = str(body.device_id);
  if (!code || verifier.length < 43 || !isValidDeviceId(deviceId)) throw new ApiError("bad_request", "Invalid token request.");

  const db = c.env.DB;
  const hash = await sha256Hex(code);
  const row = await db
    .prepare("SELECT user_id, device_id, code_challenge, expires_at, used FROM auth_codes WHERE code_hash = ?")
    .bind(hash)
    .first<{ user_id: string; device_id: string; code_challenge: string; expires_at: number; used: number }>();
  if (!row || row.used || row.expires_at < Date.now() || row.device_id !== deviceId) {
    throw new ApiError("unauthorized", "Authorization code is invalid or expired.");
  }
  if (!safeEqual(row.code_challenge, await pkceChallenge(verifier))) throw new ApiError("unauthorized", "PKCE verification failed.");
  await db.prepare("UPDATE auth_codes SET used = 1 WHERE code_hash = ?").bind(hash).run();

  const refresh = await issueRefresh(db, row.user_id, deviceId);
  return c.json(await tokenResponse(c.env, row.user_id, deviceId, refresh));
});

accountRoutes.post("/v1/auth/refresh", async (c) => {
  const body = await readJson(c);
  const result = await rotateRefresh(c.env.DB, str(body.refresh_token), str(body.device_id));
  if (!result.ok) throw new ApiError("unauthorized", "Session expired, please sign in again.", { reason: `refresh_${result.reason}` });
  return c.json(await tokenResponse(c.env, result.userId, result.deviceId, result.refreshToken));
});

accountRoutes.post("/v1/auth/logout", async (c) => {
  const body = await readJson(c);
  const token = str(body.refresh_token);
  if (token) await revokeByToken(c.env.DB, token);
  return c.json({ ok: true });
});

// ---------- 账户信息 ----------

accountRoutes.get("/v1/me", async (c) => {
  const caller = await resolveCaller(c);
  const plan = PLANS[caller.plan];
  const balance = await credits(c.env, caller.owner).balance(dailyRefillFor(caller.plan));
  const base = {
    plan: { id: plan.id, label: plan.label, daily_cap: plan.dailyCap, monthly_credits: plan.monthlyCredits ?? null },
    credits: { total: balance.total, buckets: balance.buckets, used_today: balance.usedToday },
    billing: {
      stripe: Boolean(c.env.STRIPE_SECRET_KEY),
      dev: isDev(c.env),
      plans: (["pro", "pro_plus"] as const).map((id) => ({
        id,
        label: PLANS[id].label,
        price_usd: PLANS[id].priceUsd,
        monthly_credits: PLANS[id].monthlyCredits,
        daily_cap: PLANS[id].dailyCap,
      })),
    },
  };
  if (caller.kind === "device") return c.json({ ...base, user: null, subscription: null });

  const user = await c.env.DB.prepare("SELECT email FROM users WHERE id = ?").bind(caller.userId).first<{ email: string }>();
  const sub = await getSubscription(c.env.DB, caller.userId);
  return c.json({
    ...base,
    user: { id: caller.userId, email: user?.email ?? "" },
    subscription:
      sub && planFromSubscription(sub) !== "free"
        ? {
            plan: sub.plan,
            status: sub.status,
            provider: sub.provider,
            current_period_end: sub.current_period_end,
            cancel_at_period_end: Boolean(sub.cancel_at_period_end),
          }
        : null,
  });
});
