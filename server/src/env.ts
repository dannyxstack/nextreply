import type { CreditAccount } from "./credits/do";

export interface Env {
  QUOTA: KVNamespace;
  DB: D1Database;
  CREDITS: DurableObjectNamespace<CreditAccount>;

  ANTHROPIC_API_KEY: string;
  TOKEN_SECRET: string;

  MODEL: string;
  EFFORT: string;
  THINKING: string;
  FALLBACKS: string;
  MAX_TOKENS: string;
  IP_DAILY_QUOTA: string;
  REGISTER_PER_IP: string;

  /** 对外地址，用于生成登录 / 结账链接 */
  PUBLIC_URL: string;
  /** 本地开发模式：没配邮件服务时把验证码显示在页面上；没配 Stripe 时提供模拟支付。线上必须为 "false" */
  DEV_MODE?: string;
  /** 开发测试用：不调用模型，返回固定结果（只在 DEV_MODE 下生效） */
  MOCK_AI?: string;

  RESEND_API_KEY?: string;
  EMAIL_FROM?: string;
  TURNSTILE_SITE_KEY?: string;
  TURNSTILE_SECRET?: string;

  STRIPE_SECRET_KEY?: string;
  STRIPE_WEBHOOK_SECRET?: string;
  STRIPE_PRICE_PRO?: string;
  STRIPE_PRICE_PRO_PLUS?: string;
}

export function intVar(value: string | undefined, fallback: number): number {
  const n = Number.parseInt(value ?? "", 10);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

export const isDev = (env: Env) => env.DEV_MODE === "true";
