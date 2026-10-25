package app

// 网页账户（TECH_DESIGN §10.3）：邮箱验证码登录、查看套餐和次数、升级 / 管理订阅、下线设备、删除账号。
// 纯表单、不用 JavaScript（Turnstile 除外），所以 CSP 可以很严。修改操作都要求同源 + CSRF 令牌。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/credits"
)

func (s *Server) accountHeaders(w http.ResponseWriter) {
	h := w.Header()
	csp := "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; " +
		// 升级 / 管理订阅提交后会重定向到 Stripe
		"form-action 'self' https://checkout.stripe.com https://billing.stripe.com"
	if s.cfg.TurnstileSiteKey != "" {
		csp += "; script-src https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; connect-src https://challenges.cloudflare.com"
	}
	h.Set("Content-Security-Policy", csp)
}

// accountPage 账户相关页面的外壳：顶栏（官网链接、语言切换）+ 卡片
func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, status int, l lang, title, body string) error {
	s.accountHeaders(w)
	other := map[lang]string{langEN: "zh", langZH: "en"}[l]
	site := ""
	if s.cfg.WebsiteURL != "" {
		site = `<a href="` + esc(s.cfg.WebsiteURL) + `">` + esc(tr(l, "acct.home")) + `</a>`
	}
	head := ""
	if s.cfg.TurnstileSiteKey != "" {
		head = `<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>`
	}
	html := page(l, title, body, head)
	html = strings.Replace(html, `<body><main class="card">`, `<body class="wide"><div class="topbar"><a class="brand" href="/account">NextReply</a><span>`+site+
		` · <a href="`+esc(r.URL.Path)+`?lang=`+other+`">`+esc(tr(l, "acct.switchLang"))+`</a></span></div><main class="card">`, 1)
	return writeHTML(w, status, html)
}

func notice(text string, bad bool) string {
	if text == "" {
		return ""
	}
	cls := "notice"
	if bad {
		cls += " bad"
	}
	return `<div class="` + cls + `">` + esc(text) + `</div>`
}

// 只允许预先定义的提示，避免通过链接参数在页面上显示任意文字
var accountMessages = map[string]bool{"deviceOut": true, "signedOutMsg": true, "deleteMismatch": true, "deleteSubFail": true, "badForm": true}

func fmtDate(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }

// ---------- 登录 ----------

func (s *Server) renderAccountLogin(w http.ResponseWriter, r *http.Request, l lang, email, devCode, errMsg string, codeStep bool) error {
	turnstile := ""
	if s.cfg.TurnstileSiteKey != "" && !codeStep {
		turnstile = `<div class="cf-turnstile" data-sitekey="` + esc(s.cfg.TurnstileSiteKey) + `" style="margin-top:12px"></div>`
	}
	var b strings.Builder
	b.WriteString(`<h1>` + esc(tr(l, "acct.title")) + `</h1><p>` + esc(tr(l, "acct.signInIntro")) + `</p>`)
	b.WriteString(notice(errMsg, true))
	if msg := r.URL.Query().Get("msg"); accountMessages[msg] && errMsg == "" {
		b.WriteString(notice(tr(l, "acct."+msg), msg != "signedOutMsg"))
	}
	if !codeStep {
		b.WriteString(`<form method="post" action="/account/login">
  <label for="email">` + esc(tr(l, "login.email")) + `</label>
  <input id="email" name="email" type="email" autocomplete="email" required placeholder="you@example.com" value="` + esc(email) + `">
  ` + turnstile + `
  <button type="submit">` + esc(tr(l, "login.send")) + `</button>
</form>
<p class="muted" style="margin-top:16px">` + esc(tr(l, "acct.newAccount")) + `</p>`)
	} else {
		dev := ""
		if devCode != "" {
			dev = `<div class="dev">` + esc(tr(l, "otp.devCode", devCode)) + `</div>`
		}
		b.WriteString(`<form method="post" action="/account/verify">
  <input type="hidden" name="email" value="` + esc(email) + `">
  <label for="code">` + esc(tr(l, "login.sentTo")) + ` ` + esc(email) + `</label>
  <input id="code" name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required placeholder="` + esc(tr(l, "login.codeHint")) + `" autofocus>
  ` + dev + `
  <button type="submit">` + esc(tr(l, "login.submit")) + `</button>
</form>
<a class="button secondary" href="/account">` + esc(tr(l, "login.back")) + `</a>`)
	}
	return s.accountPage(w, r, http.StatusOK, l, tr(l, "acct.title"), b.String())
}

// apiMessage 业务错误的提示文字（已按语言生成），用于在表单页显示；其他错误照常返回 500
func apiMessage(err error) (string, bool) {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Message, true
	}
	return "", false
}

func (s *Server) accountLogin(w http.ResponseWriter, r *http.Request) error {
	l := pickLang(r)
	if err := r.ParseForm(); err != nil || !s.validPost(r, nil) {
		return s.renderAccountLogin(w, r, l, "", "", tr(l, "acct.badForm"), false)
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	devCode, err := s.startOTP(r.Context(), l, email, r.PostFormValue("cf-turnstile-response"), s.clientIP(r))
	if err != nil {
		if msg, ok := apiMessage(err); ok {
			return s.renderAccountLogin(w, r, l, email, "", msg, false)
		}
		return err
	}
	return s.renderAccountLogin(w, r, l, email, devCode, "", true)
}

func (s *Server) accountVerify(w http.ResponseWriter, r *http.Request) error {
	l := pickLang(r)
	if err := r.ParseForm(); err != nil || !s.validPost(r, nil) {
		return s.renderAccountLogin(w, r, l, "", "", tr(l, "acct.badForm"), false)
	}
	ctx := r.Context()
	email := strings.TrimSpace(r.PostFormValue("email"))
	if err := s.consumeOTP(ctx, l, email, strings.TrimSpace(r.PostFormValue("code"))); err != nil {
		if msg, ok := apiMessage(err); ok {
			// 验证码过期或错误次数太多时回到第一步重新获取
			restart := msg == tr(l, "otp.expired") || msg == tr(l, "otp.locked")
			return s.renderAccountLogin(w, r, l, email, "", msg, !restart)
		}
		return err
	}
	// 网页登录只创建 / 找到账号，不绑定设备、不发注册赠送：赠送在第一次从桌面端登录时按设备规则发放
	userID, err := s.findOrCreateUser(ctx, l, email)
	if err != nil {
		if msg, ok := apiMessage(err); ok {
			return s.renderAccountLogin(w, r, l, email, "", msg, false)
		}
		return err
	}
	if err := s.createWebSession(ctx, w, userID); err != nil {
		return err
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
	return nil
}

// ---------- 账户页 ----------

type accountDevice struct {
	ID       string
	LastSeen int64
	Sessions int
}

func (s *Server) accountHome(w http.ResponseWriter, r *http.Request) error {
	l := pickLang(r)
	rememberLang(w, r)
	ws, err := s.webUser(r)
	if err != nil {
		return err
	}
	if ws == nil {
		return s.renderAccountLogin(w, r, l, "", "", "", false)
	}
	ctx := r.Context()
	now := s.nowMs()

	sub, err := getSubscription(ctx, s.db, ws.UserID)
	if err != nil {
		return err
	}
	planID := planFromSubscription(sub, now)
	plan := s.settings.Plan(planID)
	bal, err := s.credits.Balance(ctx, credits.UserOwner(ws.UserID), plan.DailyRefill)
	if err != nil {
		return err
	}
	var last30 int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events WHERE owner = ? AND status = 'ok' AND created_at >= ?`,
		credits.UserOwner(ws.UserID), now-30*24*time.Hour.Milliseconds()).Scan(&last30); err != nil {
		return err
	}
	devices, err := s.accountDevices(ctx, ws.UserID)
	if err != nil {
		return err
	}
	csrf := `<input type="hidden" name="csrf" value="` + esc(s.csrfToken(ws)) + `">`

	var b strings.Builder
	if msg := r.URL.Query().Get("msg"); accountMessages[msg] {
		b.WriteString(notice(tr(l, "acct."+msg), msg == "deleteMismatch" || msg == "deleteSubFail" || msg == "badForm"))
	}
	b.WriteString(`<h1>` + esc(tr(l, "acct.title")) + `</h1><p>` + esc(tr(l, "acct.signedInAs")) + ` <b>` + esc(ws.Email) + `</b></p>`)
	b.WriteString(`<div class="stats">
  <div class="stat"><div class="k">` + esc(tr(l, "acct.plan")) + `</div><div class="v">` + esc(tr(l, "plan."+string(planID))) + `</div></div>
  <div class="stat"><div class="k">` + esc(tr(l, "acct.replies")) + `</div><div class="v">` + fmt.Sprint(bal.Total) + `</div></div>
  <div class="stat"><div class="k">` + esc(tr(l, "acct.today")) + `</div><div class="v">` + fmt.Sprint(bal.UsedToday) + `</div><div class="s">` + esc(tr(l, "acct.of", plan.DailyCap)) + `</div></div>
  <div class="stat"><div class="k">` + esc(tr(l, "acct.last30")) + `</div><div class="v">` + fmt.Sprint(last30) + `</div></div>
</div>`)

	b.WriteString(`<h2>` + esc(tr(l, "acct.buckets")) + `</h2><ul class="rows">`)
	if len(bal.Buckets) == 0 {
		b.WriteString(`<li class="muted">` + esc(tr(l, "acct.noCredits")) + `</li>`)
	}
	for _, bk := range bal.Buckets {
		exp := tr(l, "acct.noExpiry")
		if bk.ExpiresAt != nil {
			exp = tr(l, "acct.expires", fmtDate(*bk.ExpiresAt))
		}
		b.WriteString(`<li><span>` + esc(tr(l, "bucket."+string(bk.Bucket))) + ` <span class="muted">· ` + esc(exp) + `</span></span><b>` + fmt.Sprint(bk.Remaining) + `</b></li>`)
	}
	b.WriteString(`</ul>`)

	b.WriteString(`<h2>` + esc(tr(l, "acct.subscription")) + `</h2>`)
	if planID != credits.PlanFree && sub != nil {
		line := ""
		if sub.CurrentPeriodEnd.Valid {
			key := "acct.renews"
			if sub.CancelAtPeriodEnd {
				key = "acct.ends"
			}
			line = tr(l, key, fmtDate(sub.CurrentPeriodEnd.Int64))
		}
		b.WriteString(`<p>` + esc(tr(l, "plan."+sub.Plan)) + ` · ` + esc(line) + `</p>`)
		if sub.Status == "past_due" {
			b.WriteString(notice(tr(l, "acct.pastDue"), true))
		}
		b.WriteString(`<form method="post" action="/account/portal">` + csrf + `<button type="submit" class="secondary">` + esc(tr(l, "acct.manage")) + `</button></form>`)
	} else {
		for _, id := range credits.PaidPlans {
			p := s.settings.Plan(id)
			b.WriteString(`<form method="post" action="/account/upgrade">` + csrf + `<input type="hidden" name="plan" value="` + string(id) + `">
<button type="submit">` + esc(tr(l, "acct.upgradeTo", tr(l, "plan."+string(id)), p.PriceUSD)) + `</button>
<p class="muted" style="margin:6px 0 0">` + esc(tr(l, "acct.upgradeDesc", p.MonthlyCredits, p.DailyCap)) + `</p></form>`)
		}
	}

	b.WriteString(`<h2>` + esc(tr(l, "acct.devices")) + `</h2><ul class="rows">`)
	if len(devices) == 0 {
		b.WriteString(`<li class="muted">` + esc(tr(l, "acct.noDevices")) + `</li>`)
	}
	for _, d := range devices {
		state := tr(l, "acct.signedOut")
		action := ""
		if d.Sessions > 0 {
			state = tr(l, "acct.signedIn")
			action = `<form class="inline" method="post" action="/account/devices/` + esc(d.ID) + `/signout">` + csrf + `<button type="submit" class="inline secondary">` + esc(tr(l, "acct.signOutDevice")) + `</button></form>`
		}
		b.WriteString(`<li><span>` + esc(tr(l, "acct.device", d.ID[max(0, len(d.ID)-6):])) + ` <span class="muted">· ` + esc(tr(l, "acct.lastActive", fmtDate(d.LastSeen))) + ` · ` + esc(state) + `</span></span>` + action + `</li>`)
	}
	b.WriteString(`</ul>`)

	b.WriteString(`<form method="post" action="/account/logout">` + csrf + `<button type="submit" class="secondary">` + esc(tr(l, "acct.logout")) + `</button></form>`)
	b.WriteString(`<div class="danger-zone"><h2>` + esc(tr(l, "acct.danger")) + `</h2><p>` + esc(tr(l, "acct.dangerIntro")) + `</p>
<a class="button secondary" href="/account/delete">` + esc(tr(l, "acct.deleteTitle")) + `</a></div>`)
	return s.accountPage(w, r, http.StatusOK, l, tr(l, "acct.title"), b.String())
}

func (s *Server) accountDevices(ctx context.Context, userID string) ([]accountDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.last_seen,
		(SELECT COUNT(*) FROM refresh_tokens r WHERE r.device_id = d.id AND r.user_id = d.user_id AND r.revoked_at IS NULL AND r.expires_at > ?)
		FROM devices d WHERE d.user_id = ? ORDER BY d.last_seen DESC`, s.nowMs(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []accountDevice
	for rows.Next() {
		var d accountDevice
		if err := rows.Scan(&d.ID, &d.LastSeen, &d.Sessions); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- 修改操作 ----------

// requireWebPost 已登录 + 同源 + CSRF；不满足时重定向回账户页
func (s *Server) requireWebPost(w http.ResponseWriter, r *http.Request) (*webSession, bool, error) {
	ws, err := s.webUser(r)
	if err != nil {
		return nil, false, err
	}
	if ws == nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return nil, false, nil
	}
	if err := r.ParseForm(); err != nil || !s.validPost(r, ws) {
		http.Redirect(w, r, "/account?msg=badForm", http.StatusSeeOther)
		return nil, false, nil
	}
	return ws, true, nil
}

func (s *Server) accountLogout(w http.ResponseWriter, r *http.Request) error {
	ws, ok, err := s.requireWebPost(w, r)
	if err != nil || !ok {
		return err
	}
	if err := s.endWebSession(r.Context(), w, ws); err != nil {
		return err
	}
	http.Redirect(w, r, "/account?msg=signedOutMsg", http.StatusSeeOther)
	return nil
}

// accountUpgrade / accountPortal 复用桌面端的结账流程：签一次性票据，跳到 /billing/*
func (s *Server) accountUpgrade(w http.ResponseWriter, r *http.Request) error {
	ws, ok, err := s.requireWebPost(w, r)
	if err != nil || !ok {
		return err
	}
	plan := r.PostFormValue("plan")
	if !credits.IsPaidPlan(plan) {
		http.Redirect(w, r, "/account?msg=badForm", http.StatusSeeOther)
		return nil
	}
	ticket, err := s.createTicket(r.Context(), ws.UserID, "checkout")
	if err != nil {
		return err
	}
	http.Redirect(w, r, "/billing/checkout?ticket="+ticket+"&plan="+plan, http.StatusSeeOther)
	return nil
}

func (s *Server) accountPortal(w http.ResponseWriter, r *http.Request) error {
	ws, ok, err := s.requireWebPost(w, r)
	if err != nil || !ok {
		return err
	}
	ticket, err := s.createTicket(r.Context(), ws.UserID, "portal")
	if err != nil {
		return err
	}
	http.Redirect(w, r, "/billing/portal?ticket="+ticket, http.StatusSeeOther)
	return nil
}

func (s *Server) accountDeviceSignout(w http.ResponseWriter, r *http.Request) error {
	ws, ok, err := s.requireWebPost(w, r)
	if err != nil || !ok {
		return err
	}
	// 只能下线自己账号下的设备
	if err := s.revokeDevice(r.Context(), ws.UserID, r.PathValue("id")); err != nil {
		return err
	}
	http.Redirect(w, r, "/account?msg=deviceOut", http.StatusSeeOther)
	return nil
}

func (s *Server) accountDeleteForm(w http.ResponseWriter, r *http.Request) error {
	l := pickLang(r)
	ws, err := s.webUser(r)
	if err != nil {
		return err
	}
	if ws == nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return nil
	}
	var items strings.Builder
	for _, it := range strings.Split(tr(l, "acct.deleteItems"), "|") {
		items.WriteString(`<li>` + esc(it) + `</li>`)
	}
	body := `<h1>` + esc(tr(l, "acct.deleteTitle")) + `</h1>
<p>` + esc(tr(l, "acct.deleteWhat")) + `</p>
<ul style="color:var(--muted);font-size:14px;line-height:1.6;padding-left:20px">` + items.String() + `</ul>
<form method="post" action="/account/delete">
  <input type="hidden" name="csrf" value="` + esc(s.csrfToken(ws)) + `">
  <label for="confirm">` + esc(tr(l, "acct.deleteConfirm")) + ` (` + esc(ws.Email) + `)</label>
  <input id="confirm" name="confirm" type="email" autocomplete="off" required>
  <button type="submit" class="danger">` + esc(tr(l, "acct.deleteButton")) + `</button>
</form>
<a class="button secondary" href="/account">` + esc(tr(l, "acct.cancel")) + `</a>`
	return s.accountPage(w, r, http.StatusOK, l, tr(l, "acct.deleteTitle"), body)
}

func (s *Server) accountDelete(w http.ResponseWriter, r *http.Request) error {
	l := pickLang(r)
	ws, ok, err := s.requireWebPost(w, r)
	if err != nil || !ok {
		return err
	}
	if normalizeEmail(r.PostFormValue("confirm")) != normalizeEmail(ws.Email) {
		http.Redirect(w, r, "/account?msg=deleteMismatch", http.StatusSeeOther)
		return nil
	}
	if err := s.DeleteUser(r.Context(), ws.UserID); err != nil {
		if errors.Is(err, ErrSubscriptionCancel) {
			http.Redirect(w, r, "/account?msg=deleteSubFail", http.StatusSeeOther)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	s.setSessionCookie(w, "", -1)
	return s.accountPage(w, r, http.StatusOK, l, tr(l, "acct.deleted"),
		`<h1>`+esc(tr(l, "acct.deleted"))+`</h1><p>`+esc(tr(l, "acct.deletedMsg"))+`</p>`)
}
