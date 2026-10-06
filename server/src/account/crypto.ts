const encoder = new TextEncoder();

export function toBase64Url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** 高熵随机令牌（refresh token、授权码、网页票据） */
export function randomToken(bytes = 32): string {
  return toBase64Url(crypto.getRandomValues(new Uint8Array(bytes)));
}

export function randomDigits(n: number): string {
  const buf = crypto.getRandomValues(new Uint32Array(n));
  return Array.from(buf, (v) => String(v % 10)).join("");
}

export async function sha256Hex(input: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(input)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** PKCE S256：code_challenge = base64url(sha256(code_verifier)) */
export async function pkceChallenge(verifier: string): Promise<string> {
  return toBase64Url(new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(verifier))));
}

/** 等长字符串的常量时间比较，避免计时攻击 */
export function safeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}
