import type { Env } from "../env";

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;

export const isValidEmail = (email: string) => email.length <= 254 && EMAIL_RE.test(email);

/**
 * 规范化邮箱，用于判断"是不是同一个人"：小写；去掉 + 后缀；
 * Gmail 还会忽略本地部分的点号（a.b@gmail.com 和 ab@gmail.com 是同一个邮箱）。
 */
export function normalizeEmail(email: string): string {
  const lower = email.trim().toLowerCase();
  const at = lower.lastIndexOf("@");
  let local = lower.slice(0, at);
  let domain = lower.slice(at + 1);
  if (domain === "googlemail.com") domain = "gmail.com";
  local = local.split("+")[0];
  if (domain === "gmail.com") local = local.replace(/\./g, "");
  return `${local}@${domain}`;
}

// 常见一次性邮箱域名（批量注册刷赠送额度的主要来源）。上线后可以换成定期更新的完整列表。
const DISPOSABLE = new Set([
  "mailinator.com", "guerrillamail.com", "guerrillamail.info", "sharklasers.com", "10minutemail.com",
  "temp-mail.org", "tempmail.com", "tempmail.dev", "throwawaymail.com", "yopmail.com", "trashmail.com",
  "getnada.com", "maildrop.cc", "dispostable.com", "fakeinbox.com", "mohmal.com", "emailondeck.com",
  "mintemail.com", "mailnesia.com", "tempr.email", "discard.email", "spamgourmet.com", "moakt.com",
  "linshiyouxiang.net", "bccto.me", "chacuo.net", "027168.com",
]);

export function isDisposable(email: string): boolean {
  const domain = email.trim().toLowerCase().split("@").pop() ?? "";
  return DISPOSABLE.has(domain);
}

export type SendResult = { sent: true } | { sent: false; devCode: string };

/** 发送登录验证码。没配置邮件服务时：开发模式下把验证码返回给页面显示，线上直接报错。 */
export async function sendLoginCode(env: Env, to: string, code: string, devMode: boolean): Promise<SendResult> {
  if (!env.RESEND_API_KEY) {
    if (devMode) return { sent: false, devCode: code };
    throw new Error("email provider not configured");
  }
  const resp = await fetch("https://api.resend.com/emails", {
    method: "POST",
    headers: { authorization: `Bearer ${env.RESEND_API_KEY}`, "content-type": "application/json" },
    body: JSON.stringify({
      from: env.EMAIL_FROM ?? "NextReply <noreply@example.com>",
      to,
      subject: `NextReply 登录验证码：${code}`,
      text: `你的 NextReply 登录验证码是 ${code}，10 分钟内有效。\n如果不是你本人操作，请忽略这封邮件。`,
    }),
  });
  if (!resp.ok) throw new Error(`email send failed: ${resp.status}`);
  return { sent: true };
}

/** Cloudflare Turnstile 人机验证。未配置时跳过（本地开发）。 */
export async function verifyTurnstile(env: Env, token: string | undefined, ip: string): Promise<boolean> {
  if (!env.TURNSTILE_SECRET) return true;
  if (!token) return false;
  const form = new FormData();
  form.append("secret", env.TURNSTILE_SECRET);
  form.append("response", token);
  form.append("remoteip", ip);
  const resp = await fetch("https://challenges.cloudflare.com/turnstile/v0/siteverify", { method: "POST", body: form });
  const data = (await resp.json()) as { success?: boolean };
  return data.success === true;
}
