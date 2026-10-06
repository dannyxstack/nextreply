package app

// 识别调用方：已登录用户（access token）或匿名设备（设备 token），并确定当前套餐。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
)

type Caller struct {
	Kind     string // "user" | "device"
	UserID   string
	DeviceID string
	Owner    string
	Plan     credits.PlanID
}

type Subscription struct {
	Plan               string
	Status             string
	Provider           string
	CustomerID         sql.NullString
	SubscriptionID     sql.NullString
	CurrentPeriodStart sql.NullInt64
	CurrentPeriodEnd   sql.NullInt64
	CancelAtPeriodEnd  bool
}

func getSubscription(ctx context.Context, db *sql.DB, userID string) (*Subscription, error) {
	var s Subscription
	var cancel int
	err := db.QueryRowContext(ctx,
		`SELECT plan, status, provider, customer_id, subscription_id, current_period_start, current_period_end, cancel_at_period_end
		 FROM subscriptions WHERE user_id = ?`, userID).
		Scan(&s.Plan, &s.Status, &s.Provider, &s.CustomerID, &s.SubscriptionID, &s.CurrentPeriodStart, &s.CurrentPeriodEnd, &cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.CancelAtPeriodEnd = cancel != 0
	return &s, nil
}

func planFromSubscription(sub *Subscription, nowMs int64) credits.PlanID {
	if sub == nil {
		return credits.PlanFree
	}
	return credits.ResolvePlan(sub.Plan, sub.Status, sub.CurrentPeriodEnd.Int64, nowMs)
}

func bearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

func (s *Server) resolveCaller(r *http.Request) (*Caller, error) {
	ctx := r.Context()
	token := bearer(r)
	if token == "" {
		return nil, apierr.New(apierr.Unauthorized, "Missing token.")
	}

	// JWT 有三段，设备 token 有两段
	if strings.Count(token, ".") == 2 {
		claims, err := authn.VerifyAccess(s.cfg.TokenSecret, token, s.now())
		if err != nil {
			return nil, apierr.WithDetails(apierr.Unauthorized, "Invalid or expired access token.", map[string]any{"reason": "access_expired"})
		}
		var status string
		err = s.db.QueryRowContext(ctx, `SELECT status FROM users WHERE id = ?`, claims.Sub).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if status != "active" {
			return nil, apierr.WithDetails(apierr.Unauthorized, "Account unavailable.", map[string]any{"reason": "account_unavailable"})
		}
		sub, err := getSubscription(ctx, s.db, claims.Sub)
		if err != nil {
			return nil, err
		}
		return &Caller{Kind: "user", UserID: claims.Sub, DeviceID: claims.Did, Owner: credits.UserOwner(claims.Sub), Plan: planFromSubscription(sub, s.nowMs())}, nil
	}

	deviceID, ok := authn.VerifyDeviceToken(s.cfg.TokenSecret, token)
	if !ok {
		return nil, apierr.New(apierr.Unauthorized, "Invalid device token.")
	}
	// 旧版本注册的设备持有有效 token，但数据库里可能还没有记录：首次请求时补登记
	if err := s.ensureDevice(ctx, deviceID, "", s.clientIP(r)); err != nil {
		return nil, err
	}
	return &Caller{Kind: "device", DeviceID: deviceID, Owner: credits.DeviceOwner(deviceID), Plan: credits.PlanTrial}, nil
}
