// Package app 是 HTTP 层：路由、鉴权、账号、计费、/v1/reply。接口与 TECH_DESIGN §5.1 一致。
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
)

type Server struct {
	cfg     *config.Config
	db      *sql.DB
	credits *credits.Service
	ai      ai.Generator
	http    *http.Client
	now     func() time.Time
}

func New(cfg *config.Config, db *sql.DB, gen ai.Generator) *Server {
	return &Server{
		cfg:     cfg,
		db:      db,
		credits: credits.NewService(db),
		ai:      gen,
		http:    &http.Client{Timeout: 15 * time.Second},
		now:     time.Now,
	}
}

func (s *Server) Credits() *credits.Service { return s.credits }

// 请求体上限。截图 base64 后约 6MB，对应 ~4.5MB 原图；客户端缩图后通常只有几百 KB
const (
	maxReplyBody   = 7 << 20
	maxJSONBody    = 64 << 10
	maxWebhookBody = 1 << 20
)

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, limit int64, fn handlerFunc) {
		mux.Handle(pattern, s.wrap(limit, fn))
	}

	h("GET /v1/health", 0, s.health)
	h("POST /v1/reply", maxReplyBody, s.reply)

	h("POST /v1/device/register", maxJSONBody, s.deviceRegister)
	h("GET /auth/login", 0, s.loginPage)
	h("POST /auth/email/start", maxJSONBody, s.emailStart)
	h("POST /auth/email/verify", maxJSONBody, s.emailVerify)
	h("POST /v1/auth/token", maxJSONBody, s.authToken)
	h("POST /v1/auth/refresh", maxJSONBody, s.authRefresh)
	h("POST /v1/auth/logout", maxJSONBody, s.authLogout)
	h("GET /v1/me", 0, s.me)

	h("POST /v1/billing/link", maxJSONBody, s.billingLink)
	h("GET /billing/checkout", 0, s.billingCheckout)
	h("POST /billing/dev/complete", maxJSONBody, s.devComplete)
	h("GET /billing/portal", 0, s.billingPortal)
	h("POST /billing/dev/cancel", maxJSONBody, s.devCancel)
	h("POST /billing/dev/expire", maxJSONBody, s.devExpire)
	h("GET /billing/done", 0, s.billingDone)
	h("POST /billing/webhook", maxWebhookBody, s.stripeWebhook)

	h("/", 0, func(w http.ResponseWriter, r *http.Request) error {
		return apierr.New(apierr.NotFound, "Not found.")
	})
	return mux
}

// wrap 统一处理：请求体大小限制、错误转 JSON、panic 兜底。
func (s *Server) wrap(limit int64, fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limit > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "path", r.URL.Path, "panic", v)
				writeError(w, apierr.New(apierr.Internal, "Internal server error."))
			}
		}()
		if err := fn(w, r); err != nil {
			var ae *apierr.Error
			if !errors.As(err, &ae) {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					ae = apierr.New(apierr.BadRequest, "Request body is too large.")
				} else {
					slog.Error("unhandled error", "path", r.URL.Path, "err", err.Error())
					ae = apierr.New(apierr.Internal, "Internal server error.")
				}
			}
			writeError(w, ae)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *apierr.Error) {
	_ = writeJSON(w, e.Status(), e.Body())
}

func writeHTML(w http.ResponseWriter, status int, body string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, err := io.WriteString(w, body)
	return err
}

// readJSON 读取 JSON 对象请求体。
func readJSON(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, err
		}
		return nil, apierr.New(apierr.BadRequest, "Request body must be a JSON object.")
	}
	return body, nil
}

func str(body map[string]any, key string) string {
	v, _ := body[key].(string)
	return strings.TrimSpace(v)
}

func optionalString(body map[string]any, key string, max int) string {
	v := str(body, key)
	if len(v) > max {
		v = truncate(v, max)
	}
	return v
}

// truncate 按字节截断但不切断 UTF-8 字符
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8Start(s[max]) {
		max--
	}
	return s[:max]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// clientIP 默认用 TCP 对端地址；部署在反向代理后面时从配置的请求头读取。
func (s *Server) clientIP(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		if v := strings.TrimSpace(strings.Split(r.Header.Get(h), ",")[0]); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) nowMs() int64 { return s.now().UnixMilli() }

func (s *Server) health(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		return writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": s.cfg.Model})
}
