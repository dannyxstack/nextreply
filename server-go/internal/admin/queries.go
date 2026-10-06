package admin

// 后台页面的数据查询。全部只读；数据只有元数据，没有截图或聊天内容。

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/nextreply/server/internal/credits"
)

// ---------- 概览 ----------

type WindowStats struct {
	Total, OK, Unusable, Rejected, Errors int
	// 各状态明细，如 ok、not_a_conversation、rejected:daily_cap、error:timeout
	Statuses       []NameCount
	P50, P95       int64
	CostMicros     int64
	ActiveAccounts int
	NewDevices     int
	NewUsers       int
	Models         []ModelUsage
	Plans          []PlanUsage
}

type NameCount struct {
	Name  string
	Count int
}

type ModelUsage struct {
	Model                                      string
	Requests                                   int
	TokensIn, TokensOut, CacheRead, CacheWrite int64
	CostMicros                                 int64
}

type PlanUsage struct {
	Plan       string
	Requests   int
	OK         int
	CostMicros int64
}

type DayStats struct {
	Day                            string
	Requests, OK, Rejected, Errors int
	CostMicros                     int64
	NewDevices, NewUsers           int
}

type Overview struct {
	Today, Week   WindowStats
	Days          []DayStats
	Subscriptions []NameCount
	PlanModels    []PlanModel
}

type PlanModel struct {
	Plan, Label, Model, Effort string
	DailyCap, DailyRefill      int
}

func (a *Admin) windowStats(ctx context.Context, since int64) (WindowStats, error) {
	var w WindowStats
	rows, err := a.db.QueryContext(ctx, `SELECT status, COUNT(*), COALESCE(SUM(cost_micros), 0) FROM usage_events WHERE created_at >= ? GROUP BY status ORDER BY COUNT(*) DESC`, since)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var nc NameCount
		var cost int64
		if err := rows.Scan(&nc.Name, &nc.Count, &cost); err != nil {
			rows.Close()
			return w, err
		}
		w.Statuses = append(w.Statuses, nc)
		w.Total += nc.Count
		w.CostMicros += cost
		switch {
		case nc.Name == "ok":
			w.OK += nc.Count
		case strings.HasPrefix(nc.Name, "rejected:"):
			w.Rejected += nc.Count
		case strings.HasPrefix(nc.Name, "error:"):
			w.Errors += nc.Count
		default:
			w.Unusable += nc.Count // insufficient_context / not_a_conversation
		}
	}
	rows.Close()

	// 耗时只看成功生成回复的请求；量不大，直接在内存里算分位数
	lats, err := queryInts(ctx, a.db, `SELECT latency_ms FROM usage_events WHERE created_at >= ? AND status = 'ok' ORDER BY latency_ms`, since)
	if err != nil {
		return w, err
	}
	w.P50, w.P95 = percentile(lats, 0.50), percentile(lats, 0.95)

	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT owner) FROM usage_events WHERE created_at >= ? AND status = 'ok'`, since).Scan(&w.ActiveAccounts); err != nil {
		return w, err
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE created_at >= ?`, since).Scan(&w.NewDevices); err != nil {
		return w, err
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE created_at >= ?`, since).Scan(&w.NewUsers); err != nil {
		return w, err
	}

	rows, err = a.db.QueryContext(ctx, `SELECT model, COUNT(*), SUM(tokens_in), SUM(tokens_out), SUM(cache_read), SUM(cache_write), SUM(cost_micros)
		FROM usage_events WHERE created_at >= ? AND model IS NOT NULL GROUP BY model ORDER BY SUM(cost_micros) DESC`, since)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Requests, &m.TokensIn, &m.TokensOut, &m.CacheRead, &m.CacheWrite, &m.CostMicros); err != nil {
			rows.Close()
			return w, err
		}
		w.Models = append(w.Models, m)
	}
	rows.Close()

	rows, err = a.db.QueryContext(ctx, `SELECT plan, COUNT(*), SUM(status = 'ok'), SUM(cost_micros) FROM usage_events WHERE created_at >= ? GROUP BY plan ORDER BY COUNT(*) DESC`, since)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PlanUsage
		if err := rows.Scan(&p.Plan, &p.Requests, &p.OK, &p.CostMicros); err != nil {
			return w, err
		}
		w.Plans = append(w.Plans, p)
	}
	return w, rows.Err()
}

func (a *Admin) overview(ctx context.Context) (*Overview, error) {
	now := a.now().In(a.loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.loc)
	o := &Overview{}
	var err error
	if o.Today, err = a.windowStats(ctx, midnight.UnixMilli()); err != nil {
		return nil, err
	}
	if o.Week, err = a.windowStats(ctx, now.Add(-7*24*time.Hour).UnixMilli()); err != nil {
		return nil, err
	}
	if o.Days, err = a.dailyTrend(ctx, midnight.AddDate(0, 0, -13)); err != nil {
		return nil, err
	}

	rows, err := a.db.QueryContext(ctx, `SELECT plan, status, COALESCE(current_period_end, 0) FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var plan, status string
		var end int64
		if err := rows.Scan(&plan, &status, &end); err != nil {
			rows.Close()
			return nil, err
		}
		if p := credits.ResolvePlan(plan, status, end, a.now().UnixMilli()); p != credits.PlanFree {
			counts[string(p)]++
		}
	}
	rows.Close()
	for _, id := range credits.PaidPlans {
		o.Subscriptions = append(o.Subscriptions, NameCount{Name: credits.Plans[id].Label, Count: counts[string(id)]})
	}

	for _, id := range []credits.PlanID{credits.PlanTrial, credits.PlanFree, credits.PlanPro, credits.PlanProPlus} {
		p := credits.Plans[id]
		m := a.cfg.ModelFor(string(id))
		o.PlanModels = append(o.PlanModels, PlanModel{Plan: string(id), Label: p.Label, Model: m.Model, Effort: m.Effort, DailyCap: p.DailyCap, DailyRefill: p.DailyRefill})
	}
	return o, nil
}

// dailyTrend 按后台时区的自然日汇总。偏移量取当前时刻的，夏令时切换当天可能有一小时误差，可以接受。
func (a *Admin) dailyTrend(ctx context.Context, from time.Time) ([]DayStats, error) {
	_, offset := a.now().In(a.loc).Zone()
	dayExpr := `date(created_at / 1000 + ?, 'unixepoch')`
	byDay := map[string]*DayStats{}
	var order []string
	for d := from; !d.After(a.now().In(a.loc)); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		byDay[k] = &DayStats{Day: k}
		order = append(order, k)
	}

	rows, err := a.db.QueryContext(ctx, `SELECT `+dayExpr+` AS day, COUNT(*), SUM(status = 'ok'), SUM(status LIKE 'rejected:%'), SUM(status LIKE 'error:%'), SUM(cost_micros)
		FROM usage_events WHERE created_at >= ? GROUP BY day`, offset, from.UnixMilli())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day string
		var d DayStats
		if err := rows.Scan(&day, &d.Requests, &d.OK, &d.Rejected, &d.Errors, &d.CostMicros); err != nil {
			rows.Close()
			return nil, err
		}
		if cur, ok := byDay[day]; ok {
			d.Day, d.NewDevices, d.NewUsers = day, cur.NewDevices, cur.NewUsers
			*cur = d
		}
	}
	rows.Close()

	for _, q := range []struct {
		table string
		set   func(*DayStats, int)
	}{
		{"devices", func(d *DayStats, n int) { d.NewDevices = n }},
		{"users", func(d *DayStats, n int) { d.NewUsers = n }},
	} {
		rows, err := a.db.QueryContext(ctx, `SELECT `+dayExpr+` AS day, COUNT(*) FROM `+q.table+` WHERE created_at >= ? GROUP BY day`, offset, from.UnixMilli())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var day string
			var n int
			if err := rows.Scan(&day, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if cur, ok := byDay[day]; ok {
				q.set(cur, n)
			}
		}
		rows.Close()
	}

	out := make([]DayStats, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- { // 最近的在前
		out = append(out, *byDay[order[i]])
	}
	return out, nil
}

// ---------- 账户列表 ----------

type AccountFilter struct {
	Kind   string // all | user | device
	Query  string
	Sort   string // active | usage | cost | created
	Offset int
}

type AccountRow struct {
	Kind       string // u | d
	ID         string
	Label      string // 邮箱或设备 ID
	Status     string
	Plan       credits.PlanID
	Devices    int    // 用户名下设备数
	FirstIP    string // 设备首次注册 IP
	Balance    int
	UsedToday  int
	OKTotal    int
	Requests   int
	CostMicros int64
	CreatedAt  int64
	LastActive int64
}

const pageSize = 50

func (a *Admin) accounts(ctx context.Context, f AccountFilter) ([]AccountRow, bool, error) {
	now := a.now().UnixMilli()
	// 注册用户 + 未绑定账号的匿名设备；用量、余额、今日用量按积分账户（owner）关联
	base := `
WITH acct AS (
  SELECT 'u' AS kind, u.id, u.email AS label, u.status, u.created_at,
         (SELECT MAX(last_seen) FROM devices d WHERE d.user_id = u.id) AS last_seen,
         (SELECT COUNT(*) FROM devices d WHERE d.user_id = u.id) AS devices,
         '' AS first_ip,
         s.plan AS sub_plan, s.status AS sub_status, COALESCE(s.current_period_end, 0) AS sub_end
  FROM users u LEFT JOIN subscriptions s ON s.user_id = u.id
  UNION ALL
  SELECT 'd', d.id, d.id, 'active', d.created_at, d.last_seen, 0, COALESCE(d.first_ip, ''), NULL, NULL, 0
  FROM devices d WHERE d.user_id IS NULL
),
usage AS (
  SELECT owner, COUNT(*) AS requests, SUM(status = 'ok') AS ok, SUM(cost_micros) AS cost, MAX(created_at) AS last_event
  FROM usage_events GROUP BY owner
),
bal AS (
  SELECT owner, SUM(remaining) AS balance FROM credit_grants
  WHERE remaining > 0 AND (expires_at IS NULL OR expires_at > ?) GROUP BY owner
),
today AS (
  SELECT owner, count FROM credit_usage_day WHERE day = ?
)
SELECT acct.kind, acct.id, acct.label, acct.status, acct.created_at, acct.devices, acct.first_ip,
       COALESCE(acct.sub_plan, ''), COALESCE(acct.sub_status, ''), acct.sub_end,
       COALESCE(usage.requests, 0), COALESCE(usage.ok, 0), COALESCE(usage.cost, 0),
       MAX(COALESCE(usage.last_event, 0), COALESCE(acct.last_seen, 0)) AS last_active,
       COALESCE(bal.balance, 0), COALESCE(today.count, 0)
FROM acct
LEFT JOIN usage ON usage.owner = acct.kind || ':' || acct.id
LEFT JOIN bal   ON bal.owner   = acct.kind || ':' || acct.id
LEFT JOIN today ON today.owner = acct.kind || ':' || acct.id
WHERE 1 = 1`
	args := []any{now, credits.UTCDay(now)}
	switch f.Kind {
	case "user":
		base += ` AND acct.kind = 'u'`
	case "device":
		base += ` AND acct.kind = 'd'`
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		base += ` AND (acct.label LIKE ? ESCAPE '\' OR acct.id LIKE ? ESCAPE '\' OR acct.first_ip = ?)`
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		args = append(args, like, like, q)
	}
	order := map[string]string{
		"usage":   "COALESCE(usage.ok, 0) DESC",
		"cost":    "COALESCE(usage.cost, 0) DESC",
		"created": "acct.created_at DESC",
	}[f.Sort]
	if order == "" {
		order = "last_active DESC"
	}
	base += ` ORDER BY ` + order + `, acct.id LIMIT ? OFFSET ?`
	args = append(args, pageSize+1, f.Offset)

	rows, err := a.db.QueryContext(ctx, base, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []AccountRow
	for rows.Next() {
		var r AccountRow
		var subPlan, subStatus string
		var subEnd int64
		if err := rows.Scan(&r.Kind, &r.ID, &r.Label, &r.Status, &r.CreatedAt, &r.Devices, &r.FirstIP,
			&subPlan, &subStatus, &subEnd, &r.Requests, &r.OKTotal, &r.CostMicros, &r.LastActive, &r.Balance, &r.UsedToday); err != nil {
			return nil, false, err
		}
		if r.Kind == "d" {
			r.Plan = credits.PlanTrial
		} else {
			r.Plan = credits.ResolvePlan(subPlan, subStatus, subEnd, now)
		}
		out = append(out, r)
	}
	more := len(out) > pageSize
	if more {
		out = out[:pageSize]
	}
	return out, more, rows.Err()
}

// ---------- 账户详情 ----------

type DeviceRow struct {
	ID, HWHash, FirstIP, UserID string
	CreatedAt, LastSeen         int64
	TrialGranted, BonusClaimed  bool
	ActiveSessions              int
}

type GrantRow struct {
	Bucket, SourceRef, Reason string
	Amount, Remaining         int
	ExpiresAt                 int64 // 0 表示不过期
	CreatedAt                 int64
	Expired                   bool
}

type LedgerRow struct {
	Delta     int
	Reason    string
	Ref       string
	CreatedAt int64
}

type EventRow struct {
	CreatedAt                      int64
	Status, Plan, Model, DeviceID  string
	IP                             string
	Credits                        int
	LatencyMs                      int64
	TokensIn, TokensOut, CacheRead int64
	CostMicros                     int64
}

type SubscriptionRow struct {
	Plan, Status, Provider, CustomerID string
	PeriodEnd                          int64
	CancelAtPeriodEnd                  bool
}

type AccountDetail struct {
	Kind, ID, Label, Status string
	Plan                    credits.PlanID
	CreatedAt               int64
	Balance                 int
	UsedToday               int
	Subscription            *SubscriptionRow
	Devices                 []DeviceRow
	Grants                  []GrantRow
	Ledger                  []LedgerRow
	Events                  []EventRow
	Audit                   []AuditRow
}

func (a *Admin) accountDetail(ctx context.Context, kind, id string) (*AccountDetail, error) {
	now := a.now().UnixMilli()
	d := &AccountDetail{Kind: kind, ID: id}
	owner := kind + ":" + id
	var devQuery string
	var devArgs []any
	var eventFilter string

	switch kind {
	case "u":
		err := a.db.QueryRowContext(ctx, `SELECT email, status, created_at FROM users WHERE id = ?`, id).Scan(&d.Label, &d.Status, &d.CreatedAt)
		if err != nil {
			return nil, err
		}
		var s SubscriptionRow
		var cust sql.NullString
		var end sql.NullInt64
		var cancel int
		err = a.db.QueryRowContext(ctx, `SELECT plan, status, provider, customer_id, current_period_end, cancel_at_period_end FROM subscriptions WHERE user_id = ?`, id).
			Scan(&s.Plan, &s.Status, &s.Provider, &cust, &end, &cancel)
		switch {
		case err == nil:
			s.CustomerID, s.PeriodEnd, s.CancelAtPeriodEnd = cust.String, end.Int64, cancel != 0
			d.Subscription = &s
			d.Plan = credits.ResolvePlan(s.Plan, s.Status, s.PeriodEnd, now)
		case err == sql.ErrNoRows:
			d.Plan = credits.PlanFree
		default:
			return nil, err
		}
		devQuery, devArgs = `WHERE d.user_id = ?`, []any{id}
		eventFilter = `owner = ?`
	case "d":
		var userID sql.NullString
		err := a.db.QueryRowContext(ctx, `SELECT created_at, user_id FROM devices WHERE id = ?`, id).Scan(&d.CreatedAt, &userID)
		if err != nil {
			return nil, err
		}
		d.Label, d.Status, d.Plan = id, "active", credits.PlanTrial
		devQuery, devArgs = `WHERE d.id = ?`, []any{id}
		// 设备详情看这台设备上的全部请求（包括登录后的），积分只看匿名体验账户
		eventFilter = `device_id = ?`
		owner = credits.DeviceOwner(id)
	default:
		return nil, sql.ErrNoRows
	}

	rows, err := a.db.QueryContext(ctx, `SELECT d.id, COALESCE(d.hw_hash, ''), COALESCE(d.first_ip, ''), COALESCE(d.user_id, ''), d.created_at, d.last_seen,
		d.trial_granted, d.signup_bonus_claimed,
		(SELECT COUNT(*) FROM refresh_tokens r WHERE r.device_id = d.id AND r.revoked_at IS NULL AND r.expires_at > ?)
		FROM devices d `+devQuery+` ORDER BY d.last_seen DESC`, append([]any{now}, devArgs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r DeviceRow
		if err := rows.Scan(&r.ID, &r.HWHash, &r.FirstIP, &r.UserID, &r.CreatedAt, &r.LastSeen, &r.TrialGranted, &r.BonusClaimed, &r.ActiveSessions); err != nil {
			rows.Close()
			return nil, err
		}
		d.Devices = append(d.Devices, r)
	}
	rows.Close()

	rows, err = a.db.QueryContext(ctx, `SELECT bucket, amount, remaining, COALESCE(expires_at, 0), source_ref, reason, created_at FROM credit_grants WHERE owner = ? ORDER BY created_at DESC LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g GrantRow
		if err := rows.Scan(&g.Bucket, &g.Amount, &g.Remaining, &g.ExpiresAt, &g.SourceRef, &g.Reason, &g.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		g.Expired = g.ExpiresAt != 0 && g.ExpiresAt <= now
		if !g.Expired {
			d.Balance += g.Remaining
		}
		d.Grants = append(d.Grants, g)
	}
	rows.Close()

	rows, err = a.db.QueryContext(ctx, `SELECT delta, reason, COALESCE(ref, ''), created_at FROM credit_ledger WHERE owner = ? ORDER BY id DESC LIMIT 50`, owner)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l LedgerRow
		if err := rows.Scan(&l.Delta, &l.Reason, &l.Ref, &l.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		d.Ledger = append(d.Ledger, l)
	}
	rows.Close()

	_ = a.db.QueryRowContext(ctx, `SELECT count FROM credit_usage_day WHERE owner = ? AND day = ?`, owner, credits.UTCDay(now)).Scan(&d.UsedToday)

	rows, err = a.db.QueryContext(ctx, `SELECT created_at, status, plan, COALESCE(model, ''), COALESCE(device_id, ''), COALESCE(ip, ''), credits,
		COALESCE(latency_ms, 0), tokens_in, tokens_out, cache_read, cost_micros
		FROM usage_events WHERE `+eventFilter+` ORDER BY created_at DESC LIMIT 50`, map[string]string{"u": owner, "d": id}[kind])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.CreatedAt, &e.Status, &e.Plan, &e.Model, &e.DeviceID, &e.IP, &e.Credits, &e.LatencyMs, &e.TokensIn, &e.TokensOut, &e.CacheRead, &e.CostMicros); err != nil {
			return nil, err
		}
		d.Events = append(d.Events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.Audit, err = a.auditRows(ctx, kind+":"+id, 20)
	return d, err
}

// ---------- 限额与风控 ----------

type IPCounterRow struct {
	Kind, IP     string
	Count, Limit int
	Exceeded     bool
}

type CapRow struct {
	Owner, Kind, ID string
	Plan            credits.PlanID
	Used, Cap       int
	Exceeded        bool
}

type RejectionRow struct {
	Reason, Owner, IP string
	Count             int
	Last              int64
}

type GroupRow struct {
	Key         string
	Count       int
	First, Last int64
}

type Limits struct {
	Day        string
	IPCounters []IPCounterRow
	DailyCaps  []CapRow
	Rejections []RejectionRow
	Errors     []NameCount
	IPDevices  []GroupRow
	HWDevices  []GroupRow
}

func (a *Admin) limits(ctx context.Context) (*Limits, error) {
	now := a.now().UnixMilli()
	day := credits.UTCDay(now)
	l := &Limits{Day: day}

	// ip_counters.key = <kind>:<ip>:<YYYY-MM-DD>；IPv6 地址本身带冒号，所以按首尾冒号切分
	rows, err := a.db.QueryContext(ctx, `SELECT key, count FROM ip_counters WHERE key LIKE ? ORDER BY count DESC LIMIT 500`, "%:"+day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			rows.Close()
			return nil, err
		}
		first, last := strings.Index(key, ":"), strings.LastIndex(key, ":")
		if first < 0 || last <= first {
			continue
		}
		r := IPCounterRow{Kind: key[:first], IP: key[first+1 : last], Count: n, Limit: a.ipLimits[key[:first]]}
		r.Exceeded = r.Limit > 0 && r.Count >= r.Limit
		l.IPCounters = append(l.IPCounters, r)
	}
	rows.Close()
	sort.SliceStable(l.IPCounters, func(i, j int) bool { return l.IPCounters[i].Exceeded && !l.IPCounters[j].Exceeded })

	// 今天用量接近或达到套餐每日上限的账户（显示达到上限 80% 以上的）
	minCap := credits.Plans[credits.PlanTrial].DailyCap
	for _, p := range credits.Plans {
		minCap = min(minCap, p.DailyCap)
	}
	rows, err = a.db.QueryContext(ctx, `SELECT c.owner, c.count, COALESCE(s.plan, ''), COALESCE(s.status, ''), COALESCE(s.current_period_end, 0)
		FROM credit_usage_day c LEFT JOIN subscriptions s ON c.owner = 'u:' || s.user_id
		WHERE c.day = ? AND c.count * 5 >= ? * 4 ORDER BY c.count DESC LIMIT 200`, day, minCap)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r CapRow
		var subPlan, subStatus string
		var subEnd int64
		if err := rows.Scan(&r.Owner, &r.Used, &subPlan, &subStatus, &subEnd); err != nil {
			rows.Close()
			return nil, err
		}
		r.Kind, r.ID, _ = strings.Cut(r.Owner, ":")
		if r.Kind == "d" {
			r.Plan = credits.PlanTrial
		} else {
			r.Plan = credits.ResolvePlan(subPlan, subStatus, subEnd, now)
		}
		r.Cap = credits.Plans[r.Plan].DailyCap
		if r.Used*5 < r.Cap*4 {
			continue
		}
		r.Exceeded = r.Used >= r.Cap
		l.DailyCaps = append(l.DailyCaps, r)
	}
	rows.Close()

	since := now - 24*time.Hour.Milliseconds()
	rows, err = a.db.QueryContext(ctx, `SELECT substr(status, 10), owner, COALESCE(ip, ''), COUNT(*), MAX(created_at)
		FROM usage_events WHERE status LIKE 'rejected:%' AND created_at >= ?
		GROUP BY status, owner, ip ORDER BY COUNT(*) DESC LIMIT 200`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r RejectionRow
		if err := rows.Scan(&r.Reason, &r.Owner, &r.IP, &r.Count, &r.Last); err != nil {
			rows.Close()
			return nil, err
		}
		l.Rejections = append(l.Rejections, r)
	}
	rows.Close()

	rows, err = a.db.QueryContext(ctx, `SELECT COALESCE(error_code, status), COUNT(*) FROM usage_events WHERE status LIKE 'error:%' AND created_at >= ? GROUP BY 1 ORDER BY 2 DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var nc NameCount
		if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
			rows.Close()
			return nil, err
		}
		l.Errors = append(l.Errors, nc)
	}
	rows.Close()

	if l.IPDevices, err = a.groups(ctx, `SELECT first_ip, COUNT(*), MIN(created_at), MAX(created_at) FROM devices
		WHERE first_ip IS NOT NULL GROUP BY first_ip HAVING COUNT(*) >= 3 ORDER BY COUNT(*) DESC LIMIT 50`); err != nil {
		return nil, err
	}
	if l.HWDevices, err = a.groups(ctx, `SELECT hw_hash, COUNT(*), MIN(created_at), MAX(created_at) FROM devices
		WHERE hw_hash IS NOT NULL GROUP BY hw_hash HAVING COUNT(*) >= 2 ORDER BY COUNT(*) DESC LIMIT 50`); err != nil {
		return nil, err
	}
	return l, nil
}

func (a *Admin) groups(ctx context.Context, query string) ([]GroupRow, error) {
	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupRow
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.Key, &g.Count, &g.First, &g.Last); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------- 工具 ----------

func queryInts(ctx context.Context, db *sql.DB, query string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// percentile 已排序的数据按最近秩取分位数
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p+0.5) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}
