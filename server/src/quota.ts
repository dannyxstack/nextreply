// 基于 KV 的按 IP 每日计数（软限制，防刷的外层防线）。
// KV 是最终一致的，偶尔多放行一两次可以接受；精确的账户积分在 Durable Object 里。

const TTL_SECONDS = 2 * 24 * 60 * 60;

export function today(now = new Date()): string {
  return now.toISOString().slice(0, 10);
}

export const ipKeys = {
  reply: (ip: string) => `ip:${ip}:${today()}`,
  register: (ip: string) => `reg:${ip}:${today()}`,
  trial: (ip: string) => `trial:${ip}:${today()}`,
  otp: (ip: string) => `otp:${ip}:${today()}`,
};

export async function readCount(kv: KVNamespace, key: string): Promise<number> {
  const v = await kv.get(key);
  return v ? Number.parseInt(v, 10) || 0 : 0;
}

export async function bump(kv: KVNamespace, key: string): Promise<number> {
  const next = (await readCount(kv, key)) + 1;
  await kv.put(key, String(next), { expirationTtl: TTL_SECONDS });
  return next;
}

/** 未达上限时计数 +1 并返回 true；已达上限返回 false。 */
export async function takeIfUnder(kv: KVNamespace, key: string, limit: number): Promise<boolean> {
  if ((await readCount(kv, key)) >= limit) return false;
  await bump(kv, key);
  return true;
}
