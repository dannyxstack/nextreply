package app

// 订阅支付。桌面端申请一次性网页票据 → 浏览器打开结账页 → Stripe Checkout → webhook 回调发放积分。
// 没有配置 Stripe 时，开发模式下提供模拟支付页面，方便本地完整测试订阅流程。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
)

const (
	ticketTTL = 10 * time.Minute
	devPeriod = 30 * 24 * time.Hour
	// 订阅积分在周期结束后多保留几天，避免续费扣款稍有延迟时用户突然没额度
	creditGrace = 3 * 24 * time.Hour
)

func (s *Server) publicURL(path string) string { return s.cfg.PublicURL + path }

func (s *Server) priceFor(plan string) string {
	if plan == string(credits.PlanPro) {
		return s.cfg.StripePricePro
	}
	return s.cfg.StripePriceProPlus
}

func (s *Server) planForPrice(priceID string) string {
	switch {
	case priceID == "":
		return ""
	case priceID == s.cfg.StripePricePro:
		return string(credits.PlanPro)
	case priceID == s.cfg.StripePriceProPlus:
		return string(credits.PlanProPlus)
	}
	return ""
}

func (s *Server) createTicket(ctx context.Context, userID, purpose string) (string, error) {
	ticket := authn.RandomToken()
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_tickets (ticket_hash, user_id, purpose, expires_at) VALUES (?, ?, ?, ?)`,
		authn.SHA256Hex(ticket), userID, purpose, s.nowMs()+ticketTTL.Milliseconds())
	return ticket, err
}

// useTicket 校验并消耗一次性票据，返回 user_id；无效时返回空串。
func (s *Server) useTicket(ctx context.Context, ticket, purpose string) (string, error) {
	if ticket == "" {
		return "", nil
	}
	hash := authn.SHA256Hex(ticket)
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM web_tickets WHERE ticket_hash = ? AND purpose = ? AND used = 0 AND expires_at >= ?`,
		hash, purpose, s.nowMs()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE web_tickets SET used = 1 WHERE ticket_hash = ? AND used = 0`, hash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", nil
	}
	return userID, nil
}

type subscriptionUpsert struct {
	UserID, Plan, Status, Provider string
	CustomerID, SubscriptionID     string
	PeriodStart, PeriodEnd         int64 // 毫秒；0 表示不更新
	CancelAtPeriodEnd              bool
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Server) upsertSubscription(ctx context.Context, u subscriptionUpsert) error {
	cancel := 0
	if u.CancelAtPeriodEnd {
		cancel = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscriptions (user_id, plan, status, provider, customer_id, subscription_id, current_period_start, current_period_end, cancel_at_period_end, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   plan = excluded.plan, status = excluded.status, provider = excluded.provider,
		   customer_id = COALESCE(excluded.customer_id, subscriptions.customer_id),
		   subscription_id = COALESCE(excluded.subscription_id, subscriptions.subscription_id),
		   current_period_start = COALESCE(excluded.current_period_start, subscriptions.current_period_start),
		   current_period_end = COALESCE(excluded.current_period_end, subscriptions.current_period_end),
		   cancel_at_period_end = excluded.cancel_at_period_end, updated_at = excluded.updated_at`,
		u.UserID, u.Plan, u.Status, u.Provider, nullStr(u.CustomerID), nullStr(u.SubscriptionID),
		nullInt(u.PeriodStart), nullInt(u.PeriodEnd), cancel, s.nowMs())
	return err
}

// grantPeriodCredits 为一个已付款的计费周期发放积分。sourceRef 用发票号保证同一周期只发一次。
func (s *Server) grantPeriodCredits(ctx context.Context, userID, plan string, periodEnd int64, sourceRef string) error {
	exp := periodEnd + creditGrace.Milliseconds()
	_, err := s.credits.Grant(ctx, credits.UserOwner(userID), credits.GrantInput{
		Bucket: credits.BucketSubscription, Amount: credits.Plans[credits.PlanID(plan)].MonthlyCredits,
		ExpiresAt: &exp, SourceRef: sourceRef, Reason: "subscription_" + plan,
	})
	return err
}

// revokeSubscriptionCredits 收回订阅积分（退款、拒付、订阅被立即终止）。
func (s *Server) revokeSubscriptionCredits(ctx context.Context, userID string) error {
	_, err := s.credits.Revoke(ctx, credits.UserOwner(userID), credits.BucketSubscription, "")
	return err
}

// ---------- 页面与接口 ----------

// billingLink 已登录的桌面端申请结账 / 管理订阅链接（用系统浏览器打开）。
func (s *Server) billingLink(w http.ResponseWriter, r *http.Request) error {
	caller, err := s.resolveCaller(r)
	if err != nil {
		return err
	}
	if caller.Kind != "user" {
		return apierr.WithDetails(apierr.Unauthorized, "Please sign in first.", map[string]any{"reason": "login_required"})
	}
	var body struct {
		Purpose string `json:"purpose"`
		Plan    string `json:"plan"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ctx := r.Context()
	if body.Purpose == "portal" {
		ticket, err := s.createTicket(ctx, caller.UserID, "portal")
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"url": s.publicURL("/billing/portal?ticket=" + ticket)})
	}
	if !credits.IsPaidPlan(body.Plan) {
		return apierr.New(apierr.BadRequest, "Unknown plan.")
	}
	ticket, err := s.createTicket(ctx, caller.UserID, "checkout")
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"url": s.publicURL("/billing/checkout?ticket=" + ticket + "&plan=" + body.Plan)})
}

func (s *Server) billingCheckout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	plan := r.URL.Query().Get("plan")
	if !credits.IsPaidPlan(plan) {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接无效", "未知的套餐。"))
	}
	userID, err := s.useTicket(ctx, r.URL.Query().Get("ticket"), "checkout")
	if err != nil {
		return err
	}
	if userID == "" {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接已失效", "请回到 NextReply 重新点击升级。"))
	}

	if sc := s.stripe(); sc != nil {
		price := s.priceFor(plan)
		if price == "" {
			return writeHTML(w, http.StatusServiceUnavailable, messagePage("暂不可用", "该套餐尚未开放购买。"))
		}
		var email string
		if err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sub, err := getSubscription(ctx, s.db, userID)
		if err != nil {
			return err
		}
		form := url.Values{
			"mode":                                 {"subscription"},
			"line_items[0][price]":                 {price},
			"line_items[0][quantity]":              {"1"},
			"client_reference_id":                  {userID},
			"metadata[user_id]":                    {userID},
			"metadata[plan]":                       {plan},
			"subscription_data[metadata][user_id]": {userID},
			"subscription_data[metadata][plan]":    {plan},
			"success_url":                          {s.publicURL("/billing/done?status=success")},
			"cancel_url":                           {s.publicURL("/billing/done?status=cancel")},
		}
		if sub != nil && sub.CustomerID.Valid {
			form.Set("customer", sub.CustomerID.String)
		} else if email != "" {
			form.Set("customer_email", email)
		}
		session, err := sc.createCheckout(ctx, form)
		if err != nil {
			slog.Error("stripe checkout failed", "err", err.Error())
			return writeHTML(w, http.StatusBadGateway, messagePage("暂不可用", "支付服务暂时不可用，请稍后再试。"))
		}
		target := session.URL
		if target == "" {
			target = s.publicURL("/billing/done?status=cancel")
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return nil
	}

	if !s.cfg.DevMode {
		return writeHTML(w, http.StatusServiceUnavailable, messagePage("暂不可用", "支付服务尚未开通。"))
	}
	p := credits.Plans[credits.PlanID(plan)]
	confirm, err := s.createTicket(ctx, userID, "dev_complete")
	if err != nil {
		return err
	}
	return writeHTML(w, http.StatusOK, page("模拟支付", fmt.Sprintf(`<h1>模拟支付（开发模式）</h1>
<p>未配置 Stripe，这里模拟一次订阅付款，不会产生真实扣费。</p>
<div class="plan"><b>%s</b> · $%.2f/月<br/><span style="color:var(--muted);font-size:13px">每月 %d 次，每天最多 %d 次</span></div>
<form method="post" action="/billing/dev/complete">
  <input type="hidden" name="ticket" value="%s" />
  <input type="hidden" name="plan" value="%s" />
  <button type="submit">模拟支付成功</button>
</form>`, esc(p.Label), p.PriceUSD, p.MonthlyCredits, p.DailyCap, esc(confirm), esc(plan)), ""))
}

// devComplete 开发模式：模拟付款成功 → 开通订阅并发放本周期积分。
func (s *Server) devComplete(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.DevMode || s.cfg.StripeSecretKey != "" {
		return writeHTML(w, http.StatusNotFound, messagePage("不可用", "仅开发模式可用。"))
	}
	ctx := r.Context()
	plan := r.PostFormValue("plan")
	userID, err := s.useTicket(ctx, r.PostFormValue("ticket"), "dev_complete")
	if err != nil {
		return err
	}
	if userID == "" || !credits.IsPaidPlan(plan) {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接已失效", "请回到 NextReply 重新点击升级。"))
	}
	now := s.nowMs()
	end := now + devPeriod.Milliseconds()
	if err := s.upsertSubscription(ctx, subscriptionUpsert{
		UserID: userID, Plan: plan, Status: "active", Provider: "dev",
		SubscriptionID: "dev_" + userID, PeriodStart: now, PeriodEnd: end,
	}); err != nil {
		return err
	}
	if err := s.grantPeriodCredits(ctx, userID, plan, end, fmt.Sprintf("dev:%s:%d", userID, now)); err != nil {
		return err
	}
	http.Redirect(w, r, "/billing/done?status=success", http.StatusSeeOther)
	return nil
}

func (s *Server) billingPortal(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	userID, err := s.useTicket(ctx, r.URL.Query().Get("ticket"), "portal")
	if err != nil {
		return err
	}
	if userID == "" {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接已失效", "请回到 NextReply 重新打开。"))
	}
	sub, err := getSubscription(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if sc := s.stripe(); sc != nil && sub != nil && sub.CustomerID.Valid {
		session, err := sc.createPortal(ctx, sub.CustomerID.String, s.publicURL("/billing/done?status=portal"))
		if err != nil {
			slog.Error("stripe portal failed", "err", err.Error())
			return writeHTML(w, http.StatusBadGateway, messagePage("暂不可用", "支付服务暂时不可用，请稍后再试。"))
		}
		http.Redirect(w, r, session.URL, http.StatusSeeOther)
		return nil
	}
	if !s.cfg.DevMode || sub == nil {
		return writeHTML(w, http.StatusNotFound, messagePage("没有订阅", "当前账号没有可管理的订阅。"))
	}
	t1, err := s.createTicket(ctx, userID, "dev_cancel")
	if err != nil {
		return err
	}
	t2, err := s.createTicket(ctx, userID, "dev_expire")
	if err != nil {
		return err
	}
	label := sub.Plan
	if p, ok := credits.Plans[credits.PlanID(sub.Plan)]; ok {
		label = p.Label
	}
	cancelNote := ""
	if sub.CancelAtPeriodEnd {
		cancelNote = " · 到期后取消"
	}
	return writeHTML(w, http.StatusOK, page("管理订阅", fmt.Sprintf(`<h1>管理订阅（开发模式）</h1>
<p>当前：%s · %s%s</p>
<form method="post" action="/billing/dev/cancel"><input type="hidden" name="ticket" value="%s" /><button type="submit" class="secondary">到期后取消订阅</button></form>
<form method="post" action="/billing/dev/expire"><input type="hidden" name="ticket" value="%s" /><button type="submit" class="secondary">立即结束（模拟到期 / 退款）</button></form>`,
		esc(label), esc(sub.Status), cancelNote, esc(t1), esc(t2)), ""))
}

func (s *Server) devCancel(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.DevMode {
		return writeHTML(w, http.StatusNotFound, messagePage("不可用", "仅开发模式可用。"))
	}
	userID, err := s.useTicket(r.Context(), r.PostFormValue("ticket"), "dev_cancel")
	if err != nil {
		return err
	}
	if userID == "" {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接已失效", "请重新打开。"))
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE subscriptions SET cancel_at_period_end = 1, updated_at = ? WHERE user_id = ?`, s.nowMs(), userID); err != nil {
		return err
	}
	return writeHTML(w, http.StatusOK, messagePage("已设置到期后取消", "本周期内仍可使用剩余额度。回到 NextReply 刷新即可看到。"))
}

func (s *Server) devExpire(w http.ResponseWriter, r *http.Request) error {
	if !s.cfg.DevMode {
		return writeHTML(w, http.StatusNotFound, messagePage("不可用", "仅开发模式可用。"))
	}
	userID, err := s.useTicket(r.Context(), r.PostFormValue("ticket"), "dev_expire")
	if err != nil {
		return err
	}
	if userID == "" {
		return writeHTML(w, http.StatusBadRequest, messagePage("链接已失效", "请重新打开。"))
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE subscriptions SET status = 'canceled', updated_at = ? WHERE user_id = ?`, s.nowMs(), userID); err != nil {
		return err
	}
	if err := s.revokeSubscriptionCredits(r.Context(), userID); err != nil {
		return err
	}
	return writeHTML(w, http.StatusOK, messagePage("订阅已结束", "已回到免费套餐，订阅积分已收回。回到 NextReply 刷新即可看到。"))
}

func (s *Server) billingDone(w http.ResponseWriter, r *http.Request) error {
	switch r.URL.Query().Get("status") {
	case "success":
		return writeHTML(w, http.StatusOK, messagePage("支付成功", "订阅已开通，回到 NextReply 即可使用。积分可能需要几秒钟到账。"))
	case "portal":
		return writeHTML(w, http.StatusOK, messagePage("已返回", "回到 NextReply 刷新即可看到最新状态。"))
	}
	return writeHTML(w, http.StatusOK, messagePage("已取消支付", "没有产生任何扣费，可以回到 NextReply。"))
}

// ---------- Stripe webhook ----------

func mapStripeStatus(st string) string {
	switch st {
	case "active", "trialing":
		return "active"
	case "past_due", "unpaid":
		return "past_due"
	}
	return "canceled"
}

func (s *Server) syncSubscription(ctx context.Context, sub *stripeSubscription) error {
	userID := sub.Metadata["user_id"]
	plan := s.planForPrice(sub.priceID())
	if plan == "" && credits.IsPaidPlan(sub.Metadata["plan"]) {
		plan = sub.Metadata["plan"]
	}
	if userID == "" || plan == "" {
		return nil
	}
	start, end := sub.period()
	return s.upsertSubscription(ctx, subscriptionUpsert{
		UserID: userID, Plan: plan, Status: mapStripeStatus(sub.Status), Provider: "stripe",
		CustomerID: string(sub.Customer), SubscriptionID: sub.ID,
		PeriodStart: start * 1000, PeriodEnd: end * 1000, CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
	})
}

func (s *Server) stripeWebhook(w http.ResponseWriter, r *http.Request) error {
	sc := s.stripe()
	if sc == nil || s.cfg.StripeWebhookSecret == "" {
		http.Error(w, "not configured", http.StatusNotFound)
		return nil
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if err := verifyStripeSignature(payload, r.Header.Get("Stripe-Signature"), s.cfg.StripeWebhookSecret, s.now()); err != nil {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return nil
	}
	var event stripeEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return nil
	}
	ctx := r.Context()
	// 处理失败时返回 5xx，Stripe 会自动重试；所有处理都是幂等的
	if err := s.handleStripeEvent(ctx, sc, &event); err != nil {
		slog.Error("stripe webhook failed", "event", event.Type, "id", event.ID, "err", err.Error())
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return nil
	}
	return writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

func (s *Server) handleStripeEvent(ctx context.Context, sc *stripeClient, event *stripeEvent) error {
	switch event.Type {
	case "checkout.session.completed":
		var cs struct {
			Mode         string    `json:"mode"`
			Subscription stripeRef `json:"subscription"`
		}
		if err := json.Unmarshal(event.Data.Object, &cs); err != nil {
			return err
		}
		if cs.Mode != "subscription" || cs.Subscription == "" {
			return nil
		}
		sub, err := sc.getSubscription(ctx, string(cs.Subscription))
		if err != nil {
			return err
		}
		return s.syncSubscription(ctx, sub)

	case "invoice.paid":
		// 每个计费周期付款成功时发放积分（首次订阅和每次续费都会触发）
		var inv stripeInvoice
		if err := json.Unmarshal(event.Data.Object, &inv); err != nil {
			return err
		}
		subID := inv.subscriptionID()
		if subID == "" {
			return nil
		}
		sub, err := sc.getSubscription(ctx, subID)
		if err != nil {
			return err
		}
		if err := s.syncSubscription(ctx, sub); err != nil {
			return err
		}
		userID, plan := sub.Metadata["user_id"], s.planForPrice(sub.priceID())
		_, end := sub.period()
		if len(inv.Lines.Data) > 0 && inv.Lines.Data[0].Period.End > 0 {
			end = inv.Lines.Data[0].Period.End
		}
		if userID != "" && plan != "" && inv.ID != "" {
			return s.grantPeriodCredits(ctx, userID, plan, end*1000, "inv:"+inv.ID)
		}
		return nil

	case "customer.subscription.updated", "customer.subscription.deleted":
		var sub stripeSubscription
		if err := json.Unmarshal(event.Data.Object, &sub); err != nil {
			return err
		}
		if err := s.syncSubscription(ctx, &sub); err != nil {
			return err
		}
		if event.Type == "customer.subscription.deleted" && sub.Metadata["user_id"] != "" {
			return s.revokeSubscriptionCredits(ctx, sub.Metadata["user_id"])
		}
		return nil

	case "charge.refunded", "charge.dispute.created":
		// 退款 / 拒付：收回订阅积分
		var obj struct {
			Customer stripeRef `json:"customer"`
		}
		if err := json.Unmarshal(event.Data.Object, &obj); err != nil {
			return err
		}
		if obj.Customer == "" {
			return nil
		}
		var userID string
		err := s.db.QueryRowContext(ctx, `SELECT user_id FROM subscriptions WHERE customer_id = ?`, string(obj.Customer)).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.revokeSubscriptionCredits(ctx, userID); err != nil {
			return err
		}
		slog.Info("billing_revoke", "reason", event.Type, "user", userID[:min(8, len(userID))])
	}
	return nil
}
