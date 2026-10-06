import type { Env } from "../env";
import type { Bucket } from "./allocate";
import { PLANS, type PlanId } from "./plans";

/** 积分账户的归属：登录用户按账号，未登录按设备 */
export type Owner = `u:${string}` | `d:${string}`;

export const userOwner = (userId: string): Owner => `u:${userId}`;
export const deviceOwner = (deviceId: string): Owner => `d:${deviceId}`;

export function credits(env: Env, owner: Owner) {
  return env.CREDITS.get(env.CREDITS.idFromName(owner));
}

/** 免费用户每天补充；其他套餐没有每日补充 */
export const dailyRefillFor = (plan: PlanId) => PLANS[plan].dailyRefill;

export async function grantTo(
  env: Env,
  owner: Owner,
  g: { bucket: Bucket; amount: number; expiresAt: number | null; sourceRef: string; reason: string },
) {
  return credits(env, owner).grant(g);
}
