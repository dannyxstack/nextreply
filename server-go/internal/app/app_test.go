package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
	"github.com/nextreply/server/internal/settings"
	"github.com/nextreply/server/internal/store"
)

const deviceID = "3f1c2b7e-9d4a-4c1e-8b2a-6f5d4e3c2b1a"

type fakeAI struct {
	status    ai.Status
	err       error
	calls     int
	lastModel string
}

func (f *fakeAI) Generate(ctx context.Context, in ai.Input) (*ai.Output, error) {
	f.calls++
	f.lastModel = in.Model
	if f.err != nil {
		return nil, f.err
	}
	out, _ := ai.Mock{}.Generate(ctx, in)
	if f.status != "" && f.status != ai.StatusOK {
		out.Result.Status, out.Result.Replies = f.status, []ai.Reply{}
	}
	return out, nil
}

type harness struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
	ai  *fakeAI
	db  *sql.DB
}

func newHarness(t *testing.T, mutate ...func(*config.Config)) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		TokenSecret: "test-secret-0123456789-abcdefghijklmn", Model: "mock",
		IPDailyQuota: 200, RegisterPerIP: 20, DevMode: true, PublicURL: "http://nextreply.test",
	}
	for _, m := range mutate {
		m(cfg)
	}
	fake := &fakeAI{}
	srv := New(cfg, db, fake)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, srv: srv, ts: ts, ai: fake, db: db}
}

type resp struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (h *harness) do(method, path, token string, body any, headers ...string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, raw: string(raw), header: r.Header}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (h *harness) postForm(path string, form url.Values) resp {
	h.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.PostForm(h.ts.URL+path, form)
	if err != nil {
		h.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, raw: string(raw), header: r.Header}
}

func errCode(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func num(v any) int { f, _ := v.(float64); return int(f) }

func (h *harness) register(id string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/device/register", "", map[string]any{"device_id": id})
	if r.status != 200 {
		h.t.Fatalf("register: %d %s", r.status, r.raw)
	}
	return r.body["token"].(string)
}

var replyBody = map[string]any{"image": "aGVsbG8=", "media_type": "image/jpeg", "locale": "zh-CN"}

func TestHealthAndNotFound(t *testing.T) {
	h := newHarness(t)
	if r := h.do("GET", "/v1/health", "", nil); r.status != 200 || r.body["ok"] != true {
		t.Fatal(r.raw)
	}
	if r := h.do("GET", "/nope", "", nil); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestTrialReplyFlow(t *testing.T) {
	h := newHarness(t)
	token := h.register(deviceID)

	r := h.do("POST", "/v1/reply", token, replyBody)
	if r.status != 200 || r.body["status"] != "ok" || len(r.body["replies"].([]any)) != 3 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if got := num(r.body["credits"].(map[string]any)["remaining"]); got != credits.TrialCredits-1 {
		t.Fatalf("remaining %d", got)
	}

	// 非聊天截图不扣积分
	h.ai.status = ai.StatusNotAConversation
	r = h.do("POST", "/v1/reply", token, replyBody)
	if r.body["status"] != "not_a_conversation" || num(r.body["credits"].(map[string]any)["remaining"]) != credits.TrialCredits-1 {
		t.Fatal(r.raw)
	}

	// 模型出错时退还预扣，返回对应错误码
	h.ai.status, h.ai.err = "", apierr.New(apierr.Timeout, "timed out")
	r = h.do("POST", "/v1/reply", token, replyBody)
	if r.status != 504 || errCode(r) != "timeout" {
		t.Fatal(r.status, r.raw)
	}
	h.ai.err = nil

	me := h.do("GET", "/v1/me", token, nil)
	if me.status != 200 || num(me.body["credits"].(map[string]any)["total"]) != credits.TrialCredits-1 || me.body["user"] != nil {
		t.Fatal(me.raw)
	}
	if me.body["plan"].(map[string]any)["id"] != "trial" {
		t.Fatal(me.raw)
	}
}

func TestReplyValidationAndAuth(t *testing.T) {
	h := newHarness(t)
	if r := h.do("POST", "/v1/reply", "", replyBody); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := h.do("POST", "/v1/reply", "garbage.token", replyBody); r.status != 401 {
		t.Fatal(r.status)
	}
	token := h.register(deviceID)
	if r := h.do("POST", "/v1/reply", token, map[string]any{"image": ""}); errCode(r) != "bad_request" {
		t.Fatal(r.raw)
	}
	if r := h.do("POST", "/v1/reply", token, map[string]any{"image": "aGk=", "media_type": "image/gif"}); errCode(r) != "bad_request" {
		t.Fatal(r.raw)
	}
	big := map[string]any{"image": strings.Repeat("A", maxImageB64Chars+1)}
	if r := h.do("POST", "/v1/reply", token, big); r.status != 400 {
		t.Fatal(r.status, r.raw)
	}
	if h.ai.calls != 0 {
		t.Fatal("model should not be called for invalid requests")
	}
}

func TestIdempotencyKeyChargesOnce(t *testing.T) {
	h := newHarness(t)
	token := h.register(deviceID)
	for i := 0; i < 2; i++ {
		r := h.do("POST", "/v1/reply", token, replyBody, "Idempotency-Key", "retry-key-123")
		if r.status != 200 {
			t.Fatal(r.raw)
		}
	}
	me := h.do("GET", "/v1/me", token, nil)
	if num(me.body["credits"].(map[string]any)["total"]) != credits.TrialCredits-1 {
		t.Fatal("retry charged twice", me.raw)
	}
}

func TestTrialExhaustedRequiresLogin(t *testing.T) {
	h := newHarness(t)
	token := h.register(deviceID)
	// 把体验额度用到只剩 0
	h.db.Exec(`UPDATE credit_grants SET remaining = 0`)
	r := h.do("POST", "/v1/reply", token, replyBody)
	if r.status != 402 || errCode(r) != "insufficient_credits" {
		t.Fatal(r.status, r.raw)
	}
	if d := r.body["error"].(map[string]any)["details"].(map[string]any); d["login_required"] != true {
		t.Fatal(r.raw)
	}
}

func TestTrialAbuseLimits(t *testing.T) {
	h := newHarness(t)
	// 同一 IP 每天只给前 3 台新设备发体验额度
	for i := 0; i < 4; i++ {
		tok := h.register(fmt.Sprintf("device-000000000000%04d", i))
		me := h.do("GET", "/v1/me", tok, nil)
		want := credits.TrialCredits
		if i >= credits.TrialsPerIPPerDay {
			want = 0
		}
		if got := num(me.body["credits"].(map[string]any)["total"]); got != want {
			t.Fatalf("device %d: got %d want %d", i, got, want)
		}
	}
	// 重新注册已登记的设备不重复发放
	tok := h.register("device-0000000000000000")
	if got := num(h.do("GET", "/v1/me", tok, nil).body["credits"].(map[string]any)["total"]); got != credits.TrialCredits {
		t.Fatal(got)
	}
	if r := h.do("POST", "/v1/device/register", "", map[string]any{"device_id": "bad id"}); r.status != 400 {
		t.Fatal(r.status)
	}
}

// login 走完整的浏览器登录流程，返回 token 响应
func (h *harness) login(email, device string) map[string]any {
	h.t.Helper()
	verifier := authn.RandomToken()
	challenge := authn.PKCEChallenge(verifier)
	redirect := "http://127.0.0.1:53123/callback"

	page := h.do("GET", "/auth/login?"+url.Values{"device_id": {device}, "redirect_uri": {redirect}, "state": {"st"}, "code_challenge": {challenge}}.Encode(), "", nil)
	if page.status != 200 || !strings.Contains(page.raw, "登录 NextReply") {
		h.t.Fatal(page.status)
	}

	start := h.do("POST", "/auth/email/start", "", map[string]any{"email": email})
	if start.status != 200 {
		h.t.Fatal(start.raw)
	}
	code := start.body["dev_code"].(string)
	verify := h.do("POST", "/auth/email/verify", "", map[string]any{
		"email": email, "code": code, "device_id": device, "redirect_uri": redirect, "state": "st", "code_challenge": challenge,
	})
	if verify.status != 200 {
		h.t.Fatal(verify.raw)
	}
	u, _ := url.Parse(verify.body["redirect"].(string))
	if u.Host != "127.0.0.1:53123" || u.Query().Get("state") != "st" {
		h.t.Fatal(u)
	}

	tok := h.do("POST", "/v1/auth/token", "", map[string]any{"code": u.Query().Get("code"), "code_verifier": verifier, "device_id": device})
	if tok.status != 200 {
		h.t.Fatal(tok.raw)
	}
	// 授权码只能用一次
	if again := h.do("POST", "/v1/auth/token", "", map[string]any{"code": u.Query().Get("code"), "code_verifier": verifier, "device_id": device}); again.status != 401 {
		h.t.Fatal("auth code reused")
	}
	return tok.body
}

func TestLoginRefreshLogout(t *testing.T) {
	h := newHarness(t)
	h.register(deviceID)
	tok := h.login("Jane.Doe+x@GMail.com", deviceID)
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)

	me := h.do("GET", "/v1/me", access, nil)
	if me.status != 200 || me.body["plan"].(map[string]any)["id"] != "free" {
		t.Fatal(me.raw)
	}
	// 注册赠送 50 + 每日补充 5
	if got := num(me.body["credits"].(map[string]any)["total"]); got != credits.SignupBonus+credits.Plans[credits.PlanFree].DailyRefill {
		t.Fatal(got)
	}
	if r := h.do("POST", "/v1/reply", access, replyBody); r.status != 200 || r.body["credits"].(map[string]any)["plan"] != "free" {
		t.Fatal(r.raw)
	}

	// 同一人用规范化后相同的邮箱再登录：同一账号，不重复送
	tok2 := h.login("janedoe@gmail.com", deviceID)
	if tok2["user"].(map[string]any)["id"] != tok["user"].(map[string]any)["id"] {
		t.Fatal("email normalization failed")
	}

	// 刷新轮换；旧 token 再用一次会作废整条链
	r1 := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh, "device_id": deviceID})
	if r1.status != 200 {
		t.Fatal(r1.raw)
	}
	newRefresh := r1.body["refresh_token"].(string)
	reuse := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh, "device_id": deviceID})
	if reuse.status != 401 || reuse.body["error"].(map[string]any)["details"].(map[string]any)["reason"] != "refresh_reused" {
		t.Fatal(reuse.raw)
	}
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": newRefresh, "device_id": deviceID}); r.status != 401 {
		t.Fatal("family should be revoked after reuse")
	}

	// 登出作废
	tok3 := h.login("janedoe@gmail.com", deviceID)
	h.do("POST", "/v1/auth/logout", "", map[string]any{"refresh_token": tok3["refresh_token"]})
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": tok3["refresh_token"], "device_id": deviceID}); r.status != 401 {
		t.Fatal("logout did not revoke")
	}
}

func TestLoginRejects(t *testing.T) {
	h := newHarness(t)
	if r := h.do("GET", "/auth/login?device_id="+deviceID+"&redirect_uri=https://evil.example/callback&state=s&code_challenge=c", "", nil); r.status != 400 {
		t.Fatal("non-loopback redirect accepted")
	}
	if r := h.do("POST", "/auth/email/start", "", map[string]any{"email": "x@mailinator.com"}); r.status != 400 {
		t.Fatal("disposable email accepted")
	}
	h.do("POST", "/auth/email/start", "", map[string]any{"email": "a@example.com"})
	if r := h.do("POST", "/auth/email/start", "", map[string]any{"email": "a@example.com"}); errCode(r) != "rate_limited" {
		t.Fatal("resend throttle missing", r.raw)
	}
	// 错误验证码超过次数后锁定
	body := map[string]any{"email": "a@example.com", "code": "000000", "device_id": deviceID, "redirect_uri": "http://127.0.0.1:5000/callback", "state": "s", "code_challenge": "c"}
	for i := 0; i < otpMaxAttempts; i++ {
		h.do("POST", "/auth/email/verify", "", body)
	}
	if r := h.do("POST", "/auth/email/verify", "", body); errCode(r) != "rate_limited" {
		t.Fatal(r.raw)
	}
}

func TestDevSubscriptionFlow(t *testing.T) {
	h := newHarness(t)
	h.register(deviceID)
	access := h.login("payer@example.com", deviceID)["access_token"].(string)

	// 匿名设备不能申请结账
	if r := h.do("POST", "/v1/billing/link", h.register("device-anon-000000001"), map[string]any{"plan": "pro"}); r.status != 401 {
		t.Fatal(r.status)
	}
	link := h.do("POST", "/v1/billing/link", access, map[string]any{"plan": "pro"})
	u, _ := url.Parse(link.body["url"].(string))
	checkout := h.do("GET", u.RequestURI(), "", nil)
	if checkout.status != 200 || !strings.Contains(checkout.raw, "模拟支付") {
		t.Fatal(checkout.status, checkout.raw)
	}
	// 票据只能用一次
	if r := h.do("GET", u.RequestURI(), "", nil); r.status != 400 {
		t.Fatal("ticket reused")
	}
	ticket := regexp.MustCompile(`name="ticket" value="([^"]+)"`).FindStringSubmatch(checkout.raw)[1]
	done := h.postForm("/billing/dev/complete", url.Values{"ticket": {ticket}, "plan": {"pro"}})
	if done.status != 303 {
		t.Fatal(done.status, done.raw)
	}

	me := h.do("GET", "/v1/me", access, nil)
	if me.body["plan"].(map[string]any)["id"] != "pro" || me.body["subscription"] == nil {
		t.Fatal(me.raw)
	}
	if got := num(me.body["credits"].(map[string]any)["total"]); got != credits.SignupBonus+1000 {
		t.Fatal(got)
	}

	// 管理订阅 → 立即结束：回到免费，订阅积分收回
	portal := h.do("POST", "/v1/billing/link", access, map[string]any{"purpose": "portal"})
	pu, _ := url.Parse(portal.body["url"].(string))
	pp := h.do("GET", pu.RequestURI(), "", nil)
	expire := regexp.MustCompile(`action="/billing/dev/expire"><input type="hidden" name="ticket" value="([^"]+)"`).FindStringSubmatch(pp.raw)[1]
	if r := h.postForm("/billing/dev/expire", url.Values{"ticket": {expire}}); r.status != 200 {
		t.Fatal(r.raw)
	}
	me = h.do("GET", "/v1/me", access, nil)
	if me.body["plan"].(map[string]any)["id"] != "free" || num(me.body["credits"].(map[string]any)["total"]) != credits.SignupBonus+5 {
		t.Fatal(me.raw)
	}
}

func signStripe(payload []byte, secret string, ts time.Time) string {
	t := fmt.Sprint(ts.Unix())
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(t + "." + string(payload)))
	return "t=" + t + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

func TestStripeSignature(t *testing.T) {
	now := time.Now()
	payload := []byte(`{"id":"evt_1"}`)
	if err := verifyStripeSignature(payload, signStripe(payload, "whsec_x", now), "whsec_x", now); err != nil {
		t.Fatal(err)
	}
	if verifyStripeSignature(payload, signStripe(payload, "whsec_y", now), "whsec_x", now) == nil {
		t.Fatal("wrong secret accepted")
	}
	if verifyStripeSignature(payload, signStripe(payload, "whsec_x", now.Add(-10*time.Minute)), "whsec_x", now) == nil {
		t.Fatal("stale timestamp accepted")
	}
	if verifyStripeSignature([]byte(`{"id":"evt_2"}`), signStripe(payload, "whsec_x", now), "whsec_x", now) == nil {
		t.Fatal("tampered payload accepted")
	}
}

// 用一个假的 Stripe API 跑通 invoice.paid → 发放积分 → 退款收回
func TestStripeWebhookFlow(t *testing.T) {
	periodEnd := time.Now().Add(30 * 24 * time.Hour).Unix()
	var userID string
	fakeStripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Stripe-Version") == "" {
			t.Error("missing Stripe-Version")
		}
		fmt.Fprintf(w, `{"id":"sub_1","status":"active","customer":"cus_1","cancel_at_period_end":false,
			"metadata":{"user_id":%q,"plan":"pro_plus"},
			"items":{"data":[{"current_period_start":%d,"current_period_end":%d,"price":{"id":"price_plus"}}]}}`,
			userID, time.Now().Unix(), periodEnd)
	}))
	defer fakeStripe.Close()

	h := newHarness(t, func(c *config.Config) {
		c.StripeSecretKey, c.StripeWebhookSecret = "sk_test_x", "whsec_test"
		c.StripePricePro, c.StripePriceProPlus = "price_pro", "price_plus"
	})
	h.register(deviceID)
	tok := h.login("sub@example.com", deviceID)
	userID = tok["user"].(map[string]any)["id"].(string)
	access := tok["access_token"].(string)

	// 把 Stripe 客户端指向假服务
	origBase := stripeBaseForTest
	stripeBaseForTest = fakeStripe.URL
	defer func() { stripeBaseForTest = origBase }()

	send := func(event string) resp {
		payload := []byte(event)
		return h.do("POST", "/billing/webhook", "", json.RawMessage(payload), "Stripe-Signature", signStripe(payload, "whsec_test", time.Now()))
	}
	invoice := `{"id":"evt_1","type":"invoice.paid","data":{"object":{"id":"in_1","parent":{"subscription_details":{"subscription":"sub_1"}},"lines":{"data":[{"period":{"end":` + fmt.Sprint(periodEnd) + `}}]}}}}`
	if r := send(invoice); r.status != 200 {
		t.Fatal(r.raw)
	}
	send(invoice) // 重复投递不重复发放

	me := h.do("GET", "/v1/me", access, nil)
	if me.body["plan"].(map[string]any)["id"] != "pro_plus" {
		t.Fatal(me.raw)
	}
	if got := num(me.body["credits"].(map[string]any)["total"]); got != credits.SignupBonus+3000 {
		t.Fatal(got)
	}

	if r := send(`{"id":"evt_2","type":"charge.refunded","data":{"object":{"customer":"cus_1"}}}`); r.status != 200 {
		t.Fatal(r.raw)
	}
	me = h.do("GET", "/v1/me", access, nil)
	if got := num(me.body["credits"].(map[string]any)["total"]); got != credits.SignupBonus {
		t.Fatal("refund did not revoke", got)
	}

	if r := h.do("POST", "/billing/webhook", "", json.RawMessage(`{}`), "Stripe-Signature", "t=1,v1=00"); r.status != 400 {
		t.Fatal("bad signature accepted")
	}
}

func TestClientIPHeader(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.ClientIPHeader = "X-Real-IP" })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Real-IP", "203.0.113.9")
	if ip := h.srv.clientIP(req); ip != "203.0.113.9" {
		t.Fatal(ip)
	}
	h.srv.cfg.ClientIPHeader = ""
	if ip := h.srv.clientIP(req); ip != "192.0.2.1" {
		t.Fatal("header must be ignored when not configured", ip)
	}
}

func TestNormalizeEmailAndPlan(t *testing.T) {
	cases := map[string]string{
		"Jane.Doe+promo@GMail.com":  "janedoe@gmail.com",
		"jane.doe+x@googlemail.com": "janedoe@gmail.com",
		"jane.doe+x@outlook.com":    "jane.doe@outlook.com",
	}
	for in, want := range cases {
		if got := normalizeEmail(in); got != want {
			t.Fatalf("%s → %s", in, got)
		}
	}
	if !isValidEmail("a@b.co") || isValidEmail("not-an-email") {
		t.Fatal("email validation")
	}

	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC).UnixMilli()
	day := int64(864e5)
	sub := func(status string, end int64) *Subscription {
		return &Subscription{Plan: "pro", Status: status, CurrentPeriodEnd: sql.NullInt64{Int64: end, Valid: true}}
	}
	if planFromSubscription(sub("active", now+day), now) != credits.PlanPro ||
		planFromSubscription(sub("past_due", now-day), now) != credits.PlanPro {
		t.Fatal("paid plan expected")
	}
	if planFromSubscription(nil, now) != credits.PlanFree ||
		planFromSubscription(sub("canceled", now+day), now) != credits.PlanFree ||
		planFromSubscription(sub("active", now-10*day), now) != credits.PlanFree {
		t.Fatal("free plan expected")
	}
}

func TestModelPerPlan(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.PlanModels = map[string]config.ModelChoice{
			"trial": {Model: "cheap-trial"}, "free": {Model: "cheap-free"}, "pro": {Model: "premium"},
		}
	})
	token := h.register(deviceID)
	h.do("POST", "/v1/reply", token, replyBody)
	if h.ai.lastModel != "cheap-trial" {
		t.Fatal("trial used", h.ai.lastModel)
	}
	access := h.login("plans@example.com", deviceID)["access_token"].(string)
	h.do("POST", "/v1/reply", access, replyBody)
	if h.ai.lastModel != "cheap-free" {
		t.Fatal("free used", h.ai.lastModel)
	}
}

func TestUsageEventsRecorded(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.IPDailyQuota = 2 })
	token := h.register(deviceID)
	h.do("POST", "/v1/reply", token, replyBody)        // ok
	h.ai.err = apierr.New(apierr.Timeout, "timed out") //
	h.do("POST", "/v1/reply", token, replyBody)        // error:timeout
	h.ai.err = nil                                     //
	h.do("POST", "/v1/reply", token, replyBody)        // ok（第 2 次成功）
	h.do("POST", "/v1/reply", token, replyBody)        // 达到 IP 上限 → rejected:ip_quota

	rows, err := h.db.Query(`SELECT status, credits, COALESCE(error_code, ''), device_id, ip FROM usage_events ORDER BY created_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var status, code, dev, ip string
		var cr int
		rows.Scan(&status, &cr, &code, &dev, &ip)
		if dev != deviceID || ip != "127.0.0.1" {
			t.Fatal("device/ip not recorded", dev, ip)
		}
		got = append(got, fmt.Sprintf("%s/%d/%s", status, cr, code))
	}
	want := []string{"ok/1/", "error:timeout/0/timeout", "ok/1/", "rejected:ip_quota/0/ip_quota"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatal(got)
	}
}

func TestRuntimeSettings(t *testing.T) {
	h := newHarness(t)
	st := h.srv.Settings()
	ctx := context.Background()

	// 后台改模型，下一次请求立即生效
	if _, err := st.Apply(ctx, []settings.Change{{Key: "model.trial", Value: "claude-haiku-4-5"}}, "test"); err != nil {
		t.Fatal(err)
	}
	token := h.register(deviceID)
	h.do("POST", "/v1/reply", token, replyBody)
	if h.ai.lastModel != "claude-haiku-4-5" {
		t.Fatal(h.ai.lastModel)
	}

	// 暂停发放体验额度：新设备没有积分，需要登录
	st.Apply(ctx, []settings.Change{{Key: "trial_enabled", Value: "false"}}, "test")
	tok2 := h.register("device-no-trial-000000001")
	if r := h.do("POST", "/v1/reply", tok2, replyBody); errCode(r) != "insufficient_credits" {
		t.Fatal(r.raw)
	}

	// 每日上限下调
	st.Apply(ctx, []settings.Change{{Key: "daily_cap.trial", Value: "1"}}, "test")
	if r := h.do("POST", "/v1/reply", token, replyBody); errCode(r) != "daily_cap" {
		t.Fatal(r.raw)
	}
	me := h.do("GET", "/v1/me", token, nil)
	if num(me.body["plan"].(map[string]any)["daily_cap"]) != 1 {
		t.Fatal(me.raw)
	}
}

func TestCostCapPausesAndDowngradesFreeUsers(t *testing.T) {
	h := newHarness(t)
	st := h.srv.Settings()
	ctx := context.Background()
	token := h.register(deviceID)
	// 今天已经花了 2 美元
	h.db.Exec(`INSERT INTO usage_events (request_id, owner, plan, status, credits, created_at, cost_micros) VALUES ('x', 'd:other', 'pro', 'ok', 1, ?, 2000000)`, time.Now().UnixMilli())

	st.Apply(ctx, []settings.Change{{Key: "cost_cap_usd", Value: "1"}, {Key: "cost_cap_action", Value: "pause_free"}}, "test")
	r := h.do("POST", "/v1/reply", token, replyBody)
	if r.status != 503 || errCode(r) != "service_busy" || h.ai.calls != 0 {
		t.Fatal(r.status, r.raw)
	}
	var status string
	h.db.QueryRow(`SELECT status FROM usage_events WHERE owner = ? ORDER BY created_at DESC LIMIT 1`, "d:"+deviceID).Scan(&status)
	if status != "rejected:cost_cap" {
		t.Fatal(status)
	}

	st.Apply(ctx, []settings.Change{{Key: "cost_cap_action", Value: "downgrade"}}, "test")
	if r := h.do("POST", "/v1/reply", token, replyBody); r.status != 200 || h.ai.lastModel != "claude-haiku-4-5" {
		t.Fatal(r.status, h.ai.lastModel)
	}
}
