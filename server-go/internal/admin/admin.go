// Package admin 管理后台（TECH_DESIGN §5.8）：单独的监听地址，只对本机开放，HTTP Basic 校验 ADMIN_TOKEN。
// 阶段 1 只读页面；阶段 2 管理操作（actions.go），带 CSRF 校验和操作审计。
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
)

//go:embed templates/*.html
var templateFS embed.FS

type Admin struct {
	db      *sql.DB
	cfg     *config.Config
	credits *credits.Service
	// 每次启动随机生成的 CSRF 令牌（见 checkCSRF）；重启后旧页面上的表单需要刷新
	csrf     string
	ipLimits map[string]int
	loc      *time.Location
	now      func() time.Time
	pages    map[string]*template.Template
}

func New(cfg *config.Config, db *sql.DB, svc *credits.Service, ipLimits map[string]int) (*Admin, error) {
	loc, err := time.LoadLocation(cfg.AdminTZ)
	if err != nil {
		return nil, fmt.Errorf("ADMIN_TZ: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	a := &Admin{db: db, cfg: cfg, credits: svc, csrf: hex.EncodeToString(buf), ipLimits: ipLimits, loc: loc, now: time.Now, pages: map[string]*template.Template{}}
	funcs := template.FuncMap{
		"time":      a.fmtTime,
		"usd":       fmtUSD,
		"pct":       fmtPct,
		"short":     short,
		"num":       fmtNum,
		"query":     withQuery,
		"plan":      planLabel,
		"status":    statusClass,
		"int64":     func(v int) int64 { return int64(v) },
		"csrf":      func() string { return a.csrf },
		"action":    actionLabel,
		"hasPrefix": strings.HasPrefix,
		"itoa":      strconv.Itoa,
		"div": func(a int64, b int) int64 {
			if b == 0 {
				return 0
			}
			return a / int64(b)
		},
	}
	for _, name := range []string{"overview", "accounts", "account", "limits", "audit"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		a.pages[name] = t
	}
	return a, nil
}

func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.page("overview", func(r *http.Request) (any, error) { return a.overview(r.Context()) }))
	mux.HandleFunc("GET /accounts", a.page("accounts", a.accountsData))
	mux.HandleFunc("GET /accounts/{kind}/{id}", a.page("account", func(r *http.Request) (any, error) {
		return a.accountDetail(r.Context(), r.PathValue("kind"), r.PathValue("id"))
	}))
	mux.HandleFunc("GET /limits", a.page("limits", func(r *http.Request) (any, error) { return a.limits(r.Context()) }))
	mux.HandleFunc("GET /audit", a.page("audit", func(r *http.Request) (any, error) {
		target := r.URL.Query().Get("target")
		rows, err := a.auditRows(r.Context(), target, 200)
		return AuditPage{Target: target, Rows: rows}, err
	}))

	mux.HandleFunc("POST /accounts/{kind}/{id}/grant", a.action("grant_credits", a.grantCredits))
	mux.HandleFunc("POST /accounts/u/{id}/suspend", a.action("suspend_user", a.setUserStatus("suspended")))
	mux.HandleFunc("POST /accounts/u/{id}/restore", a.action("restore_user", a.setUserStatus("active")))
	mux.HandleFunc("POST /accounts/u/{id}/revoke-sessions", a.action("revoke_sessions", a.revokeUserSessions))
	mux.HandleFunc("POST /devices/{id}/revoke-sessions", a.action("revoke_sessions", a.revokeDeviceSessions))
	mux.HandleFunc("POST /devices/{id}/unbind", a.action("unbind_device", a.unbindDevice))
	return a.auth(mux)
}

// auth HTTP Basic，用户名 admin，密码 ADMIN_TOKEN。比较前先哈希，避免按长度泄露信息。
func (a *Admin) auth(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(a.cfg.AdminToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		// 不能用 no-referrer：那样浏览器提交表单时会发送 Origin: null，同源校验会拒绝正常的操作
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		user, pass, ok := r.BasicAuth()
		got := sha256.Sum256([]byte(pass))
		if !ok || user != "admin" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			h.Set("WWW-Authenticate", `Basic realm="NextReply admin", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type pageData struct {
	Title string
	Nav   string
	Now   string
	TZ    string
	Data  any
	Query url.Values
	// 操作结果提示（POST 重定向回来时带在 ?msg= 上）
	Msg  string
	Path string
}

func (a *Admin) page(name string, load func(*http.Request) (any, error)) http.HandlerFunc {
	titles := map[string]string{"overview": "概览", "accounts": "账户", "account": "账户详情", "limits": "限额与风控", "audit": "操作审计"}
	nav := map[string]string{"account": "accounts"}
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := load(r)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("admin page failed", "page", name, "err", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		n := nav[name]
		if n == "" {
			n = name
		}
		pd := pageData{Title: titles[name], Nav: n, Now: a.fmtTime(a.now().UnixMilli()), TZ: a.loc.String(), Data: data,
			Query: r.URL.Query(), Msg: r.URL.Query().Get("msg"), Path: r.URL.Path}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := a.pages[name].Execute(w, pd); err != nil {
			slog.Error("admin render failed", "page", name, "err", err.Error())
		}
	}
}

type accountsPage struct {
	Filter AccountFilter
	Rows   []AccountRow
	More   bool
	Prev   int
	Next   int
}

func (a *Admin) accountsData(r *http.Request) (any, error) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := AccountFilter{Kind: q.Get("kind"), Query: q.Get("q"), Sort: q.Get("sort"), Offset: max(0, offset)}
	rows, more, err := a.accounts(r.Context(), f)
	if err != nil {
		return nil, err
	}
	return accountsPage{Filter: f, Rows: rows, More: more, Prev: max(0, f.Offset-pageSize), Next: f.Offset + pageSize}, nil
}

// ---------- 模板函数 ----------

func (a *Admin) fmtTime(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).In(a.loc).Format("2006-01-02 15:04")
}

func fmtUSD(micros int64) string {
	switch {
	case micros == 0:
		return "$0"
	case micros < 10_000: // 不到 1 美分时多保留几位
		return fmt.Sprintf("$%.4f", float64(micros)/1e6)
	default:
		return fmt.Sprintf("$%.2f", float64(micros)/1e6)
	}
}

func fmtPct(part, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(total))
}

func fmtNum(v int64) string {
	s := strconv.FormatInt(v, 10)
	if v < 0 || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:8] + "…"
}

// withQuery 在当前查询参数上改一个值，用于筛选 / 排序 / 翻页链接
func withQuery(q url.Values, kv ...string) string {
	n := url.Values{}
	for k, v := range q {
		n[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			n.Del(kv[i])
		} else {
			n.Set(kv[i], kv[i+1])
		}
	}
	if kv[0] != "offset" {
		n.Del("offset") // 改筛选条件时回到第一页
	}
	if len(n) == 0 {
		return "?"
	}
	return "?" + n.Encode()
}

func planLabel(p any) string {
	labels := map[string]string{"trial": "体验", "free": "免费", "pro": "Pro", "pro_plus": "Pro+"}
	s := fmt.Sprint(p)
	if l, ok := labels[s]; ok {
		return l
	}
	return s
}

func actionLabel(action string) string {
	labels := map[string]string{
		"grant_credits": "补发积分", "suspend_user": "停用账号", "restore_user": "恢复账号",
		"revoke_sessions": "强制下线", "unbind_device": "解绑设备",
	}
	if l, ok := labels[action]; ok {
		return l
	}
	return action
}

func statusClass(status string) string {
	switch {
	case status == "ok":
		return "ok"
	case strings.HasPrefix(status, "rejected:"):
		return "warn"
	case strings.HasPrefix(status, "error:"):
		return "bad"
	}
	return "muted"
}
