package admin

// 阶段 2：管理操作。每个操作都是 POST + CSRF 校验，成功后写 admin_audit，再重定向回原页面（POST-Redirect-GET）。

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
)

const (
	maxGrant      = 10_000
	maxGrantDays  = 3650
	maxReasonLen  = 200
	actionTimeout = 10 * time.Second
)

// checkCSRF Basic 认证的凭据浏览器会自动带上，所以修改操作必须防 CSRF：
// 表单里的令牌只有能读到后台页面的人才拿得到；同时要求请求来自同源页面。
func (a *Admin) checkCSRF(r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(a.csrf)) != 1 {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// actor 操作者：经过 Cloudflare Access 时用其认证的邮箱，否则记为 admin
func actor(r *http.Request) string {
	if email := strings.TrimSpace(r.Header.Get("Cf-Access-Authenticated-User-Email")); email != "" {
		return email
	}
	return "admin"
}

func requestIP(r *http.Request) string {
	if ip := r.Header.Get("Cf-Connecting-Ip"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// actionError 给操作者看的错误（参数不对、对象不存在），显示在页面上而不是 500
type actionError string

func (e actionError) Error() string { return string(e) }

// action 包装一个修改操作：校验 CSRF、执行、记审计、重定向并带上结果提示。
func (a *Admin) action(name string, run func(ctx context.Context, r *http.Request) (target string, detail map[string]any, msg string, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil || !a.checkCSRF(r) {
			http.Error(w, "invalid form or CSRF token, reload the page and try again", http.StatusForbidden)
			return
		}
		back := r.PostFormValue("back")
		if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
			back = "/"
		}
		ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
		defer cancel()

		target, detail, msg, err := run(ctx, r)
		if err == nil {
			err = a.audit(ctx, name, target, detail, r)
		}
		var ae actionError
		switch {
		case errors.As(err, &ae):
			msg = "操作失败：" + string(ae)
		case err != nil:
			slog.Error("admin action failed", "action", name, "err", err.Error())
			msg = "操作失败：服务器错误，详见日志"
		default:
			slog.Info("admin_action", "action", name, "target", target, "actor", actor(r))
		}
		http.Redirect(w, r, withMsg(back, msg), http.StatusSeeOther)
	}
}

func withMsg(path, msg string) string {
	u, err := url.Parse(path)
	if err != nil {
		return "/"
	}
	q := u.Query()
	q.Set("msg", msg)
	u.RawQuery = q.Encode()
	return u.String()
}

func (a *Admin) audit(ctx context.Context, action, target string, detail map[string]any, r *http.Request) error {
	b, _ := json.Marshal(detail)
	_, err := a.db.ExecContext(ctx, `INSERT INTO admin_audit (created_at, action, target, detail, actor, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		a.now().UnixMilli(), action, target, string(b), actor(r), requestIP(r))
	return err
}

func reason(r *http.Request, required bool) (string, error) {
	s := strings.TrimSpace(r.PostFormValue("reason"))
	if required && s == "" {
		return "", actionError("请填写原因")
	}
	if len(s) > maxReasonLen {
		return "", actionError(fmt.Sprintf("原因不能超过 %d 个字符", maxReasonLen))
	}
	return s, nil
}

func (a *Admin) exists(ctx context.Context, query, id string) error {
	var one int
	err := a.db.QueryRowContext(ctx, query, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return actionError("对象不存在")
	}
	return err
}

// ---------- 具体操作 ----------

// grantCredits 补发积分（用户或匿名设备），记为 bonus 桶，可设到期天数
func (a *Admin) grantCredits(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	var owner string
	switch kind {
	case "u":
		owner = credits.UserOwner(id)
		if err := a.exists(ctx, `SELECT 1 FROM users WHERE id = ?`, id); err != nil {
			return owner, nil, "", err
		}
	case "d":
		owner = credits.DeviceOwner(id)
		if err := a.exists(ctx, `SELECT 1 FROM devices WHERE id = ?`, id); err != nil {
			return owner, nil, "", err
		}
	default:
		return "", nil, "", actionError("未知的账户类型")
	}
	amount, err := strconv.Atoi(r.PostFormValue("amount"))
	if err != nil || amount < 1 || amount > maxGrant {
		return owner, nil, "", actionError(fmt.Sprintf("数量必须是 1–%d 的整数", maxGrant))
	}
	days, err := strconv.Atoi(r.PostFormValue("days"))
	if err != nil || days < 0 || days > maxGrantDays {
		return owner, nil, "", actionError(fmt.Sprintf("有效天数必须是 0–%d 的整数（0 表示不过期）", maxGrantDays))
	}
	why, err := reason(r, true)
	if err != nil {
		return owner, nil, "", err
	}
	var exp *int64
	if days > 0 {
		v := a.now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
		exp = &v
	}
	if _, err := a.credits.Grant(ctx, owner, credits.GrantInput{
		Bucket: credits.BucketBonus, Amount: amount, ExpiresAt: exp,
		SourceRef: "admin:" + authn.NewUUID(), Reason: "admin_grant",
	}); err != nil {
		return owner, nil, "", err
	}
	return owner, map[string]any{"amount": amount, "days": days, "reason": why}, fmt.Sprintf("已补发 %d 积分", amount), nil
}

// setUserStatus 停用 / 恢复账号。停用时同时作废全部登录会话：access token 每次请求都会查账号状态，立即失效。
func (a *Admin) setUserStatus(status string) func(context.Context, *http.Request) (string, map[string]any, string, error) {
	return func(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
		id := r.PathValue("id")
		target := credits.UserOwner(id)
		why, err := reason(r, status == "suspended")
		if err != nil {
			return target, nil, "", err
		}
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			return target, nil, "", err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `UPDATE users SET status = ? WHERE id = ?`, status, id)
		if err != nil {
			return target, nil, "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return target, nil, "", actionError("用户不存在")
		}
		revoked := int64(0)
		if status == "suspended" {
			if revoked, err = revokeTokens(ctx, tx, a.now(), `user_id = ?`, id); err != nil {
				return target, nil, "", err
			}
		}
		if err := tx.Commit(); err != nil {
			return target, nil, "", err
		}
		msg := "账号已恢复"
		if status == "suspended" {
			msg = fmt.Sprintf("账号已停用，作废了 %d 个登录会话", revoked)
		}
		return target, map[string]any{"status": status, "reason": why, "sessions_revoked": revoked}, msg, nil
	}
}

// revokeUserSessions 强制下线：作废该用户所有设备的 refresh token；access token 最多 15 分钟后过期。
func (a *Admin) revokeUserSessions(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
	id := r.PathValue("id")
	target := credits.UserOwner(id)
	if err := a.exists(ctx, `SELECT 1 FROM users WHERE id = ?`, id); err != nil {
		return target, nil, "", err
	}
	n, err := revokeTokens(ctx, a.db, a.now(), `user_id = ?`, id)
	if err != nil {
		return target, nil, "", err
	}
	return target, map[string]any{"sessions_revoked": n}, fmt.Sprintf("已作废 %d 个登录会话，最多 15 分钟后完全下线", n), nil
}

func (a *Admin) revokeDeviceSessions(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
	id := r.PathValue("id")
	target := credits.DeviceOwner(id)
	if err := a.exists(ctx, `SELECT 1 FROM devices WHERE id = ?`, id); err != nil {
		return target, nil, "", err
	}
	n, err := revokeTokens(ctx, a.db, a.now(), `device_id = ?`, id)
	if err != nil {
		return target, nil, "", err
	}
	return target, map[string]any{"sessions_revoked": n}, fmt.Sprintf("已作废这台设备的 %d 个登录会话", n), nil
}

// unbindDevice 解绑设备：设备回到匿名状态并下线，用户需要在这台设备上重新登录。
// 注册赠送的领取标记保留，解绑后再注册新账号也不会重复送。
func (a *Admin) unbindDevice(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
	id := r.PathValue("id")
	target := credits.DeviceOwner(id)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return target, nil, "", err
	}
	defer tx.Rollback()
	var userID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM devices WHERE id = ?`, id).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return target, nil, "", actionError("设备不存在")
	}
	if err != nil {
		return target, nil, "", err
	}
	if !userID.Valid {
		return target, nil, "", actionError("设备没有绑定账号")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET user_id = NULL WHERE id = ?`, id); err != nil {
		return target, nil, "", err
	}
	n, err := revokeTokens(ctx, tx, a.now(), `device_id = ? AND user_id = ?`, id, userID.String)
	if err != nil {
		return target, nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return target, nil, "", err
	}
	return target, map[string]any{"user_id": userID.String, "sessions_revoked": n}, "设备已解绑并下线", nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// revokeTokens 作废匹配条件的未作废 refresh token，返回数量。
// 与 app 包的 revokeFamily / revokeDevice 写的是同一张表、同一个字段。
func revokeTokens(ctx context.Context, db execer, now time.Time, where string, args ...any) (int64, error) {
	res, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at = ? WHERE revoked_at IS NULL AND `+where, append([]any{now.UnixMilli()}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 审计 ----------

type AuditRow struct {
	CreatedAt                         int64
	Action, Target, Detail, Actor, IP string
}

type AuditPage struct {
	Target string
	Rows   []AuditRow
}

func (a *Admin) auditRows(ctx context.Context, target string, limit int) ([]AuditRow, error) {
	query := `SELECT created_at, action, target, detail, actor, COALESCE(ip, '') FROM admin_audit`
	var args []any
	if target != "" {
		query += ` WHERE target = ?`
		args = append(args, target)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.CreatedAt, &r.Action, &r.Target, &r.Detail, &r.Actor, &r.IP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
