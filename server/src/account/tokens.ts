// 登录令牌：access token（JWT，15 分钟）+ refresh token（随机串，30 天，每次刷新都轮换）。
// refresh token 只存哈希；检测到已作废的 refresh token 被再次使用时，说明令牌可能被盗，整条令牌链作废。

import { sign, verify } from "hono/jwt";
import type { Env } from "../env";
import { randomToken, sha256Hex } from "./crypto";

export const ACCESS_TTL_S = 15 * 60;
const REFRESH_TTL_MS = 30 * 24 * 60 * 60 * 1000;
const ISSUER = "nextreply";

export interface AccessClaims {
  sub: string; // user_id
  did: string; // device_id
}

export async function signAccess(env: Env, claims: AccessClaims): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  return sign({ ...claims, iss: ISSUER, typ: "access", iat: now, exp: now + ACCESS_TTL_S }, env.TOKEN_SECRET, "HS256");
}

export async function verifyAccess(env: Env, token: string): Promise<AccessClaims | null> {
  try {
    const p = await verify(token, env.TOKEN_SECRET, { alg: "HS256", iss: ISSUER });
    if (p.typ !== "access" || typeof p.sub !== "string" || typeof p.did !== "string") return null;
    return { sub: p.sub, did: p.did };
  } catch {
    return null;
  }
}

export async function issueRefresh(db: D1Database, userId: string, deviceId: string, familyId = crypto.randomUUID()) {
  const token = randomToken();
  const now = Date.now();
  await db
    .prepare("INSERT INTO refresh_tokens (id, user_id, device_id, family_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)")
    .bind(crypto.randomUUID(), userId, deviceId, familyId, await sha256Hex(token), now + REFRESH_TTL_MS, now)
    .run();
  return token;
}

export type RotateResult =
  | { ok: true; userId: string; deviceId: string; refreshToken: string }
  | { ok: false; reason: "invalid" | "expired" | "reused" | "device_mismatch" };

export async function rotateRefresh(db: D1Database, token: string, deviceId: string): Promise<RotateResult> {
  const row = await db
    .prepare("SELECT id, user_id, device_id, family_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = ?")
    .bind(await sha256Hex(token))
    .first<{ id: string; user_id: string; device_id: string; family_id: string; expires_at: number; revoked_at: number | null }>();
  if (!row) return { ok: false, reason: "invalid" };
  if (row.revoked_at) {
    // 已轮换过的令牌被再次使用：作废整条令牌链，强制重新登录
    await revokeFamily(db, row.family_id);
    return { ok: false, reason: "reused" };
  }
  if (row.device_id !== deviceId) return { ok: false, reason: "device_mismatch" };
  if (row.expires_at < Date.now()) return { ok: false, reason: "expired" };

  await db.prepare("UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?").bind(Date.now(), row.id).run();
  const refreshToken = await issueRefresh(db, row.user_id, row.device_id, row.family_id);
  return { ok: true, userId: row.user_id, deviceId: row.device_id, refreshToken };
}

export async function revokeFamily(db: D1Database, familyId: string) {
  await db.prepare("UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL").bind(Date.now(), familyId).run();
}

export async function revokeByToken(db: D1Database, token: string) {
  const row = await db.prepare("SELECT family_id FROM refresh_tokens WHERE token_hash = ?").bind(await sha256Hex(token)).first<{ family_id: string }>();
  if (row) await revokeFamily(db, row.family_id);
}

export async function revokeDevice(db: D1Database, userId: string, deviceId: string) {
  await db
    .prepare("UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND device_id = ? AND revoked_at IS NULL")
    .bind(Date.now(), userId, deviceId)
    .run();
}
