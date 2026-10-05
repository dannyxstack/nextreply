// 无状态设备 token：base64url(device_id) + "." + base64url(HMAC_SHA256(secret, device_id))

const encoder = new TextEncoder();

function toBase64Url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64Url(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob(b64);
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

async function hmacKey(secret: string): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, [
    "sign",
    "verify",
  ]);
}

const DEVICE_ID_RE = /^[A-Za-z0-9-]{16,64}$/;

export function isValidDeviceId(id: unknown): id is string {
  return typeof id === "string" && DEVICE_ID_RE.test(id);
}

export async function issueToken(secret: string, deviceId: string): Promise<string> {
  const key = await hmacKey(secret);
  const sig = new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(deviceId)));
  return `${toBase64Url(encoder.encode(deviceId))}.${toBase64Url(sig)}`;
}

/** 校验通过返回 device_id，否则返回 null。 */
export async function verifyToken(secret: string, token: string): Promise<string | null> {
  const [idPart, sigPart, ...rest] = token.split(".");
  if (!idPart || !sigPart || rest.length > 0) return null;
  try {
    const deviceId = new TextDecoder().decode(fromBase64Url(idPart));
    if (!isValidDeviceId(deviceId)) return null;
    const key = await hmacKey(secret);
    const ok = await crypto.subtle.verify("HMAC", key, fromBase64Url(sigPart), encoder.encode(deviceId));
    return ok ? deviceId : null;
  } catch {
    return null;
  }
}

/** 日志里只记录设备 ID 的短哈希。 */
export async function deviceHash(deviceId: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(deviceId)));
  return Array.from(digest.slice(0, 4), (b) => b.toString(16).padStart(2, "0")).join("");
}
