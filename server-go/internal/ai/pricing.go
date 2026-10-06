package ai

import (
	"sort"
	"strings"
)

// 每百万 token 的美元价格，用于后台估算成本；实际账单以 Anthropic Console 为准。
// 缓存写入按 5 分钟 TTL（输入价的 1.25 倍）计。价格调整时更新这里。
type price struct{ input, output, cacheWrite, cacheRead float64 }

var prices = map[string]price{
	"claude-fable-5-1": {10, 50, 12.5, 0.25},
	"claude-opus-5-5":  {4, 20, 5, 0.20},
	"claude-opus-5":    {5, 25, 6.25, 0.50},
	"claude-sonnet-5":  {2, 10, 2.5, 0.20},
	"claude-haiku-4-5": {1, 5, 1.25, 0.10},
}

// CostMicros 估算一次调用的成本（百万分之一美元）。未知模型返回 0。
// 响应里的模型名可能带日期后缀，按前缀匹配最长的已知模型。
func CostMicros(model string, u Usage) int64 {
	var p price
	best := ""
	for name, pr := range prices {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best, p = name, pr
		}
	}
	if best == "" {
		return 0
	}
	// 价格是"美元 / 百万 token"，乘以 token 数正好得到百万分之一美元
	cost := float64(u.Input)*p.input + float64(u.Output)*p.output + float64(u.CacheWrite)*p.cacheWrite + float64(u.CacheRead)*p.cacheRead
	return int64(cost + 0.5)
}

// KnownModels 有价目的模型，管理后台只允许在这些模型之间切换（防止手误填错模型名导致线上全部失败）。
func KnownModels() []string {
	out := make([]string, 0, len(prices))
	for m := range prices {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
