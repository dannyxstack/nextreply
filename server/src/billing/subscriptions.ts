// 订阅状态变更与积分发放（Stripe webhook 和开发模式的模拟支付共用）。

import { grantTo, credits, userOwner } from "../credits/client";
import { PLANS, type PaidPlanId } from "../credits/plans";
import type { Env } from "../env";

/** 订阅积分在周期结束后多保留几天，避免续费扣款稍有延迟时用户突然没额度 */
const CREDIT_GRACE_MS = 3 * 24 * 60 * 60 * 1000;

export interface SubscriptionUpsert {
  userId: string;
  plan: PaidPlanId;
  status: "active" | "past_due" | "canceled";
  provider: "stripe" | "dev";
  customerId?: string | null;
  subscriptionId?: string | null;
  periodStart?: number | null;
  periodEnd?: number | null;
  cancelAtPeriodEnd?: boolean;
}

export async function upsertSubscription(env: Env, s: SubscriptionUpsert) {
  await env.DB.prepare(
    `INSERT INTO subscriptions (user_id, plan, status, provider, customer_id, subscription_id, current_period_start, current_period_end, cancel_at_period_end, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(user_id) DO UPDATE SET
       plan = excluded.plan, status = excluded.status, provider = excluded.provider,
       customer_id = COALESCE(excluded.customer_id, subscriptions.customer_id),
       subscription_id = COALESCE(excluded.subscription_id, subscriptions.subscription_id),
       current_period_start = COALESCE(excluded.current_period_start, subscriptions.current_period_start),
       current_period_end = COALESCE(excluded.current_period_end, subscriptions.current_period_end),
       cancel_at_period_end = excluded.cancel_at_period_end, updated_at = excluded.updated_at`,
  )
    .bind(
      s.userId, s.plan, s.status, s.provider, s.customerId ?? null, s.subscriptionId ?? null,
      s.periodStart ?? null, s.periodEnd ?? null, s.cancelAtPeriodEnd ? 1 : 0, Date.now(),
    )
    .run();
}

/** 为一个已付款的计费周期发放积分。sourceRef 用发票号保证同一周期只发一次。 */
export async function grantPeriodCredits(env: Env, userId: string, plan: PaidPlanId, periodEnd: number, sourceRef: string) {
  return grantTo(env, userOwner(userId), {
    bucket: "subscription",
    amount: PLANS[plan].monthlyCredits ?? 0,
    expiresAt: periodEnd + CREDIT_GRACE_MS,
    sourceRef,
    reason: `subscription_${plan}`,
  });
}

/** 收回订阅积分（退款、拒付、订阅被立即终止）。 */
export async function revokeSubscriptionCredits(env: Env, userId: string) {
  return credits(env, userOwner(userId)).revoke("subscription");
}
