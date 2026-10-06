// 订阅支付。桌面端申请一次性网页票据 → 浏览器打开结账页 → Stripe Checkout → webhook 回调发放积分。
// 没有配置 Stripe 时，开发模式下提供模拟支付页面，方便本地完整测试订阅流程。

import { Hono, type Context } from "hono";
import Stripe from "stripe";
import { randomToken, sha256Hex } from "../account/crypto";
import { getSubscription, resolveCaller } from "../account/identity";
import { isPaidPlan, PLANS, type PaidPlanId } from "../credits/plans";
import { isDev, type Env } from "../env";
import { ApiError } from "../errors";
import { esc, messagePage, page } from "../pages/layout";
import { grantPeriodCredits, revokeSubscriptionCredits, upsertSubscription } from "./subscriptions";

type AppEnv = { Bindings: Env };
type Ctx = Context<AppEnv>;

const TICKET_TTL_MS = 10 * 60 * 1000;
const DEV_PERIOD_MS = 30 * 24 * 60 * 60 * 1000;

const stripeClient = (env: Env) => (env.STRIPE_SECRET_KEY ? new Stripe(env.STRIPE_SECRET_KEY) : null);

function priceFor(env: Env, plan: PaidPlanId): string | undefined {
  return plan === "pro" ? env.STRIPE_PRICE_PRO : env.STRIPE_PRICE_PRO_PLUS;
}

function planForPrice(env: Env, priceId: string | undefined): PaidPlanId | null {
  if (priceId && priceId === env.STRIPE_PRICE_PRO) return "pro";
  if (priceId && priceId === env.STRIPE_PRICE_PRO_PLUS) return "pro_plus";
  return null;
}

async function createTicket(env: Env, userId: string, purpose: string): Promise<string> {
  const ticket = randomToken();
  await env.DB.prepare("INSERT INTO web_tickets (ticket_hash, user_id, purpose, expires_at) VALUES (?, ?, ?, ?)")
    .bind(await sha256Hex(ticket), userId, purpose, Date.now() + TICKET_TTL_MS)
    .run();
  return ticket;
}

/** 校验并消耗一次性票据，返回 user_id。 */
async function useTicket(env: Env, ticket: string | undefined, purpose: string): Promise<string | null> {
  if (!ticket) return null;
  const hash = await sha256Hex(ticket);
  const row = await env.DB.prepare("SELECT user_id, purpose, expires_at, used FROM web_tickets WHERE ticket_hash = ?")
    .bind(hash)
    .first<{ user_id: string; purpose: string; expires_at: number; used: number }>();
  if (!row || row.used || row.purpose !== purpose || row.expires_at < Date.now()) return null;
  await env.DB.prepare("UPDATE web_tickets SET used = 1 WHERE ticket_hash = ?").bind(hash).run();
  return row.user_id;
}

const publicUrl = (env: Env, path: string) => `${env.PUBLIC_URL.replace(/\/$/, "")}${path}`;

export const billingRoutes = new Hono<AppEnv>();

/** 已登录的桌面端申请结账 / 管理订阅链接（用系统浏览器打开）。 */
billingRoutes.post("/v1/billing/link", async (c) => {
  const caller = await resolveCaller(c);
  if (caller.kind !== "user") throw new ApiError("unauthorized", "Please sign in first.", { reason: "login_required" });
  const body = (await c.req.json().catch(() => ({}))) as { purpose?: string; plan?: string };
  if (body.purpose === "portal") {
    const ticket = await createTicket(c.env, caller.userId, "portal");
    return c.json({ url: publicUrl(c.env, `/billing/portal?ticket=${ticket}`) });
  }
  if (!body.plan || !isPaidPlan(body.plan)) throw new ApiError("bad_request", "Unknown plan.");
  const ticket = await createTicket(c.env, caller.userId, "checkout");
  return c.json({ url: publicUrl(c.env, `/billing/checkout?ticket=${ticket}&plan=${body.plan}`) });
});

billingRoutes.get("/billing/checkout", async (c) => {
  const plan = c.req.query("plan") ?? "";
  if (!isPaidPlan(plan)) return c.html(messagePage("链接无效", "未知的套餐。"), 400);
  const userId = await useTicket(c.env, c.req.query("ticket"), "checkout");
  if (!userId) return c.html(messagePage("链接已失效", "请回到 NextReply 重新点击升级。"), 400);

  const stripe = stripeClient(c.env);
  if (stripe) {
    const price = priceFor(c.env, plan);
    if (!price) return c.html(messagePage("暂不可用", "该套餐尚未开放购买。"), 503);
    const user = await c.env.DB.prepare("SELECT email FROM users WHERE id = ?").bind(userId).first<{ email: string }>();
    const sub = await getSubscription(c.env.DB, userId);
    const session = await stripe.checkout.sessions.create({
      mode: "subscription",
      line_items: [{ price, quantity: 1 }],
      client_reference_id: userId,
      ...(sub?.customer_id ? { customer: sub.customer_id } : { customer_email: user?.email }),
      metadata: { user_id: userId, plan },
      subscription_data: { metadata: { user_id: userId, plan } },
      success_url: publicUrl(c.env, "/billing/done?status=success"),
      cancel_url: publicUrl(c.env, "/billing/done?status=cancel"),
    });
    return c.redirect(session.url ?? publicUrl(c.env, "/billing/done?status=cancel"), 303);
  }

  if (!isDev(c.env)) return c.html(messagePage("暂不可用", "支付服务尚未开通。"), 503);
  const p = PLANS[plan];
  const confirm = await createTicket(c.env, userId, "dev_complete");
  return c.html(
    page(
      "模拟支付",
      `<h1>模拟支付（开发模式）</h1>
<p>未配置 Stripe，这里模拟一次订阅付款，不会产生真实扣费。</p>
<div class="plan"><b>${esc(p.label)}</b> · $${p.priceUsd}/月<br/><span style="color:var(--muted);font-size:13px">每月 ${p.monthlyCredits} 次，每天最多 ${p.dailyCap} 次</span></div>
<form method="post" action="/billing/dev/complete">
  <input type="hidden" name="ticket" value="${esc(confirm)}" />
  <input type="hidden" name="plan" value="${esc(plan)}" />
  <button type="submit">模拟支付成功</button>
</form>`,
    ),
  );
});

/** 开发模式：模拟付款成功 → 开通订阅并发放本周期积分。 */
billingRoutes.post("/billing/dev/complete", async (c) => {
  if (!isDev(c.env) || c.env.STRIPE_SECRET_KEY) return c.html(messagePage("不可用", "仅开发模式可用。"), 404);
  const form = await c.req.parseBody();
  const plan = String(form.plan ?? "");
  const userId = await useTicket(c.env, String(form.ticket ?? ""), "dev_complete");
  if (!userId || !isPaidPlan(plan)) return c.html(messagePage("链接已失效", "请回到 NextReply 重新点击升级。"), 400);
  const now = Date.now();
  await upsertSubscription(c.env, {
    userId, plan, status: "active", provider: "dev",
    subscriptionId: `dev_${userId}`, periodStart: now, periodEnd: now + DEV_PERIOD_MS, cancelAtPeriodEnd: false,
  });
  await grantPeriodCredits(c.env, userId, plan, now + DEV_PERIOD_MS, `dev:${userId}:${now}`);
  return c.redirect("/billing/done?status=success", 303);
});

billingRoutes.get("/billing/portal", async (c) => {
  const userId = await useTicket(c.env, c.req.query("ticket"), "portal");
  if (!userId) return c.html(messagePage("链接已失效", "请回到 NextReply 重新打开。"), 400);
  const sub = await getSubscription(c.env.DB, userId);
  const stripe = stripeClient(c.env);
  if (stripe && sub?.customer_id) {
    const session = await stripe.billingPortal.sessions.create({ customer: sub.customer_id, return_url: publicUrl(c.env, "/billing/done?status=portal") });
    return c.redirect(session.url, 303);
  }
  if (!isDev(c.env) || !sub) return c.html(messagePage("没有订阅", "当前账号没有可管理的订阅。"), 404);
  const t1 = await createTicket(c.env, userId, "dev_cancel");
  const t2 = await createTicket(c.env, userId, "dev_expire");
  return c.html(
    page(
      "管理订阅",
      `<h1>管理订阅（开发模式）</h1>
<p>当前：${esc(PLANS[sub.plan as PaidPlanId]?.label ?? sub.plan)} · ${esc(sub.status)}${sub.cancel_at_period_end ? " · 到期后取消" : ""}</p>
<form method="post" action="/billing/dev/cancel"><input type="hidden" name="ticket" value="${esc(t1)}" /><button type="submit" class="secondary">到期后取消订阅</button></form>
<form method="post" action="/billing/dev/expire"><input type="hidden" name="ticket" value="${esc(t2)}" /><button type="submit" class="secondary">立即结束（模拟到期 / 退款）</button></form>`,
    ),
  );
});

billingRoutes.post("/billing/dev/cancel", async (c) => {
  if (!isDev(c.env)) return c.html(messagePage("不可用", "仅开发模式可用。"), 404);
  const userId = await useTicket(c.env, String((await c.req.parseBody()).ticket ?? ""), "dev_cancel");
  if (!userId) return c.html(messagePage("链接已失效", "请重新打开。"), 400);
  await c.env.DB.prepare("UPDATE subscriptions SET cancel_at_period_end = 1, updated_at = ? WHERE user_id = ?").bind(Date.now(), userId).run();
  return c.html(messagePage("已设置到期后取消", "本周期内仍可使用剩余额度。回到 NextReply 刷新即可看到。"));
});

billingRoutes.post("/billing/dev/expire", async (c) => {
  if (!isDev(c.env)) return c.html(messagePage("不可用", "仅开发模式可用。"), 404);
  const userId = await useTicket(c.env, String((await c.req.parseBody()).ticket ?? ""), "dev_expire");
  if (!userId) return c.html(messagePage("链接已失效", "请重新打开。"), 400);
  await c.env.DB.prepare("UPDATE subscriptions SET status = 'canceled', updated_at = ? WHERE user_id = ?").bind(Date.now(), userId).run();
  await revokeSubscriptionCredits(c.env, userId);
  return c.html(messagePage("订阅已结束", "已回到免费套餐，订阅积分已收回。回到 NextReply 刷新即可看到。"));
});

billingRoutes.get("/billing/done", (c) => {
  const status = c.req.query("status");
  if (status === "success") return c.html(messagePage("支付成功", "订阅已开通，回到 NextReply 即可使用。积分可能需要几秒钟到账。"));
  if (status === "portal") return c.html(messagePage("已返回", "回到 NextReply 刷新即可看到最新状态。"));
  return c.html(messagePage("已取消支付", "没有产生任何扣费，可以回到 NextReply。"));
});

// ---------- Stripe webhook ----------

const mapStatus = (s: Stripe.Subscription.Status): "active" | "past_due" | "canceled" =>
  s === "active" || s === "trialing" ? "active" : s === "past_due" || s === "unpaid" ? "past_due" : "canceled";

async function userForCustomer(env: Env, customerId: string): Promise<string | null> {
  const row = await env.DB.prepare("SELECT user_id FROM subscriptions WHERE customer_id = ?").bind(customerId).first<{ user_id: string }>();
  return row?.user_id ?? null;
}

async function syncSubscription(env: Env, sub: Stripe.Subscription) {
  const userId = sub.metadata?.user_id;
  const item = sub.items.data[0];
  const plan = planForPrice(env, item?.price.id) ?? (isPaidPlan(sub.metadata?.plan ?? "") ? (sub.metadata.plan as PaidPlanId) : null);
  if (!userId || !plan) return;
  await upsertSubscription(env, {
    userId, plan, status: mapStatus(sub.status), provider: "stripe",
    customerId: typeof sub.customer === "string" ? sub.customer : sub.customer.id,
    subscriptionId: sub.id,
    periodStart: item ? item.current_period_start * 1000 : null,
    periodEnd: item ? item.current_period_end * 1000 : null,
    cancelAtPeriodEnd: sub.cancel_at_period_end,
  });
}

billingRoutes.post("/billing/webhook", async (c) => {
  const stripe = stripeClient(c.env);
  if (!stripe || !c.env.STRIPE_WEBHOOK_SECRET) return c.text("not configured", 404);
  const signature = c.req.header("stripe-signature") ?? "";
  const payload = await c.req.text();
  let event: Stripe.Event;
  try {
    event = await stripe.webhooks.constructEventAsync(payload, signature, c.env.STRIPE_WEBHOOK_SECRET, undefined, Stripe.createSubtleCryptoProvider());
  } catch {
    return c.text("bad signature", 400);
  }

  switch (event.type) {
    case "checkout.session.completed": {
      const s = event.data.object;
      if (s.mode === "subscription" && typeof s.subscription === "string") {
        await syncSubscription(c.env, await stripe.subscriptions.retrieve(s.subscription));
      }
      break;
    }
    case "invoice.paid": {
      // 每个计费周期付款成功时发放积分（首次订阅和每次续费都会触发）
      const inv = event.data.object;
      const subId = inv.parent?.subscription_details?.subscription;
      if (!subId) break;
      const sub = await stripe.subscriptions.retrieve(typeof subId === "string" ? subId : subId.id);
      await syncSubscription(c.env, sub);
      const userId = sub.metadata?.user_id;
      const plan = planForPrice(c.env, sub.items.data[0]?.price.id);
      const periodEnd = (inv.lines.data[0]?.period.end ?? sub.items.data[0]?.current_period_end ?? 0) * 1000;
      if (userId && plan && inv.id) await grantPeriodCredits(c.env, userId, plan, periodEnd, `inv:${inv.id}`);
      break;
    }
    case "customer.subscription.updated":
      await syncSubscription(c.env, event.data.object);
      break;
    case "customer.subscription.deleted": {
      const sub = event.data.object;
      await syncSubscription(c.env, sub);
      if (sub.metadata?.user_id) await revokeSubscriptionCredits(c.env, sub.metadata.user_id);
      break;
    }
    case "charge.refunded":
    case "charge.dispute.created": {
      // 退款 / 拒付：收回订阅积分
      const obj = event.data.object as { customer?: string | { id: string } | null };
      const customerId = typeof obj.customer === "string" ? obj.customer : obj.customer?.id;
      const userId = customerId ? await userForCustomer(c.env, customerId) : null;
      if (userId) {
        await revokeSubscriptionCredits(c.env, userId);
        console.log(JSON.stringify({ evt: "billing_revoke", reason: event.type, user: userId.slice(0, 8) }));
      }
      break;
    }
  }
  return c.json({ received: true });
});
