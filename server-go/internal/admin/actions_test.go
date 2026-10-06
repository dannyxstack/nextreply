package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// apiCall 调用真实的 /v1 接口
func apiCall(t *testing.T, e env, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.api+path, rd)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out map[string]any
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

// login 走一遍开发模式的邮箱登录，返回 user_id、access token、refresh token
func login(t *testing.T, e env, deviceID, email string) (string, string, string) {
	t.Helper()
	buf := make([]byte, 32)
	rand.Read(buf)
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:50000/callback"
	apiCall(t, e, "POST", "/v1/device/register", "", map[string]any{"device_id": deviceID})
	_, start := apiCall(t, e, "POST", "/auth/email/start", "", map[string]any{"email": email})
	_, ver := apiCall(t, e, "POST", "/auth/email/verify", "", map[string]any{
		"email": email, "code": start["dev_code"], "device_id": deviceID, "redirect_uri": redirect, "state": "s", "code_challenge": challenge,
	})
	u, _ := url.Parse(ver["redirect"].(string))
	_, tok := apiCall(t, e, "POST", "/v1/auth/token", "", map[string]any{"code": u.Query().Get("code"), "code_verifier": verifier, "device_id": deviceID})
	return tok["user"].(map[string]any)["id"].(string), tok["access_token"].(string), tok["refresh_token"].(string)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func csrfToken(t *testing.T, h http.Handler, page string) string {
	t.Helper()
	_, body := get(t, h, page, true)
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no csrf token on", page)
	}
	return m[1]
}

// post 提交后台表单，返回状态码和重定向地址
func post(t *testing.T, h http.Handler, path string, form url.Values, headers ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", token)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

func flash(loc string) string {
	u, _ := url.Parse(loc)
	return u.Query().Get("msg")
}

func auditCount(t *testing.T, e env, action string) int {
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM admin_audit WHERE action = ?`, action).Scan(&n)
	return n
}

func TestCSRF(t *testing.T) {
	e := setupEnv(t)
	page := "/accounts/d/device-aaaaaaaaaaaa-0003"
	csrf := csrfToken(t, e.h, page)
	form := url.Values{"amount": {"5"}, "days": {"0"}, "reason": {"test"}, "back": {page}}

	if code, _ := post(t, e.h, page+"/grant", form); code != 403 {
		t.Fatal("missing token accepted", code)
	}
	form.Set("csrf", csrf)
	if code, _ := post(t, e.h, page+"/grant", form, "Origin", "https://evil.example"); code != 403 {
		t.Fatal("cross origin accepted", code)
	}
	// 沙箱 iframe 等场景下浏览器发送 Origin: null，不能放行
	if code, _ := post(t, e.h, page+"/grant", form, "Origin", "null"); code != 403 {
		t.Fatal("null origin accepted", code)
	}
	if code, _ := post(t, e.h, page+"/grant", form, "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Fatal("cross-site fetch accepted", code)
	}
	// 没有 Basic 认证
	req := httptest.NewRequest("POST", page+"/grant", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("unauthenticated post", rec.Code)
	}
	if auditCount(t, e, "grant_credits") != 0 {
		t.Fatal("rejected requests must not change anything")
	}
	if code, loc := post(t, e.h, page+"/grant", form, "Origin", "http://example.com", "Sec-Fetch-Site", "same-origin"); code != 303 || flash(loc) != "已补发 5 积分" {
		t.Fatal("same-origin post", code, loc)
	}
}

func TestReferrerPolicyKeepsOrigin(t *testing.T) {
	// Referrer-Policy: no-referrer 会让浏览器在表单 POST 时发送 Origin: null，导致正常操作被拒绝
	h, _ := setup(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if p := rec.Header().Get("Referrer-Policy"); p == "no-referrer" || p == "" {
		t.Fatal(p)
	}
}

func TestGrantCredits(t *testing.T) {
	e := setupEnv(t)
	page := "/accounts/d/device-aaaaaaaaaaaa-0003"
	csrf := csrfToken(t, e.h, page)
	form := url.Values{"csrf": {csrf}, "amount": {"25"}, "days": {"30"}, "reason": {"客服补偿"}, "back": {page}}

	code, loc := post(t, e.h, page+"/grant", form)
	if code != 303 || !strings.HasPrefix(loc, page+"?") || flash(loc) != "已补发 25 积分" {
		t.Fatal(code, loc)
	}
	// 设备原本有 10 次体验额度
	_, body := get(t, e.h, page, true)
	if !strings.Contains(body, `<div class="v">35</div>`) || !strings.Contains(body, "客服补偿") {
		t.Fatal("balance or audit not shown")
	}
	// 通过真实接口确认余额
	var total int
	e.db.QueryRow(`SELECT SUM(remaining) FROM credit_grants WHERE owner = 'd:device-aaaaaaaaaaaa-0003'`).Scan(&total)
	if total != 35 {
		t.Fatal(total)
	}

	for _, bad := range []url.Values{
		{"amount": {"0"}, "days": {"0"}, "reason": {"x"}},
		{"amount": {"5"}, "days": {"-1"}, "reason": {"x"}},
		{"amount": {"5"}, "days": {"0"}, "reason": {""}},
	} {
		bad.Set("csrf", csrf)
		bad.Set("back", page)
		if _, loc := post(t, e.h, page+"/grant", bad); !strings.HasPrefix(flash(loc), "操作失败") {
			t.Fatal("bad input accepted", bad, flash(loc))
		}
	}
	if _, loc := post(t, e.h, "/accounts/d/no-such-device-000000/grant", url.Values{"csrf": {csrf}, "amount": {"5"}, "days": {"0"}, "reason": {"x"}}); !strings.Contains(flash(loc), "不存在") {
		t.Fatal(flash(loc))
	}
	if auditCount(t, e, "grant_credits") != 1 {
		t.Fatal("only the successful grant should be audited")
	}
	// back 只能是站内路径
	if _, loc := post(t, e.h, page+"/grant", url.Values{"csrf": {csrf}, "amount": {"1"}, "days": {"0"}, "reason": {"x"}, "back": {"https://evil.example/"}}); !strings.HasPrefix(loc, "/?") {
		t.Fatal("open redirect", loc)
	}
}

func TestSuspendRestoreAndSessions(t *testing.T) {
	e := setupEnv(t)
	dev := "device-bbbbbbbbbbbb-0001"
	userID, access, refresh := login(t, e, dev, "user@example.com")
	page := "/accounts/u/" + userID
	csrf := csrfToken(t, e.h, page)

	if code, _ := apiCall(t, e, "GET", "/v1/me", access, nil); code != 200 {
		t.Fatal("precondition", code)
	}
	if _, loc := post(t, e.h, page+"/suspend", url.Values{"csrf": {csrf}, "reason": {""}, "back": {page}}); !strings.HasPrefix(flash(loc), "操作失败") {
		t.Fatal("suspend without reason accepted")
	}
	_, loc := post(t, e.h, page+"/suspend", url.Values{"csrf": {csrf}, "reason": {"刷量"}, "back": {page}})
	if flash(loc) != "账号已停用，作废了 1 个登录会话" {
		t.Fatal(flash(loc))
	}
	// 停用立即生效：access token 被拒绝，refresh token 已作废
	if code, body := apiCall(t, e, "GET", "/v1/me", access, nil); code != 401 || body["error"].(map[string]any)["details"].(map[string]any)["reason"] != "account_unavailable" {
		t.Fatal("suspended user still served", code)
	}
	if code, _ := apiCall(t, e, "POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh, "device_id": dev}); code != 401 {
		t.Fatal("refresh after suspend", code)
	}
	_, body := get(t, e.h, page, true)
	if !strings.Contains(body, "/restore") || !strings.Contains(body, "刷量") {
		t.Fatal("restore form or audit missing")
	}

	if _, loc = post(t, e.h, page+"/restore", url.Values{"csrf": {csrf}, "back": {page}}); flash(loc) != "账号已恢复" {
		t.Fatal(flash(loc))
	}
	_, access2, refresh2 := login(t, e, dev, "user@example.com")
	if code, _ := apiCall(t, e, "GET", "/v1/me", access2, nil); code != 200 {
		t.Fatal("restored user cannot use the service", code)
	}

	// 强制下线：refresh token 作废
	if _, loc = post(t, e.h, page+"/revoke-sessions", url.Values{"csrf": {csrf}, "back": {page}}); !strings.HasPrefix(flash(loc), "已作废 1 个登录会话") {
		t.Fatal(flash(loc))
	}
	if code, _ := apiCall(t, e, "POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh2, "device_id": dev}); code != 401 {
		t.Fatal("refresh after revoke", code)
	}
	for _, a := range []string{"suspend_user", "restore_user", "revoke_sessions"} {
		if auditCount(t, e, a) != 1 {
			t.Fatal("audit missing", a)
		}
	}
}

func TestUnbindAndDeviceSessions(t *testing.T) {
	e := setupEnv(t)
	dev := "device-cccccccccccc-0001"
	userID, _, refresh := login(t, e, dev, "unbind@example.com")
	page := "/accounts/u/" + userID
	csrf := csrfToken(t, e.h, page)

	if _, loc := post(t, e.h, "/devices/"+dev+"/revoke-sessions", url.Values{"csrf": {csrf}, "back": {page}}); flash(loc) != "已作废这台设备的 1 个登录会话" {
		t.Fatal(flash(loc))
	}
	if code, _ := apiCall(t, e, "POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh, "device_id": dev}); code != 401 {
		t.Fatal("device session still valid")
	}

	if _, loc := post(t, e.h, "/devices/"+dev+"/unbind", url.Values{"csrf": {csrf}, "back": {page}}); flash(loc) != "设备已解绑并下线" {
		t.Fatal(flash(loc))
	}
	var owner *string
	e.db.QueryRow(`SELECT user_id FROM devices WHERE id = ?`, dev).Scan(&owner)
	if owner != nil {
		t.Fatal("device still bound")
	}
	if _, loc := post(t, e.h, "/devices/"+dev+"/unbind", url.Values{"csrf": {csrf}, "back": {page}}); !strings.Contains(flash(loc), "没有绑定账号") {
		t.Fatal("double unbind", flash(loc))
	}

	_, body := get(t, e.h, "/audit", true)
	if !strings.Contains(body, "解绑设备") || !strings.Contains(body, "强制下线") {
		t.Fatal("audit page")
	}
	_, body = get(t, e.h, "/audit?target=d:"+dev, true)
	if strings.Count(body, "<tr>") != 3 { // 表头 + 2 条
		t.Fatal("audit filter", strings.Count(body, "<tr>"))
	}
}

func TestActorFromCloudflareAccess(t *testing.T) {
	e := setupEnv(t)
	page := "/accounts/d/device-aaaaaaaaaaaa-0003"
	csrf := csrfToken(t, e.h, page)
	post(t, e.h, page+"/grant", url.Values{"csrf": {csrf}, "amount": {"1"}, "days": {"0"}, "reason": {"x"}}, "Cf-Access-Authenticated-User-Email", "ops@example.com")
	var who string
	e.db.QueryRow(`SELECT actor FROM admin_audit`).Scan(&who)
	if who != "ops@example.com" {
		t.Fatal(who)
	}
}
