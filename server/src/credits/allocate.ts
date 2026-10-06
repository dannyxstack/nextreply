// 积分分配的纯函数部分（不依赖 Durable Object，便于单元测试）。

export type Bucket = "trial" | "bonus" | "daily" | "subscription" | "topup";

export interface Grant {
  id: string;
  bucket: Bucket;
  remaining: number;
  /** 毫秒时间戳；null 表示不过期 */
  expires_at: number | null;
  created_at: number;
}

export interface Allocation {
  grantId: string;
  amount: number;
}

const usable = (g: Grant, now: number) => g.remaining > 0 && (g.expires_at === null || g.expires_at > now);

/** 扣费顺序：先扣最快过期的桶，不过期的最后扣；到期时间相同时先扣先发放的。 */
export function spendOrder(grants: Grant[], now: number): Grant[] {
  return grants
    .filter((g) => usable(g, now))
    .sort((a, b) => (a.expires_at ?? Infinity) - (b.expires_at ?? Infinity) || a.created_at - b.created_at);
}

/** 从各桶中凑出 cost 积分；余额不足返回 null（不做部分扣费）。 */
export function allocate(grants: Grant[], cost: number, now: number): Allocation[] | null {
  const out: Allocation[] = [];
  let left = cost;
  for (const g of spendOrder(grants, now)) {
    if (left <= 0) break;
    const take = Math.min(g.remaining, left);
    out.push({ grantId: g.id, amount: take });
    left -= take;
  }
  return left > 0 ? null : out;
}

export interface BucketSummary {
  bucket: Bucket;
  remaining: number;
  expires_at: number | null;
}

/** 可用余额汇总：同一桶内按最早到期时间合并显示。 */
export function summarize(grants: Grant[], now: number): { total: number; buckets: BucketSummary[] } {
  const map = new Map<Bucket, BucketSummary>();
  let total = 0;
  for (const g of spendOrder(grants, now)) {
    total += g.remaining;
    const cur = map.get(g.bucket);
    if (cur) cur.remaining += g.remaining;
    else map.set(g.bucket, { bucket: g.bucket, remaining: g.remaining, expires_at: g.expires_at });
  }
  return { total, buckets: [...map.values()] };
}

export const utcDay = (now: number) => new Date(now).toISOString().slice(0, 10);

export function endOfUtcDay(now: number): number {
  const d = new Date(now);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() + 1);
}
