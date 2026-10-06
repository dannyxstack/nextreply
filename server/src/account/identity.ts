// 识别调用方：已登录用户（access token）或匿名设备（设备 token），并确定当前套餐。

import type { Context } from "hono";
import { verifyToken as verifyDeviceToken } from "../auth";
import { deviceOwner, userOwner, type Owner } from "../credits/client";
import { isPaidPlan, type PlanId } from "../credits/plans";
import type { Env } from "../env";
import { ApiError } from "../errors";
import { ensureDevice } from "./devices";
import { verifyAccess } from "./tokens";

export type Caller =
  | { kind: "user"; userId: string; deviceId: string; owner: Owner; plan: PlanId }
  | { kind: "device"; deviceId: string; owner: Owner; plan: "trial" };

/** 订阅到期后的宽限期：续费扣款失败（past_due）时给用户几天时间处理 */
const GRACE_MS = 3 * 24 * 60 * 60 * 1000;

export interface SubscriptionRow {
  plan: string;
  status: string;
  provider: string;
  customer_id: string | null;
  subscription_id: string | null;
  current_period_start: number | null;
  current_period_end: number | null;
  cancel_at_period_end: number;
}

export async function getSubscription(db: D1Database, userId: string): Promise<SubscriptionRow | null> {
  return db.prepare("SELECT * FROM subscriptions WHERE user_id = ?").bind(userId).first<SubscriptionRow>();
}

export function planFromSubscription(sub: SubscriptionRow | null, now = Date.now()): PlanId {
  if (!sub || !isPaidPlan(sub.plan)) return "free";
  if (sub.status !== "active" && sub.status !== "past_due") return "free";
  if (sub.current_period_end && sub.current_period_end + GRACE_MS < now) return "free";
  return sub.plan;
}

function bearer(c: Context<{ Bindings: Env }>): string {
  const auth = c.req.header("authorization") ?? "";
  return auth.startsWith("Bearer ") ? auth.slice(7) : "";
}

export async function resolveCaller(c: Context<{ Bindings: Env }>): Promise<Caller> {
  const token = bearer(c);
  if (!token) throw new ApiError("unauthorized", "Missing token.");

  // JWT 有三段，设备 token 有两段
  if (token.split(".").length === 3) {
    const claims = await verifyAccess(c.env, token);
    if (!claims) throw new ApiError("unauthorized", "Invalid or expired access token.", { reason: "access_expired" });
    const user = await c.env.DB.prepare("SELECT status FROM users WHERE id = ?").bind(claims.sub).first<{ status: string }>();
    if (!user || user.status !== "active") throw new ApiError("unauthorized", "Account unavailable.", { reason: "account_unavailable" });
    const plan = planFromSubscription(await getSubscription(c.env.DB, claims.sub));
    return { kind: "user", userId: claims.sub, deviceId: claims.did, owner: userOwner(claims.sub), plan };
  }

  const deviceId = await verifyDeviceToken(c.env.TOKEN_SECRET, token);
  if (!deviceId) throw new ApiError("unauthorized", "Invalid device token.");
  // 旧版本注册的设备持有有效 token，但数据库里可能还没有记录：首次请求时补登记
  await ensureDevice(c.env, deviceId, null, c.req.header("cf-connecting-ip") ?? "127.0.0.1");
  return { kind: "device", deviceId, owner: deviceOwner(deviceId), plan: "trial" };
}
