package credits

// 积分账户（用户 u:<id> 或匿名设备 d:<id>）。
//
// 扣费流程：Reserve（预扣 + 限流）→ 调用模型 → Commit（确认）或 Release（退还）。
// 只有成功生成回复才 Commit，失败、非聊天截图等情况都 Release，不扣用户积分。
//
// 所有修改都在一个进程内互斥锁 + SQLite 事务里完成：互斥锁保证"检查余额 → 扣减"不会被并发打断
// （也保护内存里的每分钟计数），事务保证进程崩溃时不会写一半。服务按单实例部署（见 README）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nextreply/server/internal/authn"
)

// 超过这个时间仍未 commit / release 的预扣视为异常中断，自动退还
const holdTTL = 5 * time.Minute

type Service struct {
	db  *sql.DB
	mu  sync.Mutex
	now func() time.Time
	// 每个账户最近一分钟成功预扣的时间戳（只在内存中；重启后重置可以接受）
	recent map[string][]int64
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, recent: map[string][]int64{}}
}

type GrantInput struct {
	Bucket Bucket
	Amount int
	// 毫秒时间戳；nil 表示不过期
	ExpiresAt *int64
	// 幂等键：同一来源（如某张发票、某天的补充）只发放一次
	SourceRef string
	Reason    string
}

type ReserveInput struct {
	Key           string
	Cost          int
	DailyCap      int
	PerMinute     int
	MaxConcurrent int
	DailyRefill   int
}

// ReserveResult Code 为空表示成功；否则是 insufficient_credits / rate_limited / daily_cap。
type ReserveResult struct {
	Code      string
	Remaining int
}

func (r ReserveResult) OK() bool { return r.Code == "" }

type Balance struct {
	Total     int
	Buckets   []BucketSummary
	UsedToday int
}

func (s *Service) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func grants(ctx context.Context, tx *sql.Tx, owner string) ([]Grant, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, bucket, remaining, expires_at, created_at FROM credit_grants WHERE owner = ? AND remaining > 0`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		var exp sql.NullInt64
		if err := rows.Scan(&g.ID, &g.Bucket, &g.Remaining, &exp, &g.CreatedAt); err != nil {
			return nil, err
		}
		if exp.Valid {
			v := exp.Int64
			g.ExpiresAt = &v
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func insertGrant(ctx context.Context, tx *sql.Tx, owner string, g GrantInput, now int64) (bool, error) {
	if g.Amount <= 0 {
		return false, nil
	}
	id := authn.NewUUID()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO credit_grants (id, owner, bucket, amount, remaining, expires_at, source_ref, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(owner, source_ref) DO NOTHING`,
		id, owner, g.Bucket, g.Amount, g.Amount, g.ExpiresAt, g.SourceRef, g.Reason, now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_ledger (owner, delta, reason, grant_id, ref, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		owner, g.Amount, g.Reason, id, g.SourceRef, now)
	return err == nil, err
}

func refillDaily(ctx context.Context, tx *sql.Tx, owner string, amount int, now int64) error {
	if amount <= 0 {
		return nil
	}
	exp := EndOfUTCDay(now)
	_, err := insertGrant(ctx, tx, owner, GrantInput{Bucket: BucketDaily, Amount: amount, ExpiresAt: &exp, SourceRef: "daily:" + UTCDay(now), Reason: "daily_refill"}, now)
	return err
}

func returnAllocations(ctx context.Context, tx *sql.Tx, allocJSON string) error {
	var allocs []Allocation
	if err := json.Unmarshal([]byte(allocJSON), &allocs); err != nil {
		return err
	}
	for _, a := range allocs {
		if _, err := tx.ExecContext(ctx, `UPDATE credit_grants SET remaining = remaining + ? WHERE id = ?`, a.Amount, a.GrantID); err != nil {
			return err
		}
	}
	return nil
}

func expireStaleHolds(ctx context.Context, tx *sql.Tx, owner string, now int64) error {
	type hold struct{ owner, key, allocs string }
	query := `SELECT owner, key, allocations FROM credit_holds WHERE created_at < ?`
	args := []any{now - holdTTL.Milliseconds()}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var stale []hold
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.owner, &h.key, &h.allocs); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, h)
	}
	rows.Close()
	for _, h := range stale {
		if err := returnAllocations(ctx, tx, h.allocs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM credit_holds WHERE owner = ? AND key = ?`, h.owner, h.key); err != nil {
			return err
		}
	}
	return nil
}

func usedToday(ctx context.Context, tx *sql.Tx, owner string, now int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count FROM credit_usage_day WHERE owner = ? AND day = ?`, owner, UTCDay(now)).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func exists(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func total(ctx context.Context, tx *sql.Tx, owner string, now int64) (int, error) {
	gs, err := grants(ctx, tx, owner)
	if err != nil {
		return 0, err
	}
	t, _ := Summarize(gs, now)
	return t, nil
}

// Grant 发放积分（幂等）。返回 false 表示该来源已发放过。
func (s *Service) Grant(ctx context.Context, owner string, g GrantInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ok bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		ok, err = insertGrant(ctx, tx, owner, g, s.now().UnixMilli())
		return err
	})
	return ok, err
}

func (s *Service) Reserve(ctx context.Context, owner string, in ReserveInput) (ReserveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	var res ReserveResult
	reserved := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := expireStaleHolds(ctx, tx, owner, now); err != nil {
			return err
		}
		if err := refillDaily(ctx, tx, owner, in.DailyRefill, now); err != nil {
			return err
		}
		finish := func(code string) error {
			t, err := total(ctx, tx, owner, now)
			res = ReserveResult{Code: code, Remaining: t}
			return err
		}

		// 同一个幂等键重复请求（客户端网络重试）：进行中的直接视为已预扣
		if ok, err := exists(ctx, tx, `SELECT 1 FROM credit_holds WHERE owner = ? AND key = ?`, owner, in.Key); err != nil || ok {
			if err != nil {
				return err
			}
			return finish("")
		}
		// 已经扣过费的：放行但不再扣（空预扣，commit 时不写流水、不计用量）
		if ok, err := exists(ctx, tx, `SELECT 1 FROM credit_ledger WHERE owner = ? AND ref = ? AND reason = 'reply' LIMIT 1`, owner, in.Key); err != nil || ok {
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credit_holds (owner, key, allocations, created_at) VALUES (?, ?, '[]', ?)`, owner, in.Key, now); err != nil {
				return err
			}
			return finish("")
		}

		var inFlight int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credit_holds WHERE owner = ?`, owner).Scan(&inFlight); err != nil {
			return err
		}
		if len(s.recentSince(owner, now-60_000)) >= in.PerMinute || inFlight >= in.MaxConcurrent {
			return finish("rate_limited")
		}
		used, err := usedToday(ctx, tx, owner, now)
		if err != nil {
			return err
		}
		if used+inFlight >= in.DailyCap {
			return finish("daily_cap")
		}

		gs, err := grants(ctx, tx, owner)
		if err != nil {
			return err
		}
		allocs := Allocate(gs, in.Cost, now)
		if allocs == nil {
			return finish("insufficient_credits")
		}
		for _, a := range allocs {
			if _, err := tx.ExecContext(ctx, `UPDATE credit_grants SET remaining = remaining - ? WHERE id = ?`, a.Amount, a.GrantID); err != nil {
				return err
			}
		}
		b, _ := json.Marshal(allocs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO credit_holds (owner, key, allocations, created_at) VALUES (?, ?, ?, ?)`, owner, in.Key, string(b), now); err != nil {
			return err
		}
		reserved = true
		return finish("")
	})
	if err != nil {
		return ReserveResult{}, err
	}
	if reserved {
		s.recent[owner] = append(s.recentSince(owner, now-60_000), now)
	}
	return res, nil
}

func (s *Service) recentSince(owner string, since int64) []int64 {
	kept := s.recent[owner][:0]
	for _, t := range s.recent[owner] {
		if t > since {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(s.recent, owner)
		return nil
	}
	s.recent[owner] = kept
	return kept
}

// Commit 确认扣费：写流水、计入当日用量。
func (s *Service) Commit(ctx context.Context, owner, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var allocJSON string
		err := tx.QueryRowContext(ctx, `SELECT allocations FROM credit_holds WHERE owner = ? AND key = ?`, owner, key).Scan(&allocJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var allocs []Allocation
		if err := json.Unmarshal([]byte(allocJSON), &allocs); err != nil {
			return err
		}
		// 空预扣：重试已扣过费的请求，只删掉占位
		for _, a := range allocs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO credit_ledger (owner, delta, reason, grant_id, ref, created_at) VALUES (?, ?, 'reply', ?, ?, ?)`,
				owner, -a.Amount, a.GrantID, key, now); err != nil {
				return err
			}
		}
		if len(allocs) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO credit_usage_day (owner, day, count) VALUES (?, ?, 1) ON CONFLICT(owner, day) DO UPDATE SET count = count + 1`,
				owner, UTCDay(now)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM credit_holds WHERE owner = ? AND key = ?`, owner, key)
		return err
	})
}

// Release 退还预扣（请求失败、非聊天截图、被取消）。
func (s *Service) Release(ctx context.Context, owner, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var allocJSON string
		err := tx.QueryRowContext(ctx, `SELECT allocations FROM credit_holds WHERE owner = ? AND key = ?`, owner, key).Scan(&allocJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := returnAllocations(ctx, tx, allocJSON); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM credit_holds WHERE owner = ? AND key = ?`, owner, key)
		return err
	})
}

func (s *Service) Balance(ctx context.Context, owner string, dailyRefill int) (Balance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	var b Balance
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := expireStaleHolds(ctx, tx, owner, now); err != nil {
			return err
		}
		if err := refillDaily(ctx, tx, owner, dailyRefill, now); err != nil {
			return err
		}
		gs, err := grants(ctx, tx, owner)
		if err != nil {
			return err
		}
		b.Total, b.Buckets = Summarize(gs, now)
		b.UsedToday, err = usedToday(ctx, tx, owner, now)
		return err
	})
	return b, err
}

// Revoke 收回某个桶（或某个来源）的剩余积分：退款、拒付、订阅被撤销时使用。
func (s *Service) Revoke(ctx context.Context, owner string, bucket Bucket, sourceRef string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	revoked := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		query := `SELECT id, remaining FROM credit_grants WHERE owner = ? AND bucket = ? AND remaining > 0`
		args := []any{owner, bucket}
		if sourceRef != "" {
			query += ` AND source_ref = ?`
			args = append(args, sourceRef)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		type row struct {
			id        string
			remaining int
		}
		var list []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.remaining); err != nil {
				rows.Close()
				return err
			}
			list = append(list, r)
		}
		rows.Close()
		var ref any
		if sourceRef != "" {
			ref = sourceRef
		}
		for _, r := range list {
			revoked += r.remaining
			if _, err := tx.ExecContext(ctx, `UPDATE credit_grants SET remaining = 0 WHERE id = ?`, r.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credit_ledger (owner, delta, reason, grant_id, ref, created_at) VALUES (?, ?, 'revoke', ?, ?, ?)`,
				owner, -r.remaining, r.id, ref, now); err != nil {
				return err
			}
		}
		return nil
	})
	return revoked, err
}

// ExpireAllStaleHolds 后台定时任务调用：退还所有账户中超时的预扣。
func (s *Service) ExpireAllStaleHolds(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return expireStaleHolds(ctx, tx, "", s.now().UnixMilli())
	})
}
