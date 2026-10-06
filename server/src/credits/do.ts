// 每个积分账户（用户 u:<id> 或匿名设备 d:<id>）一个 Durable Object。
// DO 内的同步 SQL 操作天然串行执行，扣费是原子的，不会出现并发超扣（KV 做不到这一点）。
//
// 扣费流程：reserve（预扣 + 限流）→ 调用模型 → commit（确认）或 release（退还）。
// 只有成功生成回复才 commit，失败、非聊天截图等情况都 release，不扣用户积分。

import { DurableObject } from "cloudflare:workers";
import type { Env } from "../env";
import { allocate, endOfUtcDay, summarize, utcDay, type Allocation, type Bucket, type Grant } from "./allocate";

/** 超过这个时间仍未 commit / release 的预扣视为异常中断，自动退还 */
const HOLD_TTL_MS = 5 * 60 * 1000;

export interface GrantInput {
  bucket: Bucket;
  amount: number;
  expiresAt: number | null;
  /** 幂等键：同一来源（如某张发票、某天的补充）只发放一次 */
  sourceRef: string;
  reason: string;
}

export interface ReserveInput {
  key: string;
  cost: number;
  dailyCap: number;
  perMinute: number;
  maxConcurrent: number;
  dailyRefill?: number;
}

export type ReserveResult =
  | { ok: true; remaining: number }
  | { ok: false; code: "insufficient_credits" | "rate_limited" | "daily_cap"; remaining: number };

export interface BalanceResult {
  total: number;
  buckets: { bucket: Bucket; remaining: number; expires_at: number | null }[];
  usedToday: number;
}

type Row = Record<string, SqlStorageValue>;

export class CreditAccount extends DurableObject<Env> {
  private sql: SqlStorage;
  /** 最近一分钟的请求时间戳（只在内存中；实例被回收后重置可以接受） */
  private recent: number[] = [];

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS grants (
        id TEXT PRIMARY KEY, bucket TEXT NOT NULL, amount INTEGER NOT NULL, remaining INTEGER NOT NULL,
        expires_at INTEGER, source_ref TEXT NOT NULL UNIQUE, reason TEXT NOT NULL, created_at INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS ledger (
        id INTEGER PRIMARY KEY AUTOINCREMENT, delta INTEGER NOT NULL, reason TEXT NOT NULL,
        grant_id TEXT, ref TEXT, created_at INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS holds (key TEXT PRIMARY KEY, allocations TEXT NOT NULL, created_at INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS usage_day (day TEXT PRIMARY KEY, count INTEGER NOT NULL);
      CREATE INDEX IF NOT EXISTS ledger_ref ON ledger(ref);
    `);
  }

  private grants(): Grant[] {
    return this.sql
      .exec("SELECT id, bucket, remaining, expires_at, created_at FROM grants WHERE remaining > 0")
      .toArray()
      .map((r: Row) => ({
        id: r.id as string,
        bucket: r.bucket as Bucket,
        remaining: r.remaining as number,
        expires_at: (r.expires_at as number | null) ?? null,
        created_at: r.created_at as number,
      }));
  }

  private insertGrant(g: GrantInput, now: number): boolean {
    const exists = this.sql.exec("SELECT 1 FROM grants WHERE source_ref = ?", g.sourceRef).toArray().length > 0;
    if (exists || g.amount <= 0) return false;
    const id = crypto.randomUUID();
    this.sql.exec(
      "INSERT INTO grants (id, bucket, amount, remaining, expires_at, source_ref, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
      id, g.bucket, g.amount, g.amount, g.expiresAt, g.sourceRef, g.reason, now,
    );
    this.sql.exec("INSERT INTO ledger (delta, reason, grant_id, ref, created_at) VALUES (?, ?, ?, ?, ?)", g.amount, g.reason, id, g.sourceRef, now);
    return true;
  }

  private refillDaily(amount: number | undefined, now: number) {
    if (!amount) return;
    this.insertGrant({ bucket: "daily", amount, expiresAt: endOfUtcDay(now), sourceRef: `daily:${utcDay(now)}`, reason: "daily_refill" }, now);
  }

  private returnAllocations(allocs: Allocation[]) {
    for (const a of allocs) this.sql.exec("UPDATE grants SET remaining = remaining + ? WHERE id = ?", a.amount, a.grantId);
  }

  private expireStaleHolds(now: number) {
    const stale = this.sql.exec("SELECT key, allocations FROM holds WHERE created_at < ?", now - HOLD_TTL_MS).toArray();
    for (const h of stale) {
      this.returnAllocations(JSON.parse(h.allocations as string));
      this.sql.exec("DELETE FROM holds WHERE key = ?", h.key);
    }
  }

  private usedToday(now: number): number {
    const r = this.sql.exec("SELECT count FROM usage_day WHERE day = ?", utcDay(now)).toArray()[0];
    return (r?.count as number | undefined) ?? 0;
  }

  /** 发放积分（幂等）。返回 false 表示该来源已发放过。 */
  async grant(input: GrantInput): Promise<boolean> {
    return this.insertGrant(input, Date.now());
  }

  async reserve(input: ReserveInput): Promise<ReserveResult> {
    const now = Date.now();
    this.expireStaleHolds(now);
    this.refillDaily(input.dailyRefill, now);
    const balance = () => summarize(this.grants(), now).total;

    // 同一个幂等键重复请求（客户端网络重试）：进行中的直接视为已预扣
    if (this.sql.exec("SELECT 1 FROM holds WHERE key = ?", input.key).toArray().length > 0) {
      return { ok: true, remaining: balance() };
    }
    // 已经扣过费的：放行但不再扣（空预扣，commit 时不写流水、不计用量）
    if (this.sql.exec("SELECT 1 FROM ledger WHERE ref = ? AND reason = 'reply' LIMIT 1", input.key).toArray().length > 0) {
      this.sql.exec("INSERT INTO holds (key, allocations, created_at) VALUES (?, '[]', ?)", input.key, now);
      return { ok: true, remaining: balance() };
    }

    this.recent = this.recent.filter((t) => t > now - 60_000);
    const inFlight = this.sql.exec("SELECT COUNT(*) AS n FROM holds").toArray()[0].n as number;
    if (this.recent.length >= input.perMinute || inFlight >= input.maxConcurrent) {
      return { ok: false, code: "rate_limited", remaining: balance() };
    }
    if (this.usedToday(now) + inFlight >= input.dailyCap) {
      return { ok: false, code: "daily_cap", remaining: balance() };
    }

    const allocs = allocate(this.grants(), input.cost, now);
    if (!allocs) return { ok: false, code: "insufficient_credits", remaining: balance() };

    for (const a of allocs) this.sql.exec("UPDATE grants SET remaining = remaining - ? WHERE id = ?", a.amount, a.grantId);
    this.sql.exec("INSERT INTO holds (key, allocations, created_at) VALUES (?, ?, ?)", input.key, JSON.stringify(allocs), now);
    this.recent.push(now);
    return { ok: true, remaining: balance() };
  }

  /** 确认扣费：写流水、计入当日用量。 */
  async commit(key: string): Promise<void> {
    const now = Date.now();
    const h = this.sql.exec("SELECT allocations FROM holds WHERE key = ?", key).toArray()[0];
    if (!h) return;
    const allocs: Allocation[] = JSON.parse(h.allocations as string);
    if (allocs.length === 0) {
      // 重试已扣过费的请求
      this.sql.exec("DELETE FROM holds WHERE key = ?", key);
      return;
    }
    for (const a of allocs) {
      this.sql.exec("INSERT INTO ledger (delta, reason, grant_id, ref, created_at) VALUES (?, 'reply', ?, ?, ?)", -a.amount, a.grantId, key, now);
    }
    this.sql.exec(
      "INSERT INTO usage_day (day, count) VALUES (?, 1) ON CONFLICT(day) DO UPDATE SET count = count + 1",
      utcDay(now),
    );
    this.sql.exec("DELETE FROM holds WHERE key = ?", key);
  }

  /** 退还预扣（请求失败、非聊天截图、被取消）。 */
  async release(key: string): Promise<void> {
    const h = this.sql.exec("SELECT allocations FROM holds WHERE key = ?", key).toArray()[0];
    if (!h) return;
    this.returnAllocations(JSON.parse(h.allocations as string));
    this.sql.exec("DELETE FROM holds WHERE key = ?", key);
  }

  async balance(dailyRefill?: number): Promise<BalanceResult> {
    const now = Date.now();
    this.expireStaleHolds(now);
    this.refillDaily(dailyRefill, now);
    return { ...summarize(this.grants(), now), usedToday: this.usedToday(now) };
  }

  /** 收回某个桶（或某个来源）的剩余积分：退款、拒付、订阅被撤销时使用。 */
  async revoke(bucket: Bucket, sourceRef?: string): Promise<number> {
    const now = Date.now();
    const rows = this.sql
      .exec(
        sourceRef
          ? "SELECT id, remaining FROM grants WHERE bucket = ? AND source_ref = ? AND remaining > 0"
          : "SELECT id, remaining FROM grants WHERE bucket = ? AND remaining > 0",
        ...(sourceRef ? [bucket, sourceRef] : [bucket]),
      )
      .toArray();
    let total = 0;
    for (const r of rows) {
      total += r.remaining as number;
      this.sql.exec("UPDATE grants SET remaining = 0 WHERE id = ?", r.id);
      this.sql.exec("INSERT INTO ledger (delta, reason, grant_id, ref, created_at) VALUES (?, 'revoke', ?, ?, ?)", -(r.remaining as number), r.id, sourceRef ?? null, now);
    }
    return total;
  }
}
