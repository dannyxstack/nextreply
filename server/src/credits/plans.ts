// 套餐与积分规则。数值是初始值，等埋点数据出来后再调整。

export type PlanId = "trial" | "free" | "pro" | "pro_plus";
export type PaidPlanId = "pro" | "pro_plus";

export interface Plan {
  id: PlanId;
  label: string;
  /** 每天最多成功请求次数（合理使用上限，订阅用户也有） */
  dailyCap: number;
  /** 免费用户每天补充的积分（当天有效） */
  dailyRefill?: number;
  /** 订阅用户每个计费周期发放的积分 */
  monthlyCredits?: number;
  priceUsd?: number;
}

export const PLANS: Record<PlanId, Plan> = {
  trial: { id: "trial", label: "体验", dailyCap: 30 },
  free: { id: "free", label: "免费", dailyCap: 50, dailyRefill: 5 },
  pro: { id: "pro", label: "Pro", dailyCap: 100, monthlyCredits: 1000, priceUsd: 9.99 },
  pro_plus: { id: "pro_plus", label: "Pro+", dailyCap: 300, monthlyCredits: 3000, priceUsd: 19.99 },
};

export const isPaidPlan = (p: string): p is PaidPlanId => p === "pro" || p === "pro_plus";

/** 每次成功生成回复消耗的积分（以后可以按模型加权） */
export const REPLY_COST = 1;

/** 匿名设备的一次性体验额度 */
export const TRIAL_CREDITS = 10;
/** 同一 IP 每天最多给多少台新设备发放体验额度 */
export const TRIALS_PER_IP_PER_DAY = 3;

/** 注册赠送：每台设备、每个账号各只能领一次 */
export const SIGNUP_BONUS = 50;
export const SIGNUP_BONUS_DAYS = 90;

/** 请求频率限制（所有套餐相同） */
export const RATE_LIMIT = { perMinute: 6, maxConcurrent: 2 };

/** 每个账号最多同时登录的设备数，超出时踢掉最早的 */
export const MAX_DEVICES_PER_USER = 5;
