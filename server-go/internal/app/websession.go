package app

// 网页账户的登录会话（TECH_DESIGN §10.3）：cookie 里是随机令牌，数据库只存哈希。
// HTTPS 下用 __Host- 前缀（必须 Secure、Path=/、不能带 Domain，主品牌其他子域名无法覆盖或读取）；
// 测试机走 HTTP 时退回普通 cookie。

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nextreply/server/internal/authn"
)

const webSessionTTL = 30 * 24 * time.Hour

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.cfg.PublicURL, "https://") }

func (s *Server) sessionCookieName() string {
	if s.secureCookies() {
		return "__Host-nr_session"
	}
	return "nr_session"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: s.sessionCookieName(), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) createWebSession(ctx context.Context, w http.ResponseWriter, userID string) error {
	token := authn.RandomToken()
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		authn.SHA256Hex(token), userID, now.UnixMilli(), now.Add(webSessionTTL).UnixMilli()); err != nil {
		return err
	}
	s.setSessionCookie(w, token, int(webSessionTTL.Seconds()))
	return nil
}

type webSession struct {
	UserID    string
	Email     string
	TokenHash string
}

// webUser 当前浏览器登录的用户；没有登录、会话过期或账号已停用时返回 nil。
func (s *Server) webUser(r *http.Request) (*webSession, error) {
	c, err := r.Cookie(s.sessionCookieName())
	if err != nil || c.Value == "" {
		return nil, nil
	}
	ws := &webSession{TokenHash: authn.SHA256Hex(c.Value)}
	var status string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT u.id, u.email, u.status FROM web_sessions ws JOIN users u ON u.id = ws.user_id
		 WHERE ws.token_hash = ? AND ws.expires_at > ?`, ws.TokenHash, s.nowMs()).Scan(&ws.UserID, &ws.Email, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, nil
	}
	return ws, nil
}

func (s *Server) endWebSession(ctx context.Context, w http.ResponseWriter, ws *webSession) error {
	s.setSessionCookie(w, "", -1)
	if ws == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, ws.TokenHash)
	return err
}

// csrfToken 表单里的 CSRF 令牌，由会话派生：只有能读到账户页的人才拿得到
func (s *Server) csrfToken(ws *webSession) string {
	return authn.SHA256Hex(s.cfg.TokenSecret + ":csrf:" + ws.TokenHash)[:32]
}

// sameOrigin 修改操作只接受来自本站页面的提交（SameSite=Lax 之外的第二道防线；登录表单没有会话，只能靠它防登录 CSRF）
func sameOrigin(r *http.Request) bool {
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

func (s *Server) validPost(r *http.Request, ws *webSession) bool {
	if !sameOrigin(r) {
		return false
	}
	if ws == nil {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.csrfToken(ws))) == 1
}
