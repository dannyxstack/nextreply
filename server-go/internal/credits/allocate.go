package credits

// 积分分配的纯函数部分（不依赖数据库，便于单元测试）。

import (
	"math"
	"sort"
	"time"
)

type Bucket string

const (
	BucketTrial        Bucket = "trial"
	BucketBonus        Bucket = "bonus"
	BucketDaily        Bucket = "daily"
	BucketSubscription Bucket = "subscription"
	BucketTopup        Bucket = "topup"
)

type Grant struct {
	ID        string
	Bucket    Bucket
	Remaining int
	// 毫秒时间戳；nil 表示不过期
	ExpiresAt *int64
	CreatedAt int64
}

type Allocation struct {
	GrantID string `json:"grant_id"`
	Amount  int    `json:"amount"`
}

func usable(g Grant, now int64) bool {
	return g.Remaining > 0 && (g.ExpiresAt == nil || *g.ExpiresAt > now)
}

func expiryKey(g Grant) int64 {
	if g.ExpiresAt == nil {
		return math.MaxInt64
	}
	return *g.ExpiresAt
}

// SpendOrder 扣费顺序：先扣最快过期的桶，不过期的最后扣；到期时间相同时先扣先发放的。
func SpendOrder(grants []Grant, now int64) []Grant {
	out := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if usable(g, now) {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := expiryKey(out[i]), expiryKey(out[j])
		if ei != ej {
			return ei < ej
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// Allocate 从各桶中凑出 cost 积分；余额不足返回 nil（不做部分扣费）。
func Allocate(grants []Grant, cost int, now int64) []Allocation {
	out := []Allocation{}
	left := cost
	for _, g := range SpendOrder(grants, now) {
		if left <= 0 {
			break
		}
		take := min(g.Remaining, left)
		out = append(out, Allocation{GrantID: g.ID, Amount: take})
		left -= take
	}
	if left > 0 {
		return nil
	}
	return out
}

type BucketSummary struct {
	Bucket    Bucket `json:"bucket"`
	Remaining int    `json:"remaining"`
	ExpiresAt *int64 `json:"expires_at"`
}

// Summarize 可用余额汇总：同一桶内按最早到期时间合并显示。
func Summarize(grants []Grant, now int64) (int, []BucketSummary) {
	total := 0
	buckets := []BucketSummary{}
	index := map[Bucket]int{}
	for _, g := range SpendOrder(grants, now) {
		total += g.Remaining
		if i, ok := index[g.Bucket]; ok {
			buckets[i].Remaining += g.Remaining
			continue
		}
		index[g.Bucket] = len(buckets)
		buckets = append(buckets, BucketSummary{Bucket: g.Bucket, Remaining: g.Remaining, ExpiresAt: g.ExpiresAt})
	}
	return total, buckets
}

func UTCDay(nowMs int64) string { return time.UnixMilli(nowMs).UTC().Format("2006-01-02") }

func EndOfUTCDay(nowMs int64) int64 {
	t := time.UnixMilli(nowMs).UTC()
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC).UnixMilli()
}
