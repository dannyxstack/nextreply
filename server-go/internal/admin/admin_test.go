package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/app"
	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/store"
)

const token = "admin-token-0123456789-abcdef"

// setup 通过真实接口制造数据：匿名设备调用、积分用完被拒绝、IP 达到上限，再返回后台的 handler。
func setup(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		TokenSecret: "test-secret-0123456789-abcdefghijklmn", Model: "claude-opus-5-5", Effort: "low",
		IPDailyQuota: 3, RegisterPerIP: 20, DevMode: true, PublicURL: "http://t",
		AdminToken: token, AdminTZ: "Asia/Shanghai",
		PlanModels: map[string]config.ModelChoice{"trial": {Model: "claude-sonnet-5", Effort: "low"}},
	}
	srv := app.New(cfg, db, ai.Mock{})
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)

	post := func(path, bearer string, body any) map[string]any {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", api.URL+path, bytes.NewReader(b))
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
		return out
	}
	dev := post("/v1/device/register", "", map[string]any{"device_id": "device-aaaaaaaaaaaa-0001", "hw_hash": strings.Repeat("ab", 16)})["token"].(string)
	post("/v1/device/register", "", map[string]any{"device_id": "device-aaaaaaaaaaaa-0002", "hw_hash": strings.Repeat("ab", 16)})
	post("/v1/device/register", "", map[string]any{"device_id": "device-aaaaaaaaaaaa-0003"})
	img := map[string]any{"image": "aGVsbG8=", "media_type": "image/jpeg"}
	for i := 0; i < 4; i++ { // 第 4 次触发 IP 每日上限（IPDailyQuota = 3）
		post("/v1/reply", dev, img)
	}

	adm, err := New(cfg, db, srv.IPLimits())
	if err != nil {
		t.Fatal(err)
	}
	return adm.Handler(), db
}

func get(t *testing.T, h http.Handler, path string, auth bool) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if auth {
		req.SetBasicAuth("admin", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestAuth(t *testing.T) {
	h, _ := setup(t)
	if code, _ := get(t, h, "/", false); code != 401 {
		t.Fatal("no auth", code)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong-token-xxxxxxxxxxxxxxxx")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("wrong token", rec.Code)
	}
	if code, _ := get(t, h, "/", true); code != 200 {
		t.Fatal("valid token", code)
	}
}

func TestPages(t *testing.T) {
	h, _ := setup(t)

	code, body := get(t, h, "/", true)
	for _, want := range []string{"概览", "claude-sonnet-5", "rejected:ip_quota", "体验"} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("overview missing %q (code %d)", want, code)
		}
	}

	code, body = get(t, h, "/accounts", true)
	if code != 200 || !strings.Contains(body, "/accounts/d/device-aaaaaaaaaaaa-0001") || !strings.Contains(body, "/accounts/d/device-aaaaaaaaaaaa-0003") {
		t.Fatal("accounts", code)
	}
	// 搜索、筛选、排序
	if _, body = get(t, h, "/accounts?q=0003", true); strings.Contains(body, "0001\"") || !strings.Contains(body, "device-aaaaaaaaaaaa-0003") {
		t.Fatal("search")
	}
	if _, body = get(t, h, "/accounts?kind=user", true); !strings.Contains(body, "没有匹配的账户") {
		t.Fatal("user filter should be empty")
	}
	for _, s := range []string{"usage", "cost", "created"} {
		if code, _ := get(t, h, "/accounts?sort="+s, true); code != 200 {
			t.Fatal("sort", s)
		}
	}

	code, body = get(t, h, "/accounts/d/device-aaaaaaaaaaaa-0001", true)
	if code != 200 || !strings.Contains(body, "mock") || !strings.Contains(body, "rejected:ip_quota") {
		t.Fatal("device detail", code)
	}
	if code, _ := get(t, h, "/accounts/d/nope-nope-nope-nope", true); code != 404 {
		t.Fatal("missing account", code)
	}
	if code, _ := get(t, h, "/accounts/x/whatever", true); code != 404 {
		t.Fatal("bad kind", code)
	}

	code, body = get(t, h, "/limits", true)
	if code != 200 || !strings.Contains(body, "已达上限") || !strings.Contains(body, "IP 达到每日上限") {
		t.Fatal("limits", code)
	}
	if !strings.Contains(body, "同一硬件") || !strings.Contains(body, ">2<") {
		t.Fatal("hw hash group missing")
	}
}

func TestRejectionsRecorded(t *testing.T) {
	// setup 里 3 次成功、第 4 次被 IP 上限拒绝；被拒绝的请求也要记录
	_, db := setup(t)
	var ok, rejected int
	db.QueryRow(`SELECT SUM(status = 'ok'), SUM(status LIKE 'rejected:%') FROM usage_events`).Scan(&ok, &rejected)
	if ok != 3 || rejected != 1 {
		t.Fatal(ok, rejected)
	}
}

func TestHelpers(t *testing.T) {
	if fmtUSD(13734) != "$0.01" || fmtUSD(357) != "$0.0004" || fmtUSD(0) != "$0" {
		t.Fatal(fmtUSD(13734), fmtUSD(357))
	}
	if fmtNum(1234567) != "1,234,567" || fmtNum(999) != "999" {
		t.Fatal(fmtNum(1234567))
	}
	if percentile([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.5) != 5 || percentile([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.95) != 10 || percentile(nil, 0.5) != 0 {
		t.Fatal("percentile")
	}
}
