// 基于 KV 的每日软额度。KV 是最终一致的，偶尔多放行一两次可以接受。

const TTL_SECONDS = 2 * 24 * 60 * 60;

export function today(now = new Date()): string {
  return now.toISOString().slice(0, 10);
}

async function read(kv: KVNamespace, key: string): Promise<number> {
  const v = await kv.get(key);
  return v ? Number.parseInt(v, 10) || 0 : 0;
}

async function bump(kv: KVNamespace, key: string): Promise<number> {
  const next = (await read(kv, key)) + 1;
  await kv.put(key, String(next), { expirationTtl: TTL_SECONDS });
  return next;
}

export const quotaKeys = {
  device: (deviceId: string, day = today()) => `q:${deviceId}:${day}`,
  ip: (ip: string, day = today()) => `ip:${ip}:${day}`,
  register: (ip: string, day = today()) => `reg:${ip}:${day}`,
};

export interface QuotaLimits {
  device: number;
  ip: number;
}

/** 返回今日设备剩余次数；任一维度已用完返回 0。 */
export async function remaining(kv: KVNamespace, deviceId: string, ip: string, limits: QuotaLimits): Promise<number> {
  const [usedDevice, usedIp] = await Promise.all([
    read(kv, quotaKeys.device(deviceId)),
    read(kv, quotaKeys.ip(ip)),
  ]);
  if (usedIp >= limits.ip) return 0;
  return Math.max(0, limits.device - usedDevice);
}

/** 成功生成回复后扣减额度，返回扣减后的设备剩余次数。 */
export async function consume(kv: KVNamespace, deviceId: string, ip: string, limits: QuotaLimits): Promise<number> {
  const [usedDevice] = await Promise.all([bump(kv, quotaKeys.device(deviceId)), bump(kv, quotaKeys.ip(ip))]);
  return Math.max(0, limits.device - usedDevice);
}

/** 注册限流：同一 IP 每天最多注册 limit 个设备。超出返回 false。 */
export async function allowRegister(kv: KVNamespace, ip: string, limit: number): Promise<boolean> {
  const key = quotaKeys.register(ip);
  if ((await read(kv, key)) >= limit) return false;
  await bump(kv, key);
  return true;
}
