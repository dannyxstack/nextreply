package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
	"github.com/nextreply/server/internal/store"
)

var ctx = context.Background()

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "claude-opus-5-5", Effort: "low", IPDailyQuota: 200, RegisterPerIP: 20,
		PlanModels: map[string]config.ModelChoice{"trial": {Model: "claude-sonnet-5", Effort: "low"}, "free": {Model: "claude-sonnet-5", Effort: "low"}}}
	s := New(db, cfg)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaults(t *testing.T) {
	s := newStore(t)
	if s.Get("model.trial") != "claude-sonnet-5" || s.Get("model.pro") != "claude-opus-5-5" {
		t.Fatal("models from config")
	}
	if s.Plan(credits.PlanFree).DailyCap != 50 || s.Plan(credits.PlanFree).DailyRefill != 5 || s.IPDailyQuota() != 200 || !s.TrialEnabled() {
		t.Fatal("defaults")
	}
	for _, f := range Fields {
		if _, err := f.Validate(s.Default(f.Key)); err != nil {
			t.Fatalf("default of %s is invalid: %v", f.Key, err)
		}
	}
}

func TestApply(t *testing.T) {
	s := newStore(t)
	diff, err := s.Apply(ctx, []Change{{"model.trial", "claude-haiku-4-5"}, {"daily_cap.free", " 80 "}, {"ip_daily_quota", "200"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// ip_daily_quota 填的是默认值，不产生覆盖
	if len(diff) != 2 || diff["daily_cap.free"] != [2]string{"50", "80"} || s.Overridden("ip_daily_quota") {
		t.Fatal(diff)
	}
	if s.Get("model.trial") != "claude-haiku-4-5" || s.Plan(credits.PlanFree).DailyCap != 80 {
		t.Fatal("not applied")
	}

	// 有一项不合法时整批都不写入
	for _, bad := range [][]Change{
		{{"model.pro", "gpt-5"}},
		{{"daily_cap.pro", "0"}},
		{{"effort.pro", "huge"}},
		{{"trial_enabled", "yes"}},
		{{"cost_cap_action", "panic"}},
		{{"no_such_key", "1"}},
		{{"daily_cap.pro", "120"}, {"model.pro", "nope"}},
	} {
		_, err := s.Apply(ctx, bad, "admin")
		if _, ok := err.(*ValidationError); !ok {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
	if s.Overridden("daily_cap.pro") {
		t.Fatal("partial write")
	}

	// 留空恢复默认；重新加载后保持
	if _, err := s.Apply(ctx, []Change{{"model.trial", ""}}, "admin"); err != nil {
		t.Fatal(err)
	}
	s2 := New(s.db, s.cfg)
	s2.Load(ctx)
	if s2.Get("model.trial") != "claude-sonnet-5" || s2.Plan(credits.PlanFree).DailyCap != 80 {
		t.Fatal("persisted state wrong")
	}
}

func TestLoadIgnoresInvalidStoredValues(t *testing.T) {
	s := newStore(t)
	s.db.Exec(`INSERT INTO settings (key, value, updated_at, updated_by) VALUES ('model.pro', 'retired-model', 0, 'x'), ('unknown', '1', 0, 'x')`)
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Get("model.pro") != "claude-opus-5-5" {
		t.Fatal("invalid stored model should fall back to default")
	}
}

func TestCostCap(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.db.Exec(`INSERT INTO usage_events (request_id, owner, plan, status, credits, created_at, cost_micros) VALUES ('a', 'd:x', 'trial', 'ok', 1, ?, 600000), ('b', 'd:x', 'trial', 'ok', 1, ?, 900000)`,
		now.Add(-time.Hour).UnixMilli(), now.Add(-24*time.Hour).UnixMilli())

	c, _ := s.CostCap(ctx)
	if c.Hit || c.TodayMicros != 600000 || c.CapMicros != 0 {
		t.Fatal("no cap by default", c)
	}
	s.Apply(ctx, []Change{{"cost_cap_usd", "1"}}, "admin")
	if c, _ = s.CostCap(ctx); c.Hit {
		t.Fatal("0.6 < 1")
	}
	s.AddCost(500000) // 缓存期内的新调用也计入
	if c, _ = s.CostCap(ctx); !c.Hit || c.TodayMicros != 1100000 {
		t.Fatal("cap should hit", c)
	}
	if m := s.ModelFor(credits.PlanTrial, c); m.Model != "claude-haiku-4-5" {
		t.Fatal("free tier should downgrade", m)
	}
	if m := s.ModelFor(credits.PlanPro, c); m.Model != "claude-opus-5-5" {
		t.Fatal("paid tier must not be affected", m)
	}
	// 第二天重新计算
	now = now.Add(24 * time.Hour)
	if c, _ = s.CostCap(ctx); c.Hit || c.TodayMicros != 0 {
		t.Fatal("new day", c)
	}
}

func TestCostCapRounding(t *testing.T) {
	s := newStore(t)
	for _, v := range []string{"0.000001", "0.29", "1.15"} {
		s.Apply(ctx, []Change{{"cost_cap_usd", v}}, "admin")
		c, _ := s.CostCap(ctx)
		want := map[string]int64{"0.000001": 1, "0.29": 290000, "1.15": 1150000}[v]
		if c.CapMicros != want {
			t.Fatal(v, c.CapMicros)
		}
	}
}

func TestFieldsHaveUniqueKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Fields {
		if seen[f.Key] || strings.TrimSpace(f.Label) == "" {
			t.Fatal("bad field", f.Key)
		}
		seen[f.Key] = true
	}
}
