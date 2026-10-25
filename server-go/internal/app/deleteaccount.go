package app

// 删除账号（TECH_DESIGN §10.3）：网页账户和管理后台共用。
//
// 删除邮箱、登录信息、订阅记录和积分；设备回到匿名状态（保留"已领取注册赠送"标记，防止重复领取）；
// 用量记录去掉账户、IP 和设备后保留，用于对账和防滥用。另存一份加盐的邮箱哈希，删号后重新注册不再送注册赠送。

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
)

// ErrSubscriptionCancel 无法在 Stripe 取消订阅时不删除账号，避免删号后继续扣费
var ErrSubscriptionCancel = errors.New("could not cancel the Stripe subscription")

func (s *Server) deletedEmailHash(email string) string {
	return authn.SHA256Hex(s.cfg.TokenSecret + ":deleted:" + normalizeEmail(email))
}

func (s *Server) userExists(ctx context.Context, userID string) (bool, error) {
	return s.rowExists(ctx, `SELECT 1 FROM users WHERE id = ?`, userID)
}

// DeleteUser 删除账号及个人数据。账号不存在时返回 sql.ErrNoRows。
func (s *Server) DeleteUser(ctx context.Context, userID string) error {
	var email string
	if err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		return err
	}

	// 先在 Stripe 取消仍在扣费的订阅；失败就整个中止
	sub, err := getSubscription(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if sub != nil && sub.Provider == "stripe" && sub.SubscriptionID.Valid && sub.Status != "canceled" {
		sc := s.stripe()
		if sc == nil {
			return ErrSubscriptionCancel
		}
		if err := sc.cancelSubscription(ctx, sub.SubscriptionID.String); err != nil {
			slog.Error("stripe cancel on account deletion failed", "err", err.Error())
			return ErrSubscriptionCancel
		}
	}

	owner := credits.UserOwner(userID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT OR IGNORE INTO deleted_emails (email_hash, deleted_at) VALUES (?, ?)`, []any{s.deletedEmailHash(email), s.nowMs()}},
		{`DELETE FROM refresh_tokens WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM web_sessions WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM auth_codes WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM web_tickets WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM otp_codes WHERE email_norm = ?`, []any{normalizeEmail(email)}},
		{`UPDATE devices SET user_id = NULL WHERE user_id = ?`, []any{userID}},
		{`DELETE FROM subscriptions WHERE user_id = ?`, []any{userID}},
		{`UPDATE usage_events SET owner = 'u:deleted', ip = NULL, device_id = NULL WHERE owner = ?`, []any{owner}},
		{`DELETE FROM users WHERE id = ?`, []any{userID}},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 积分只通过 credits.Service 修改
	if err := s.credits.DeleteOwner(ctx, owner); err != nil {
		return err
	}
	slog.Info("account_deleted", "user", userID[:min(8, len(userID))])
	return nil
}
