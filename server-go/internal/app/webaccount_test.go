package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
)

// browser 带 cookie、不自动跟随重定向的网页客户端，表单提交带同源 Origin
type browser struct {
	t      *testing.T
	h      *harness
	client *http.Client
	lang   string
}

func newBrowser(h *harness) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: h.t, h: h, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) send(req *http.Request) (int, string, string) {
	b.t.Helper()
	if b.lang != "" {
		req.Header.Set("Accept-Language", b.lang)
	}
	r, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(body), r.Header.Get("Location")
}

func (b *browser) get(path string) (int, string, string) {
	req, _ := http.NewRequest("GET", b.h.ts.URL+path, nil)
	return b.send(req)
}

func (b *browser) post(path string, form url.Values, headers ...string) (int, string, string) {
	req, _ := http.NewRequest("POST", b.h.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.h.ts.URL)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return b.send(req)
}

var (
	devCodeRe = regexp.MustCompile(`(?:your code is|验证码) (\d{6})`)
	csrfRe    = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)
)

// webLogin 网页邮箱登录，返回登录后的账户页
func (b *browser) webLogin(email string) string {
	b.t.Helper()
	code, body, _ := b.post("/account/login", url.Values{"email": {email}})
	m := devCodeRe.FindStringSubmatch(body)
	if code != 200 || m == nil {
		b.t.Fatalf("login start: %d %s", code, body)
	}
	code, _, loc := b.post("/account/verify", url.Values{"email": {email}, "code": {m[1]}})
	if code != 303 || loc != "/account" {
		b.t.Fatalf("verify: %d %s", code, loc)
	}
	_, page, _ := b.get("/account")
	if !strings.Contains(page, email) {
		b.t.Fatal("account page doesn't show email")
	}
	return page
}

func (b *browser) csrf() string {
	_, page, _ := b.get("/account")
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		b.t.Fatal("no csrf token")
	}
	return m[1]
}

func balanceOf(t *testing.T, h *harness, userID string) int {
	b, err := h.srv.Credits().Balance(context.Background(), credits.UserOwner(userID), 0)
	if err != nil {
		t.Fatal(err)
	}
	return b.Total
}

func userIDByEmail(h *harness, email string) string {
	var id string
	h.db.QueryRow(`SELECT id FROM users WHERE email_norm = ?`, normalizeEmail(email)).Scan(&id)
	return id
}

func TestLanguageSelection(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(h)
	q := "/auth/login?" + url.Values{"device_id": {deviceID}, "redirect_uri": {"http://127.0.0.1:5000/callback"}, "state": {"s"}, "code_challenge": {"c"}}.Encode()
	if _, body, _ := b.get(q); !strings.Contains(body, "Sign in to NextReply") || !strings.Contains(body, `lang="en"`) {
		t.Fatal("default should be English")
	}
	b.lang = "zh-CN,zh;q=0.9,en;q=0.8"
	if _, body, _ := b.get(q); !strings.Contains(body, "登录 NextReply") || !strings.Contains(body, `lang="zh-CN"`) {
		t.Fatal("Accept-Language zh")
	}
	// ?lang= 覆盖并记住
	b.get("/account?lang=en")
	if _, body, _ := b.get("/account"); !strings.Contains(body, "Sign in with your email") {
		t.Fatal("lang cookie not remembered")
	}
	// 接口错误信息跟随语言
	b.lang = "zh"
	b.client.Jar, _ = cookiejar.New(nil)
	if _, body, _ := b.post("/account/login", url.Values{"email": {"bad"}}); !strings.Contains(body, "邮箱格式不正确") {
		t.Fatal("localized error")
	}
}

func TestWebLoginAndBonusRules(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(h)
	if _, body, _ := b.get("/account"); !strings.Contains(body, `action="/account/login"`) {
		t.Fatal("anonymous should see login form")
	}
	page := b.webLogin("web@example.com")
	uid := userIDByEmail(h, "web@example.com")
	// 网页注册不发注册赠送，只有免费用户的每日补充
	if balanceOf(t, h, uid) != 5 || !strings.Contains(page, "Free") {
		t.Fatal("web sign-up must not grant the bonus", balanceOf(t, h, uid))
	}
	if !strings.Contains(page, "No desktop devices yet") || !strings.Contains(page, "Upgrade to Pro") {
		t.Fatal("account page content")
	}
	// 第一次从桌面端登录时发放
	h.register(deviceID)
	h.login("web@example.com", deviceID)
	if balanceOf(t, h, uid) != 5+credits.SignupBonus {
		t.Fatal("bonus on first desktop sign-in", balanceOf(t, h, uid))
	}
	_, page, _ = b.get("/account")
	if !strings.Contains(page, "Device …"+deviceID[len(deviceID)-6:]) || !strings.Contains(page, "signed in") {
		t.Fatal("device not listed")
	}

	// 验证码用过后再提交：提示已过期并回到第一步重新获取
	code, body, _ := b.post("/account/verify", url.Values{"email": {"web@example.com"}, "code": {"000000"}})
	if code != 200 || !strings.Contains(body, "expired") || !strings.Contains(body, `action="/account/login"`) {
		t.Fatal("used code", code)
	}

	// 另一个邮箱输错验证码：提示错误，留在第二步
	b2 := newBrowser(h)
	b2.post("/account/login", url.Values{"email": {"typo@example.com"}})
	code, body, _ = b2.post("/account/verify", url.Values{"email": {"typo@example.com"}, "code": {"000000"}})
	if code != 200 || !strings.Contains(body, "correct") || !strings.Contains(body, `action="/account/verify"`) {
		t.Fatal("wrong code", code)
	}
}

func TestWebAccountActions(t *testing.T) {
	h := newHarness(t)
	h.register(deviceID)
	tok := h.login("act@example.com", deviceID)
	b := newBrowser(h)
	b.webLogin("act@example.com")

	// CSRF：没有令牌 / 跨站提交都会被拒绝
	if _, _, loc := b.post("/account/upgrade", url.Values{"plan": {"pro"}}); loc != "/account?msg=badForm" {
		t.Fatal("missing csrf", loc)
	}
	csrf := b.csrf()
	if _, _, loc := b.post("/account/upgrade", url.Values{"plan": {"pro"}, "csrf": {csrf}}, "Origin", "https://evil.example"); loc != "/account?msg=badForm" {
		t.Fatal("cross origin", loc)
	}
	if _, _, loc := b.post("/account/upgrade", url.Values{"plan": {"pro"}, "csrf": {csrf}}, "Sec-Fetch-Site", "cross-site"); loc != "/account?msg=badForm" {
		t.Fatal("cross site", loc)
	}

	// 升级：跳转到结账流程（开发模式为模拟支付）
	_, _, loc := b.post("/account/upgrade", url.Values{"plan": {"pro"}, "csrf": {csrf}})
	if !strings.HasPrefix(loc, "/billing/checkout?ticket=") {
		t.Fatal(loc)
	}
	_, mock, _ := b.get(loc)
	ticket := regexp.MustCompile(`name="ticket" value="([^"]+)"`).FindStringSubmatch(mock)[1]
	b.post("/billing/dev/complete", url.Values{"ticket": {ticket}, "plan": {"pro"}})
	_, page, _ := b.get("/account")
	if !strings.Contains(page, "Pro") || !strings.Contains(page, "Manage subscription") {
		t.Fatal("subscription not shown")
	}
	if _, _, loc = b.post("/account/portal", url.Values{"csrf": {csrf}}); !strings.HasPrefix(loc, "/billing/portal?ticket=") {
		t.Fatal(loc)
	}

	// 下线设备：桌面端的刷新令牌失效
	if _, _, loc = b.post("/account/devices/"+deviceID+"/signout", url.Values{"csrf": {csrf}}); loc != "/account?msg=deviceOut" {
		t.Fatal(loc)
	}
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": tok["refresh_token"], "device_id": deviceID}); r.status != 401 {
		t.Fatal("device still signed in")
	}
	// 不能下线别人的设备（只作用于自己的账号）
	h.register("device-other-000000000001")
	other := h.login("other@example.com", "device-other-000000000001")
	b.post("/account/devices/device-other-000000000001/signout", url.Values{"csrf": {csrf}})
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": other["refresh_token"], "device_id": "device-other-000000000001"}); r.status != 200 {
		t.Fatal("signed out someone else's device")
	}

	// 退出网页登录
	if _, _, loc = b.post("/account/logout", url.Values{"csrf": {csrf}}); loc != "/account?msg=signedOutMsg" {
		t.Fatal(loc)
	}
	if _, body, _ := b.get("/account"); !strings.Contains(body, `action="/account/login"`) {
		t.Fatal("still signed in")
	}
}

func TestDeleteAccount(t *testing.T) {
	h := newHarness(t)
	h.register(deviceID)
	tok := h.login("bye@example.com", deviceID)
	h.do("POST", "/v1/reply", tok["access_token"].(string), replyBody)
	uid := userIDByEmail(h, "bye@example.com")
	b := newBrowser(h)
	b.webLogin("bye@example.com")
	csrf := b.csrf()

	if _, body, _ := b.get("/account/delete"); !strings.Contains(body, "be undone") {
		t.Fatal("confirm page")
	}
	if _, _, loc := b.post("/account/delete", url.Values{"csrf": {csrf}, "confirm": {"someone@else.com"}}); loc != "/account?msg=deleteMismatch" {
		t.Fatal("mismatch", loc)
	}
	code, body, _ := b.post("/account/delete", url.Values{"csrf": {csrf}, "confirm": {"Bye@Example.com"}})
	if code != 200 || !strings.Contains(body, "Account deleted") {
		t.Fatal(code, body)
	}

	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, uid).Scan(&n)
	var devOwner *string
	h.db.QueryRow(`SELECT user_id FROM devices WHERE id = ?`, deviceID).Scan(&devOwner)
	var grants, events, tomb, sessions int
	h.db.QueryRow(`SELECT COUNT(*) FROM credit_grants WHERE owner = ?`, "u:"+uid).Scan(&grants)
	h.db.QueryRow(`SELECT COUNT(*) FROM usage_events WHERE owner = 'u:deleted' AND ip IS NULL AND device_id IS NULL`).Scan(&events)
	h.db.QueryRow(`SELECT COUNT(*) FROM deleted_emails`).Scan(&tomb)
	h.db.QueryRow(`SELECT COUNT(*) FROM web_sessions`).Scan(&sessions)
	if n != 0 || devOwner != nil || grants != 0 || events != 1 || tomb != 1 || sessions != 0 {
		t.Fatal(n, devOwner, grants, events, tomb, sessions)
	}
	// 桌面端会话失效
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": tok["refresh_token"], "device_id": deviceID}); r.status != 401 {
		t.Fatal("desktop session survived deletion")
	}
	// 同一邮箱换一台新设备重新注册：不再送注册赠送
	h.register("device-new-machine-000001")
	h.login("bye@example.com", "device-new-machine-000001")
	var bonus int
	h.db.QueryRow(`SELECT COUNT(*) FROM credit_grants WHERE owner = ? AND source_ref = 'signup_bonus'`, "u:"+userIDByEmail(h, "bye@example.com")).Scan(&bonus)
	if bonus != 0 {
		t.Fatal("re-registered email got the bonus again")
	}
}

func TestDeleteAccountCancelsStripe(t *testing.T) {
	var cancelled []string
	fail := false
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/subscriptions/") {
			if fail {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"error":{"type":"api_error","message":"boom"}}`)
				return
			}
			cancelled = append(cancelled, strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/"))
			fmt.Fprint(w, `{"id":"sub_1","status":"canceled"}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer fake.Close()
	orig := stripeBaseForTest
	stripeBaseForTest = fake.URL
	defer func() { stripeBaseForTest = orig }()

	h := newHarness(t, func(c *config.Config) { c.StripeSecretKey, c.StripeWebhookSecret = "sk_test_x", "whsec_test" })
	h.register(deviceID)
	h.login("paid@example.com", deviceID)
	uid := userIDByEmail(h, "paid@example.com")
	h.srv.upsertSubscription(context.Background(), subscriptionUpsert{UserID: uid, Plan: "pro", Status: "active", Provider: "stripe",
		CustomerID: "cus_1", SubscriptionID: "sub_1", PeriodEnd: time.Now().Add(24 * time.Hour).UnixMilli()})

	b := newBrowser(h)
	b.webLogin("paid@example.com")
	csrf := b.csrf()

	// Stripe 取消失败：不删除账号
	fail = true
	if _, _, loc := b.post("/account/delete", url.Values{"csrf": {csrf}, "confirm": {"paid@example.com"}}); loc != "/account?msg=deleteSubFail" {
		t.Fatal(loc)
	}
	if userIDByEmail(h, "paid@example.com") == "" {
		t.Fatal("deleted despite failed cancellation")
	}
	fail = false
	if code, _, _ := b.post("/account/delete", url.Values{"csrf": {csrf}, "confirm": {"paid@example.com"}}); code != 200 || len(cancelled) != 1 || cancelled[0] != "sub_1" {
		t.Fatal("subscription not cancelled", code, cancelled)
	}

	// 之后 Stripe 发来的事件不能为已删除的账号重新写入数据
	payload := `{"id":"evt_9","type":"customer.subscription.deleted","data":{"object":{"id":"sub_1","status":"canceled","customer":"cus_1","metadata":{"user_id":"` + uid + `","plan":"pro"},"items":{"data":[{"current_period_end":1,"price":{"id":"price_x"}}]}}}}`
	if r := h.do("POST", "/billing/webhook", "", jsonRaw(payload), "Stripe-Signature", signStripe([]byte(payload), "whsec_test", time.Now())); r.status != 200 {
		t.Fatal("webhook for deleted user should be acknowledged", r.status, r.raw)
	}
	var subs int
	h.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&subs)
	if subs != 0 {
		t.Fatal("subscription recreated for deleted user")
	}
}

func TestSecureCookieOnHTTPS(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PublicURL = "https://nextreply-api.example.com" })
	rec := httptest.NewRecorder()
	if err := h.srv.createWebSession(context.Background(), rec, "user-1"); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != "__Host-nr_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("%+v", c)
	}
}

// jsonRaw 让 harness.do 原样发送已签名的 JSON
type jsonRaw string

func (j jsonRaw) MarshalJSON() ([]byte, error) { return []byte(j), nil }
