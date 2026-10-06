package credits

// 套餐与积分规则。数值是初始值，等埋点数据出来后再调整。

type PlanID string

const (
	PlanTrial   PlanID = "trial"
	PlanFree    PlanID = "free"
	PlanPro     PlanID = "pro"
	PlanProPlus PlanID = "pro_plus"
)

type Plan struct {
	ID    PlanID
	Label string
	// 每天最多成功请求次数（合理使用上限，订阅用户也有）
	DailyCap int
	// 免费用户每天补充的积分（当天有效）
	DailyRefill int
	// 订阅用户每个计费周期发放的积分
	MonthlyCredits int
	PriceUSD       float64
}

var Plans = map[PlanID]Plan{
	PlanTrial:   {ID: PlanTrial, Label: "体验", DailyCap: 30},
	PlanFree:    {ID: PlanFree, Label: "免费", DailyCap: 50, DailyRefill: 5},
	PlanPro:     {ID: PlanPro, Label: "Pro", DailyCap: 100, MonthlyCredits: 1000, PriceUSD: 9.99},
	PlanProPlus: {ID: PlanProPlus, Label: "Pro+", DailyCap: 300, MonthlyCredits: 3000, PriceUSD: 19.99},
}

// PaidPlans 按展示顺序
var PaidPlans = []PlanID{PlanPro, PlanProPlus}

func IsPaidPlan(p string) bool { return p == string(PlanPro) || p == string(PlanProPlus) }

const (
	// 每次成功生成回复消耗的积分（以后可以按模型加权）
	ReplyCost = 1
	// 匿名设备的一次性体验额度
	TrialCredits = 10
	// 同一 IP 每天最多给多少台新设备发放体验额度
	TrialsPerIPPerDay = 3
	// 注册赠送：每台设备、每个账号各只能领一次
	SignupBonus     = 50
	SignupBonusDays = 90
	// 请求频率限制（所有套餐相同）
	RatePerMinute     = 6
	RateMaxConcurrent = 2
	// 每个账号最多同时登录的设备数，超出时踢掉最早的
	MaxDevicesPerUser = 5
)

// 订阅到期后的宽限期：续费扣款失败（past_due）时给用户几天时间处理
const SubscriptionGraceMs = 3 * 24 * 60 * 60 * 1000

// ResolvePlan 根据订阅记录判断已登录用户当前的套餐；没有订阅（plan 为空）时是免费套餐。
// periodEnd 为 0 表示未知（不按到期时间判断）。接口和管理后台共用这条规则。
func ResolvePlan(plan, status string, periodEndMs, nowMs int64) PlanID {
	if !IsPaidPlan(plan) || (status != "active" && status != "past_due") {
		return PlanFree
	}
	if periodEndMs != 0 && periodEndMs+SubscriptionGraceMs < nowMs {
		return PlanFree
	}
	return PlanID(plan)
}

// Owner 积分账户的归属：登录用户按账号，未登录按设备
func UserOwner(userID string) string     { return "u:" + userID }
func DeviceOwner(deviceID string) string { return "d:" + deviceID }
