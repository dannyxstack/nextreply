package app

// 设备注册、邮箱验证码登录（浏览器 + PKCE + 本机回调）、令牌刷新、/v1/me。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
)

const (
	otpTTL         = 10 * time.Minute
	otpMaxAttempts = 5
	otpResend      = time.Minute
	otpPerIPPerDay = 10 // 管理后台也按这个值判断 IP 是否达到上限（见 IPLimits）
	authCodeTTL    = 5 * time.Minute
	refreshTTL     = 30 * 24 * time.Hour
)

var (
	// 只允许跳回本机回环地址（RFC 8252），防止授权码被重定向到第三方网站
	loopbackRe = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost):\d{2,5}/callback$`)
	hwHashRe   = regexp.MustCompile(`^[a-f0-9]{32,64}$`)
	sixDigits  = regexp.MustCompile(`^\d{6}$`)
)

func (s *Server) otpHash(emailNorm, code string) string {
	return authn.SHA256Hex(s.cfg.TokenSecret + ":otp:" + emailNorm + ":" + code)
}

func (s *Server) rowExists(ctx context.Context, query string, args ...any) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ensureDevice 确保设备已登记；新设备按规则发放体验额度。
// 设备注册接口和"持有旧版设备 token、但数据库里还没有记录"的设备都走这里。
func (s *Server) ensureDevice(ctx context.Context, deviceID, hwHash, ip string) error {
	now := s.nowMs()
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ?`, now, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var hw any
	if hwHash != "" {
		hw = hwHash
	}
	res, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO devices (id, hw_hash, first_ip, created_at, last_seen) VALUES (?, ?, ?, ?, ?)`, deviceID, hw, ip, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // 并发请求已经登记过
	}

	// 体验额度：同一台机器（硬件哈希相同）只发一次；同一 IP 每天最多发给 N 台新设备
	sameMachine := false
	if hwHash != "" {
		if sameMachine, err = s.rowExists(ctx, `SELECT 1 FROM devices WHERE hw_hash = ? AND trial_granted = 1`, hwHash); err != nil {
			return err
		}
	}
	// 后台可以暂停发放体验额度（例如遭遇批量注册时）；设备照常登记，登录后可用
	if sameMachine || !s.settings.TrialEnabled() || s.settings.TrialCredits() == 0 {
		return nil
	}
	ok, err := s.takeIfUnder(ctx, s.ipKey("trial", ip), s.settings.TrialsPerIP())
	if err != nil || !ok {
		return err
	}
	if _, err := s.credits.Grant(ctx, credits.DeviceOwner(deviceID), credits.GrantInput{
		Bucket: credits.BucketTrial, Amount: s.settings.TrialCredits(), SourceRef: "trial", Reason: "trial",
	}); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE devices SET trial_granted = 1 WHERE id = ?`, deviceID)
	return err
}

// ---------- 设备注册 + 体验额度 ----------

func (s *Server) deviceRegister(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	deviceID := str(body, "device_id")
	if !authn.IsValidDeviceID(deviceID) {
		return apierr.New(apierr.BadRequest, "Invalid device_id.")
	}
	hwHash := str(body, "hw_hash")
	if !hwHashRe.MatchString(hwHash) {
		hwHash = ""
	}
	ip := s.clientIP(r)
	ctx := r.Context()

	known, err := s.rowExists(ctx, `SELECT 1 FROM devices WHERE id = ?`, deviceID)
	if err != nil {
		return err
	}
	if !known {
		ok, err := s.takeIfUnder(ctx, s.ipKey("reg", ip), s.settings.RegisterPerIP())
		if err != nil {
			return err
		}
		if !ok {
			return apierr.New(apierr.QuotaExceeded, "Too many devices registered from this network today.")
		}
	}
	if err := s.ensureDevice(ctx, deviceID, hwHash, ip); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"token": authn.IssueDeviceToken(s.cfg.TokenSecret, deviceID)})
}

// ---------- 浏览器登录页 ----------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	p := loginParams{
		DeviceID:      q.Get("device_id"),
		RedirectURI:   q.Get("redirect_uri"),
		State:         q.Get("state"),
		CodeChallenge: q.Get("code_challenge"),
		TurnstileKey:  s.cfg.TurnstileSiteKey,
	}
	if !authn.IsValidDeviceID(p.DeviceID) || !loopbackRe.MatchString(p.RedirectURI) || p.State == "" || p.CodeChallenge == "" {
		return writeHTML(w, http.StatusBadRequest, messagePage("登录链接无效", "请回到 NextReply 重新点击登录。"))
	}
	return writeHTML(w, http.StatusOK, renderLoginPage(p))
}

func (s *Server) emailStart(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	email := str(body, "email")
	if !isValidEmail(email) {
		return apierr.New(apierr.BadRequest, "邮箱格式不正确。")
	}
	if isDisposable(email) {
		return apierr.New(apierr.BadRequest, "暂不支持临时邮箱，请换一个常用邮箱。")
	}
	ip := s.clientIP(r)
	if ok, err := s.verifyTurnstile(ctx, str(body, "turnstile"), ip); err != nil || !ok {
		if err != nil {
			slog.Warn("turnstile verify failed", "err", err.Error())
		}
		return apierr.New(apierr.BadRequest, "人机验证失败，请重试。")
	}

	emailNorm := normalizeEmail(email)
	now := s.nowMs()
	var prevCreated int64
	err = s.db.QueryRowContext(ctx, `SELECT created_at FROM otp_codes WHERE email_norm = ?`, emailNorm).Scan(&prevCreated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && now-prevCreated < otpResend.Milliseconds() {
		return apierr.New(apierr.RateLimited, "发送太频繁，请 1 分钟后再试。")
	}
	ok, err := s.takeIfUnder(ctx, s.ipKey("otp", ip), otpPerIPPerDay)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.New(apierr.RateLimited, "今天请求验证码的次数太多了，请明天再试。")
	}

	code := authn.RandomDigits(6)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO otp_codes (email_norm, code_hash, attempts, expires_at, created_at) VALUES (?, ?, 0, ?, ?)
		 ON CONFLICT(email_norm) DO UPDATE SET code_hash = excluded.code_hash, attempts = 0, expires_at = excluded.expires_at, created_at = excluded.created_at`,
		emailNorm, s.otpHash(emailNorm, code), now+otpTTL.Milliseconds(), now); err != nil {
		return err
	}

	devCode, err := s.sendLoginCode(ctx, email, code)
	if err != nil {
		slog.Error("send login code failed", "err", err.Error())
		return apierr.New(apierr.Internal, "验证码发送失败，请稍后再试。")
	}
	resp := map[string]any{"ok": true}
	if devCode != "" {
		resp["dev_code"] = devCode
	}
	return writeJSON(w, http.StatusOK, resp)
}

// signIn 找到或创建用户；把设备绑定到该用户；发放注册赠送（每台设备、每个账号各一次）。
func (s *Server) signIn(ctx context.Context, email, deviceID, ip string) (string, error) {
	now := s.nowMs()
	emailNorm := normalizeEmail(email)

	var userID, status string
	err := s.db.QueryRowContext(ctx, `SELECT id, status FROM users WHERE email_norm = ?`, emailNorm).Scan(&userID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		userID, status = authn.NewUUID(), "active"
		if _, err := s.db.ExecContext(ctx, `INSERT INTO users (id, email, email_norm, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(email_norm) DO NOTHING`,
			userID, email, emailNorm, now); err != nil {
			return "", err
		}
		// 并发注册同一邮箱时以先写入的为准
		err = s.db.QueryRowContext(ctx, `SELECT id, status FROM users WHERE email_norm = ?`, emailNorm).Scan(&userID, &status)
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", apierr.New(apierr.Unauthorized, "该账号已被停用，请联系客服。")
	}

	var bonusClaimed int
	err = s.db.QueryRowContext(ctx, `SELECT signup_bonus_claimed FROM devices WHERE id = ?`, deviceID).Scan(&bonusClaimed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, user_id, first_ip, created_at, last_seen) VALUES (?, ?, ?, ?, ?)`, deviceID, userID, ip, now, now); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET user_id = ?, last_seen = ? WHERE id = ?`, userID, now, deviceID); err != nil {
			return "", err
		}
	}

	// 每个账号最多 N 台设备：超出时让最早的设备下线
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM devices WHERE user_id = ? ORDER BY last_seen DESC`, userID)
	if err != nil {
		return "", err
	}
	var devices []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		devices = append(devices, id)
	}
	rows.Close()
	for i := credits.MaxDevicesPerUser; i < len(devices); i++ {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET user_id = NULL WHERE id = ?`, devices[i]); err != nil {
			return "", err
		}
		if err := s.revokeDevice(ctx, userID, devices[i]); err != nil {
			return "", err
		}
	}

	// 注册赠送：按 source_ref 幂等保证每个账号一次；devices 标记保证每台设备一次（防止一台机器注册多个号刷额度）
	if bonusClaimed == 0 {
		exp := now + int64(credits.SignupBonusDays)*24*time.Hour.Milliseconds()
		granted, err := s.credits.Grant(ctx, credits.UserOwner(userID), credits.GrantInput{
			Bucket: credits.BucketBonus, Amount: credits.SignupBonus, ExpiresAt: &exp, SourceRef: "signup_bonus", Reason: "signup_bonus",
		})
		if err != nil {
			return "", err
		}
		if granted {
			if _, err := s.db.ExecContext(ctx, `UPDATE devices SET signup_bonus_claimed = 1 WHERE id = ?`, deviceID); err != nil {
				return "", err
			}
		}
	}
	return userID, nil
}

func (s *Server) emailVerify(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	email, code := str(body, "email"), str(body, "code")
	deviceID, redirectURI := str(body, "device_id"), str(body, "redirect_uri")
	state, challenge := str(body, "state"), str(body, "code_challenge")
	if !isValidEmail(email) || !sixDigits.MatchString(code) {
		return apierr.New(apierr.BadRequest, "验证码不正确。")
	}
	if !authn.IsValidDeviceID(deviceID) || !loopbackRe.MatchString(redirectURI) || state == "" || challenge == "" {
		return apierr.New(apierr.BadRequest, "登录链接无效，请回到 NextReply 重新点击登录。")
	}

	emailNorm := normalizeEmail(email)
	var codeHash string
	var attempts int
	var expiresAt int64
	err = s.db.QueryRowContext(ctx, `SELECT code_hash, attempts, expires_at FROM otp_codes WHERE email_norm = ?`, emailNorm).Scan(&codeHash, &attempts, &expiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || expiresAt < s.nowMs() {
		return apierr.New(apierr.BadRequest, "验证码已过期，请重新获取。")
	}
	if attempts >= otpMaxAttempts {
		return apierr.New(apierr.RateLimited, "错误次数太多，请重新获取验证码。")
	}
	if !authn.SafeEqual(codeHash, s.otpHash(emailNorm, code)) {
		if _, err := s.db.ExecContext(ctx, `UPDATE otp_codes SET attempts = attempts + 1 WHERE email_norm = ?`, emailNorm); err != nil {
			return err
		}
		return apierr.New(apierr.BadRequest, "验证码不正确。")
	}
	// 删除成功才算消耗了验证码，防止并发请求重复使用同一个验证码
	res, err := s.db.ExecContext(ctx, `DELETE FROM otp_codes WHERE email_norm = ? AND code_hash = ?`, emailNorm, codeHash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierr.New(apierr.BadRequest, "验证码已过期，请重新获取。")
	}

	userID, err := s.signIn(ctx, email, deviceID, s.clientIP(r))
	if err != nil {
		return err
	}
	authCode := authn.RandomToken()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO auth_codes (code_hash, user_id, device_id, code_challenge, expires_at) VALUES (?, ?, ?, ?, ?)`,
		authn.SHA256Hex(authCode), userID, deviceID, challenge, s.nowMs()+authCodeTTL.Milliseconds()); err != nil {
		return err
	}

	redirect, _ := url.Parse(redirectURI)
	q := redirect.Query()
	q.Set("code", authCode)
	q.Set("state", state)
	redirect.RawQuery = q.Encode()
	return writeJSON(w, http.StatusOK, map[string]any{"redirect": redirect.String()})
}

// ---------- 令牌 ----------
// refresh token 只存哈希；检测到已作废的 refresh token 被再次使用时，说明令牌可能被盗，整条令牌链作废。

func (s *Server) issueRefresh(ctx context.Context, userID, deviceID, familyID string) (string, error) {
	if familyID == "" {
		familyID = authn.NewUUID()
	}
	token := authn.RandomToken()
	now := s.nowMs()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (id, user_id, device_id, family_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		authn.NewUUID(), userID, deviceID, familyID, authn.SHA256Hex(token), now+refreshTTL.Milliseconds(), now)
	return token, err
}

func (s *Server) revokeFamily(ctx context.Context, familyID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`, s.nowMs(), familyID)
	return err
}

func (s *Server) revokeDevice(ctx context.Context, userID, deviceID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND device_id = ? AND revoked_at IS NULL`, s.nowMs(), userID, deviceID)
	return err
}

func (s *Server) tokenResponse(ctx context.Context, userID, deviceID, refresh string) (map[string]any, error) {
	var email string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return map[string]any{
		"access_token":  authn.SignAccess(s.cfg.TokenSecret, userID, deviceID, s.now()),
		"expires_in":    int(authn.AccessTTL.Seconds()),
		"refresh_token": refresh,
		"user":          map[string]any{"id": userID, "email": email},
	}, nil
}

func (s *Server) authToken(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	code, verifier, deviceID := str(body, "code"), str(body, "code_verifier"), str(body, "device_id")
	if code == "" || len(verifier) < 43 || !authn.IsValidDeviceID(deviceID) {
		return apierr.New(apierr.BadRequest, "Invalid token request.")
	}

	hash := authn.SHA256Hex(code)
	var userID, codeDevice, challenge string
	var expiresAt int64
	var used int
	err = s.db.QueryRowContext(ctx, `SELECT user_id, device_id, code_challenge, expires_at, used FROM auth_codes WHERE code_hash = ?`, hash).
		Scan(&userID, &codeDevice, &challenge, &expiresAt, &used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil || used != 0 || expiresAt < s.nowMs() || codeDevice != deviceID {
		return apierr.New(apierr.Unauthorized, "Authorization code is invalid or expired.")
	}
	if !authn.SafeEqual(challenge, authn.PKCEChallenge(verifier)) {
		return apierr.New(apierr.Unauthorized, "PKCE verification failed.")
	}
	// 条件更新保证授权码只能兑换一次
	res, err := s.db.ExecContext(ctx, `UPDATE auth_codes SET used = 1 WHERE code_hash = ? AND used = 0`, hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierr.New(apierr.Unauthorized, "Authorization code is invalid or expired.")
	}

	refresh, err := s.issueRefresh(ctx, userID, deviceID, "")
	if err != nil {
		return err
	}
	resp, err := s.tokenResponse(ctx, userID, deviceID, refresh)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authRefresh(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	fail := func(reason string) error {
		return apierr.WithDetails(apierr.Unauthorized, "Session expired, please sign in again.", map[string]any{"reason": "refresh_" + reason})
	}

	var id, userID, deviceID, familyID string
	var expiresAt int64
	var revokedAt sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT id, user_id, device_id, family_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = ?`,
		authn.SHA256Hex(str(body, "refresh_token"))).Scan(&id, &userID, &deviceID, &familyID, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fail("invalid")
	}
	if err != nil {
		return err
	}
	if revokedAt.Valid {
		// 已轮换过的令牌被再次使用：作废整条令牌链，强制重新登录
		if err := s.revokeFamily(ctx, familyID); err != nil {
			return err
		}
		return fail("reused")
	}
	if deviceID != str(body, "device_id") {
		return fail("device_mismatch")
	}
	if expiresAt < s.nowMs() {
		return fail("expired")
	}

	// 条件更新：两个并发刷新只有一个成功，另一个按重用处理
	res, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.nowMs(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if err := s.revokeFamily(ctx, familyID); err != nil {
			return err
		}
		return fail("reused")
	}
	refresh, err := s.issueRefresh(ctx, userID, deviceID, familyID)
	if err != nil {
		return err
	}
	resp, err := s.tokenResponse(ctx, userID, deviceID, refresh)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) error {
	body, err := readJSON(r)
	if err != nil {
		return err
	}
	if token := str(body, "refresh_token"); token != "" {
		var familyID string
		err := s.db.QueryRowContext(r.Context(), `SELECT family_id FROM refresh_tokens WHERE token_hash = ?`, authn.SHA256Hex(token)).Scan(&familyID)
		if err == nil {
			if err := s.revokeFamily(r.Context(), familyID); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 账户信息 ----------

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	caller, err := s.resolveCaller(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	plan := s.settings.Plan(caller.Plan)
	bal, err := s.credits.Balance(ctx, caller.Owner, plan.DailyRefill)
	if err != nil {
		return err
	}
	var monthly any
	if plan.MonthlyCredits > 0 {
		monthly = plan.MonthlyCredits
	}
	paid := []map[string]any{}
	for _, id := range credits.PaidPlans {
		p := s.settings.Plan(id)
		paid = append(paid, map[string]any{"id": id, "label": p.Label, "price_usd": p.PriceUSD, "monthly_credits": p.MonthlyCredits, "daily_cap": p.DailyCap})
	}
	resp := map[string]any{
		"plan":         map[string]any{"id": plan.ID, "label": plan.Label, "daily_cap": plan.DailyCap, "monthly_credits": monthly},
		"credits":      map[string]any{"total": bal.Total, "buckets": bal.Buckets, "used_today": bal.UsedToday},
		"billing":      map[string]any{"stripe": s.cfg.StripeSecretKey != "", "dev": s.cfg.DevMode, "plans": paid},
		"user":         nil,
		"subscription": nil,
	}
	if caller.Kind == "device" {
		return writeJSON(w, http.StatusOK, resp)
	}

	var email string
	if err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, caller.UserID).Scan(&email); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	resp["user"] = map[string]any{"id": caller.UserID, "email": email}
	sub, err := getSubscription(ctx, s.db, caller.UserID)
	if err != nil {
		return err
	}
	if sub != nil && planFromSubscription(sub, s.nowMs()) != credits.PlanFree {
		var periodEnd any
		if sub.CurrentPeriodEnd.Valid {
			periodEnd = sub.CurrentPeriodEnd.Int64
		}
		resp["subscription"] = map[string]any{
			"plan": sub.Plan, "status": sub.Status, "provider": sub.Provider,
			"current_period_end": periodEnd, "cancel_at_period_end": sub.CancelAtPeriodEnd,
		}
	}
	return writeJSON(w, http.StatusOK, resp)
}
