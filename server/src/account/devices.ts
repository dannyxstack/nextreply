import { deviceOwner, grantTo } from "../credits/client";
import { TRIAL_CREDITS, TRIALS_PER_IP_PER_DAY } from "../credits/plans";
import type { Env } from "../env";
import { ipKeys, takeIfUnder } from "../quota";

/**
 * 确保设备已登记；新设备按规则发放体验额度。
 * 设备注册接口和"持有旧版设备 token、但数据库里还没有记录"的设备都走这里。
 */
export async function ensureDevice(env: Env, deviceId: string, hwHash: string | null, ip: string): Promise<{ created: boolean }> {
  const db = env.DB;
  const now = Date.now();
  const existing = await db.prepare("SELECT id FROM devices WHERE id = ?").bind(deviceId).first();
  if (existing) {
    await db.prepare("UPDATE devices SET last_seen = ? WHERE id = ?").bind(now, deviceId).run();
    return { created: false };
  }

  await db
    .prepare("INSERT OR IGNORE INTO devices (id, hw_hash, first_ip, created_at, last_seen) VALUES (?, ?, ?, ?, ?)")
    .bind(deviceId, hwHash, ip, now, now)
    .run();

  // 体验额度：同一台机器（硬件哈希相同）只发一次；同一 IP 每天最多发给 N 台新设备
  const sameMachine = hwHash
    ? await db.prepare("SELECT 1 FROM devices WHERE hw_hash = ? AND trial_granted = 1").bind(hwHash).first()
    : null;
  if (!sameMachine && (await takeIfUnder(env.QUOTA, ipKeys.trial(ip), TRIALS_PER_IP_PER_DAY))) {
    await grantTo(env, deviceOwner(deviceId), { bucket: "trial", amount: TRIAL_CREDITS, expiresAt: null, sourceRef: "trial", reason: "trial" });
    await db.prepare("UPDATE devices SET trial_granted = 1 WHERE id = ?").bind(deviceId).run();
  }
  return { created: true };
}
