package app

// GET /v1/plans：公开的套餐信息，网站价格页和以后的客户端都从这里读（TECH_DESIGN §10.2）。
// 数值走运行配置，后台改了每日上限等，网站随之更新，不会和实际规则不一致。

import (
	"net/http"

	"github.com/nextreply/server/internal/credits"
)

func (s *Server) plans(w http.ResponseWriter, r *http.Request) error {
	list := []map[string]any{}
	for _, id := range []credits.PlanID{credits.PlanFree, credits.PlanPro, credits.PlanProPlus} {
		p := s.settings.Plan(id)
		list = append(list, map[string]any{
			"id": p.ID, "label": p.Label, "price_usd": p.PriceUSD,
			"monthly_credits": p.MonthlyCredits, "daily_cap": p.DailyCap, "daily_refill": p.DailyRefill,
		})
	}
	h := w.Header()
	// 只读的公开数据，不带凭据，允许任意来源读取（网站与 API 不同源）
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "public, max-age=300")
	return writeJSON(w, http.StatusOK, map[string]any{
		"currency":          "usd",
		"trial_credits":     s.settings.TrialCredits(),
		"signup_bonus":      credits.SignupBonus,
		"signup_bonus_days": credits.SignupBonusDays,
		"plans":             list,
	})
}
