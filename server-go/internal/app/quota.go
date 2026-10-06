package app

// 按 IP 的每日计数（软限制，防刷的外层防线）。精确的账户积分在 credits 包里。

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const ipCounterTTL = 2 * 24 * time.Hour

func (s *Server) ipKey(kind, ip string) string {
	return kind + ":" + ip + ":" + s.now().UTC().Format("2006-01-02")
}

func (s *Server) readCount(ctx context.Context, key string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count FROM ip_counters WHERE key = ?`, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func (s *Server) bump(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ip_counters (key, count, expires_at) VALUES (?, 1, ?) ON CONFLICT(key) DO UPDATE SET count = count + 1`,
		key, s.now().Add(ipCounterTTL).UnixMilli())
	return err
}

// takeIfUnder 未达上限时计数 +1 并返回 true；已达上限返回 false。单条语句完成，没有竞态。
func (s *Server) takeIfUnder(ctx context.Context, key string, limit int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ip_counters (key, count, expires_at) VALUES (?, 1, ?)
		 ON CONFLICT(key) DO UPDATE SET count = count + 1 WHERE ip_counters.count < ?`,
		key, s.now().Add(ipCounterTTL).UnixMilli(), limit)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
