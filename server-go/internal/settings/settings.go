// Package settings 运行时配置（TECH_DESIGN §5.8 阶段 3）。
//
// 默认值来自环境变量和代码里的套餐表；管理后台修改的值存在 settings 表里覆盖默认值，
// 保存后立即在本进程生效（服务单实例运行，不需要跨进程通知）。
package settings

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/config"
	"github.com/nextreply/server/internal/credits"
)

type Kind int

const (
	KindInt Kind = iota
	KindFloat
	KindBool
	KindModel
	KindEffort
	KindChoice
)

type Field struct {
	Key     string
	Group   string
	Label   string
	Help    string
	Kind    Kind
	Min     float64
	Max     float64
	Choices []string // KindChoice 的可选值
	def     func(*config.Config) string
}

var Efforts = []string{"none", "low", "medium", "high", "xhigh", "max"}

// 成本上限触发后的处理方式
const (
	CapDowngrade = "downgrade"  // 免费用户改用 cost_cap_model，付费用户不受影响
	CapPauseFree = "pause_free" // 暂停免费用户（trial / free），付费用户不受影响
)

var plans = []credits.PlanID{credits.PlanTrial, credits.PlanFree, credits.PlanPro, credits.PlanProPlus}

func itoa(n int) func(*config.Config) string {
	return func(*config.Config) string { return strconv.Itoa(n) }
}

// Fields 可以在后台修改的全部配置项，按页面上的显示顺序。
var Fields = func() []Field {
	var fs []Field
	for _, p := range plans {
		plan := string(p)
		label := credits.Plans[p].Label
		fs = append(fs,
			Field{Key: "model." + plan, Group: "模型", Label: label + " · 模型", Kind: KindModel,
				def: func(c *config.Config) string { return c.ModelFor(plan).Model }},
			Field{Key: "effort." + plan, Group: "模型", Label: label + " · effort", Kind: KindEffort, Help: "none 表示不传；Haiku 自动不传",
				def: func(c *config.Config) string { return c.ModelFor(plan).Effort }},
		)
	}
	for _, p := range plans {
		fs = append(fs, Field{Key: "daily_cap." + string(p), Group: "套餐", Label: credits.Plans[p].Label + " · 每日上限", Kind: KindInt, Min: 1, Max: 10000,
			Help: "每个账户每天最多成功回复次数（UTC 日）", def: itoa(credits.Plans[p].DailyCap)})
	}
	fs = append(fs,
		Field{Key: "daily_refill.free", Group: "套餐", Label: "免费 · 每日补充", Kind: KindInt, Min: 0, Max: 1000,
			Help: "已登录免费用户每天补充的积分，当天有效", def: itoa(credits.Plans[credits.PlanFree].DailyRefill)},
		Field{Key: "trial_credits", Group: "套餐", Label: "匿名体验额度", Kind: KindInt, Min: 0, Max: 1000,
			Help: "新设备首次注册时发放；只影响之后注册的设备", def: itoa(credits.TrialCredits)},

		Field{Key: "trial_enabled", Group: "防刷", Label: "发放体验额度", Kind: KindBool,
			Help: "关闭后新设备不再获得体验额度，需要登录才能使用", def: func(*config.Config) string { return "true" }},
		Field{Key: "trials_per_ip", Group: "防刷", Label: "同一 IP 每天发放体验额度的设备数", Kind: KindInt, Min: 0, Max: 1000, def: itoa(credits.TrialsPerIPPerDay)},
		Field{Key: "register_per_ip", Group: "防刷", Label: "同一 IP 每天注册的新设备数", Kind: KindInt, Min: 1, Max: 100000,
			def: func(c *config.Config) string { return strconv.Itoa(c.RegisterPerIP) }},
		Field{Key: "ip_daily_quota", Group: "防刷", Label: "同一 IP 每天成功回复次数", Kind: KindInt, Min: 1, Max: 1000000,
			def: func(c *config.Config) string { return strconv.Itoa(c.IPDailyQuota) }},

		Field{Key: "cost_cap_usd", Group: "成本", Label: "每日成本上限（美元）", Kind: KindFloat, Min: 0, Max: 100000,
			Help: "今天（UTC）估算成本达到后触发下面的处理；0 表示不限制", def: func(*config.Config) string { return "0" }},
		Field{Key: "cost_cap_action", Group: "成本", Label: "达到上限后", Kind: KindChoice, Choices: []string{CapDowngrade, CapPauseFree},
			Help: "downgrade：免费用户改用下面的模型；pause_free：暂停免费用户。付费用户都不受影响", def: func(*config.Config) string { return CapDowngrade }},
		Field{Key: "cost_cap_model", Group: "成本", Label: "降级使用的模型", Kind: KindModel, def: func(*config.Config) string { return "claude-haiku-4-5" }},
	)
	return fs
}()

func fieldByKey(key string) (Field, bool) {
	i := slices.IndexFunc(Fields, func(f Field) bool { return f.Key == key })
	if i < 0 {
		return Field{}, false
	}
	return Fields[i], true
}

// ValidationError 配置值不合法；后台把它原样显示给操作者
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Validate 检查一个值是否合法，返回规范化后的值。
func (f Field) Validate(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch f.Kind {
	case KindInt:
		n, err := strconv.Atoi(v)
		if err != nil || float64(n) < f.Min || float64(n) > f.Max {
			return "", invalid("%s：必须是 %g–%g 的整数", f.Label, f.Min, f.Max)
		}
		return strconv.Itoa(n), nil
	case KindFloat:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < f.Min || n > f.Max {
			return "", invalid("%s：必须是 %g–%g 的数字", f.Label, f.Min, f.Max)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case KindBool:
		if v != "true" && v != "false" {
			return "", invalid("%s：必须是 true 或 false", f.Label)
		}
		return v, nil
	case KindModel:
		if !slices.Contains(ai.KnownModels(), v) {
			return "", invalid("%s：未知的模型 %q", f.Label, v)
		}
		return v, nil
	case KindEffort:
		if !slices.Contains(Efforts, v) {
			return "", invalid("%s：effort 必须是 %s 之一", f.Label, strings.Join(Efforts, " / "))
		}
		return v, nil
	case KindChoice:
		if !slices.Contains(f.Choices, v) {
			return "", invalid("%s：必须是 %s 之一", f.Label, strings.Join(f.Choices, " / "))
		}
		return v, nil
	}
	return "", invalid("%s：未知类型", f.Label)
}

// 今天成本的缓存时间：成本上限按这个粒度生效，期间新增的调用也会实时累加
const costCacheTTL = 30 * time.Second

type Store struct {
	db  *sql.DB
	cfg *config.Config
	now func() time.Time

	mu        sync.RWMutex
	overrides map[string]string

	costMu     sync.Mutex
	costDay    string
	costMicros int64
	costAt     time.Time
}

func New(db *sql.DB, cfg *config.Config) *Store {
	return &Store{db: db, cfg: cfg, now: time.Now, overrides: map[string]string{}}
}

// Load 从数据库读取覆盖值。数据库里的值不合法（例如价目表里删掉了某个模型）时忽略并使用默认值。
func (s *Store) Load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if f, ok := fieldByKey(k); ok {
			if nv, err := f.Validate(v); err == nil {
				m[k] = nv
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.overrides = m
	s.mu.Unlock()
	return nil
}

// Default 环境变量 / 代码里的默认值
func (s *Store) Default(key string) string {
	f, ok := fieldByKey(key)
	if !ok {
		return ""
	}
	return f.def(s.cfg)
}

// Get 当前生效的值
func (s *Store) Get(key string) string {
	s.mu.RLock()
	v, ok := s.overrides[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	return s.Default(key)
}

// Overridden 是否被后台修改过（有覆盖值）
func (s *Store) Overridden(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.overrides[key]
	return ok
}

func (s *Store) Int(key string) int {
	n, _ := strconv.Atoi(s.Get(key))
	return n
}

// Change 一项修改；Value 为空表示删除覆盖值、恢复默认。
type Change struct {
	Key, Value string
}

// Apply 校验并保存一组修改，全部合法才写入；成功后立即生效。返回实际发生变化的项（旧值 → 新值）。
func (s *Store) Apply(ctx context.Context, changes []Change, actor string) (map[string][2]string, error) {
	type op struct {
		key, value string // value 为空：删除
	}
	var ops []op
	diff := map[string][2]string{}
	for _, c := range changes {
		f, ok := fieldByKey(c.Key)
		if !ok {
			return nil, invalid("未知配置项 %q", c.Key)
		}
		before := s.Get(c.Key)
		if strings.TrimSpace(c.Value) == "" {
			if s.Overridden(c.Key) {
				ops = append(ops, op{key: c.Key})
				diff[c.Key] = [2]string{before, s.Default(c.Key) + "（默认）"}
			}
			continue
		}
		v, err := f.Validate(c.Value)
		if err != nil {
			return nil, err
		}
		// 值没变就不写；没有覆盖时填的正好是默认值，也不需要写入覆盖
		if v == before {
			continue
		}
		ops = append(ops, op{key: c.Key, value: v})
		diff[c.Key] = [2]string{before, v}
	}
	if len(ops) == 0 {
		return diff, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	for _, o := range ops {
		if o.value == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, o.key)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
				o.key, o.value, now, actor)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return diff, s.Load(ctx)
}

// ---------- 业务读取 ----------

// Plan 套餐数值（每日上限、每日补充按后台配置）
func (s *Store) Plan(id credits.PlanID) credits.Plan {
	p := credits.Plans[id]
	if _, ok := fieldByKey("daily_cap." + string(id)); ok {
		p.DailyCap = s.Int("daily_cap." + string(id))
	}
	if id == credits.PlanFree {
		p.DailyRefill = s.Int("daily_refill.free")
	}
	return p
}

func (s *Store) IPDailyQuota() int  { return s.Int("ip_daily_quota") }
func (s *Store) RegisterPerIP() int { return s.Int("register_per_ip") }
func (s *Store) TrialsPerIP() int   { return s.Int("trials_per_ip") }
func (s *Store) TrialCredits() int  { return s.Int("trial_credits") }
func (s *Store) TrialEnabled() bool { return s.Get("trial_enabled") == "true" }

// IsFreeTier 免费用户：匿名体验 + 已登录未订阅。成本上限只影响这两类。
func IsFreeTier(plan credits.PlanID) bool {
	return plan == credits.PlanTrial || plan == credits.PlanFree
}

// CostCap 成本上限的当前状态
type CostCap struct {
	CapMicros   int64 // 0 表示不限制
	TodayMicros int64
	Action      string
	Model       string
	Hit         bool
}

func (s *Store) CostCap(ctx context.Context) (CostCap, error) {
	usd, _ := strconv.ParseFloat(s.Get("cost_cap_usd"), 64)
	// 四舍五入：直接截断会让 0.000001 这类值变成 0（等于关闭上限）
	c := CostCap{CapMicros: int64(math.Round(usd * 1e6)), Action: s.Get("cost_cap_action"), Model: s.Get("cost_cap_model")}
	today, err := s.TodayCost(ctx)
	if err != nil {
		return c, err
	}
	c.TodayMicros = today
	c.Hit = c.CapMicros > 0 && today >= c.CapMicros
	return c, nil
}

// TodayCost 今天（UTC）的估算成本，缓存 costCacheTTL，期间用 AddCost 累加新调用。
func (s *Store) TodayCost(ctx context.Context) (int64, error) {
	now := s.now()
	day := credits.UTCDay(now.UnixMilli())
	s.costMu.Lock()
	defer s.costMu.Unlock()
	if s.costDay == day && now.Sub(s.costAt) < costCacheTTL {
		return s.costMicros, nil
	}
	start := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros), 0) FROM usage_events WHERE created_at >= ?`, start).Scan(&total); err != nil {
		return 0, err
	}
	s.costDay, s.costMicros, s.costAt = day, total, now
	return total, nil
}

// AddCost 记录一次调用的成本，让上限在缓存刷新前也能及时生效。
func (s *Store) AddCost(micros int64) {
	s.costMu.Lock()
	defer s.costMu.Unlock()
	if s.costDay == credits.UTCDay(s.now().UnixMilli()) {
		s.costMicros += micros
	}
}

// ModelFor 本次请求使用的模型。成本上限触发且处理方式为降级时，免费用户改用降级模型。
func (s *Store) ModelFor(plan credits.PlanID, cap CostCap) config.ModelChoice {
	if cap.Hit && cap.Action == CapDowngrade && IsFreeTier(plan) {
		return config.ModelChoice{Model: cap.Model, Effort: "low"}
	}
	return config.ModelChoice{Model: s.Get("model." + string(plan)), Effort: s.Get("effort." + string(plan))}
}
