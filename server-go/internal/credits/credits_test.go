package credits

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nextreply/server/internal/store"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).UnixMilli()

func ptr(v int64) *int64 { return &v }

func g(id string, b Bucket, remaining int, exp *int64, created int64) Grant {
	return Grant{ID: id, Bucket: b, Remaining: remaining, ExpiresAt: exp, CreatedAt: created}
}

func ids(gs []Grant) []string {
	out := []string{}
	for _, x := range gs {
		out = append(out, x.ID)
	}
	return out
}

func TestSpendOrder(t *testing.T) {
	grants := []Grant{
		g("sub", BucketSubscription, 100, ptr(now+20*864e5), 0),
		g("trial", BucketTrial, 5, nil, 0),
		g("daily", BucketDaily, 5, ptr(EndOfUTCDay(now)), 0),
	}
	if got := ids(SpendOrder(grants, now)); !reflect.DeepEqual(got, []string{"daily", "sub", "trial"}) {
		t.Fatal(got)
	}
}

func TestAllocate(t *testing.T) {
	grants := []Grant{g("old", BucketBonus, 10, ptr(now-1), 0), g("empty", BucketDaily, 0, ptr(now+1000), 0), g("ok", BucketTrial, 1, nil, 0)}
	if got := Allocate(grants, 1, now); !reflect.DeepEqual(got, []Allocation{{"ok", 1}}) {
		t.Fatal(got)
	}
	split := []Grant{g("a", BucketDaily, 1, ptr(now+1000), 0), g("b", BucketBonus, 2, ptr(now+5000), 0)}
	if got := Allocate(split, 2, now); !reflect.DeepEqual(got, []Allocation{{"a", 1}, {"b", 1}}) {
		t.Fatal(got)
	}
	if Allocate(split, 4, now) != nil {
		t.Fatal("partial allocation should be nil")
	}
}

func TestSummarize(t *testing.T) {
	grants := []Grant{
		g("a", BucketBonus, 3, ptr(now+1000), 0), g("b", BucketBonus, 2, ptr(now+9000), 0),
		g("c", BucketTrial, 4, nil, 0), g("x", BucketDaily, 9, ptr(now-1), 0),
	}
	total, buckets := Summarize(grants, now)
	want := []BucketSummary{{BucketBonus, 5, ptr(now + 1000)}, {BucketTrial, 4, nil}}
	if total != 9 || !reflect.DeepEqual(buckets, want) {
		t.Fatal(total, buckets)
	}
}

func TestEndOfUTCDay(t *testing.T) {
	if EndOfUTCDay(now) != time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatal(EndOfUTCDay(now))
	}
}

// ---------- 账户服务（真实 SQLite） ----------

func newService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(now)
	s := NewService(db)
	s.now = func() time.Time { return clock }
	return s, &clock
}

var ctx = context.Background()

func reserve(t *testing.T, s *Service, owner, key string, refill int) ReserveResult {
	t.Helper()
	r, err := s.Reserve(ctx, owner, ReserveInput{Key: key, Cost: 1, DailyCap: 50, PerMinute: 6, MaxConcurrent: 2, DailyRefill: refill})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func balance(t *testing.T, s *Service, owner string) Balance {
	t.Helper()
	b, err := s.Balance(ctx, owner, 0)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGrantIsIdempotent(t *testing.T) {
	s, _ := newService(t)
	in := GrantInput{Bucket: BucketTrial, Amount: 10, SourceRef: "trial", Reason: "trial"}
	if ok, err := s.Grant(ctx, "d:x", in); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := s.Grant(ctx, "d:x", in); ok {
		t.Fatal("granted twice")
	}
	// 不同账户相互独立
	if ok, _ := s.Grant(ctx, "d:y", in); !ok {
		t.Fatal("other owner blocked")
	}
	if b := balance(t, s, "d:x"); b.Total != 10 {
		t.Fatal(b)
	}
}

func TestReserveCommitRelease(t *testing.T) {
	s, _ := newService(t)
	s.Grant(ctx, "d:x", GrantInput{Bucket: BucketTrial, Amount: 2, SourceRef: "trial", Reason: "trial"})

	if r := reserve(t, s, "d:x", "k1", 0); !r.OK() || r.Remaining != 1 {
		t.Fatal(r)
	}
	if err := s.Commit(ctx, "d:x", "k1"); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s, "d:x"); b.Total != 1 || b.UsedToday != 1 {
		t.Fatal(b)
	}

	reserve(t, s, "d:x", "k2", 0)
	if err := s.Release(ctx, "d:x", "k2"); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s, "d:x"); b.Total != 1 || b.UsedToday != 1 {
		t.Fatal("release did not refund", b)
	}

	reserve(t, s, "d:x", "k3", 0)
	s.Commit(ctx, "d:x", "k3")
	if r := reserve(t, s, "d:x", "k4", 0); r.Code != "insufficient_credits" {
		t.Fatal(r)
	}
}

func TestIdempotentRetryChargesOnce(t *testing.T) {
	s, _ := newService(t)
	s.Grant(ctx, "d:x", GrantInput{Bucket: BucketTrial, Amount: 5, SourceRef: "trial", Reason: "trial"})

	reserve(t, s, "d:x", "same", 0)
	// 进行中重试：不再预扣
	if r := reserve(t, s, "d:x", "same", 0); !r.OK() || r.Remaining != 4 {
		t.Fatal(r)
	}
	s.Commit(ctx, "d:x", "same")
	// 完成后重试：放行但不扣费
	if r := reserve(t, s, "d:x", "same", 0); !r.OK() {
		t.Fatal(r)
	}
	s.Commit(ctx, "d:x", "same")
	if b := balance(t, s, "d:x"); b.Total != 4 || b.UsedToday != 1 {
		t.Fatal(b)
	}
}

func TestRateLimitsAndDailyCap(t *testing.T) {
	s, clock := newService(t)
	s.Grant(ctx, "d:x", GrantInput{Bucket: BucketTrial, Amount: 100, SourceRef: "trial", Reason: "trial"})

	reserve(t, s, "d:x", "a", 0)
	reserve(t, s, "d:x", "b", 0)
	if r := reserve(t, s, "d:x", "c", 0); r.Code != "rate_limited" {
		t.Fatal("concurrency limit not enforced", r)
	}
	s.Commit(ctx, "d:x", "a")
	s.Commit(ctx, "d:x", "b")
	for _, k := range []string{"c", "d", "e", "f"} {
		reserve(t, s, "d:x", k, 0)
		s.Commit(ctx, "d:x", k)
	}
	if r := reserve(t, s, "d:x", "g", 0); r.Code != "rate_limited" {
		t.Fatal("per-minute limit not enforced", r)
	}
	*clock = clock.Add(61 * time.Second)
	if r := reserve(t, s, "d:x", "g", 0); !r.OK() {
		t.Fatal(r)
	}
	s.Commit(ctx, "d:x", "g")

	r, _ := s.Reserve(ctx, "d:x", ReserveInput{Key: "h", Cost: 1, DailyCap: 7, PerMinute: 100, MaxConcurrent: 5})
	if r.Code != "daily_cap" {
		t.Fatal("daily cap not enforced", r)
	}
}

func TestDailyRefillAndStaleHolds(t *testing.T) {
	s, clock := newService(t)
	if r := reserve(t, s, "u:1", "k", 5); !r.OK() || r.Remaining != 4 {
		t.Fatal("daily refill missing", r)
	}
	// 超时未确认的预扣自动退还
	*clock = clock.Add(holdTTL + time.Second)
	if err := s.ExpireAllStaleHolds(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Balance(ctx, "u:1", 5)
	if b.Total != 5 {
		t.Fatal(b)
	}
	// 第二天重新补充，前一天的过期
	*clock = clock.Add(24 * time.Hour)
	b, _ = s.Balance(ctx, "u:1", 5)
	if b.Total != 5 || len(b.Buckets) != 1 {
		t.Fatal(b)
	}
}

func TestRevoke(t *testing.T) {
	s, _ := newService(t)
	s.Grant(ctx, "u:1", GrantInput{Bucket: BucketSubscription, Amount: 1000, ExpiresAt: ptr(now + 1e9), SourceRef: "inv:1", Reason: "subscription_pro"})
	s.Grant(ctx, "u:1", GrantInput{Bucket: BucketBonus, Amount: 50, ExpiresAt: ptr(now + 1e9), SourceRef: "signup_bonus", Reason: "signup_bonus"})
	n, err := s.Revoke(ctx, "u:1", BucketSubscription, "")
	if err != nil || n != 1000 {
		t.Fatal(n, err)
	}
	if b := balance(t, s, "u:1"); b.Total != 50 {
		t.Fatal(b)
	}
}
