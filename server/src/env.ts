export interface Env {
  QUOTA: KVNamespace;

  ANTHROPIC_API_KEY: string;
  TOKEN_SECRET: string;

  MODEL: string;
  EFFORT: string;
  THINKING: string;
  FALLBACKS: string;
  MAX_TOKENS: string;
  DAILY_QUOTA: string;
  IP_DAILY_QUOTA: string;
  REGISTER_PER_IP: string;
}

export function intVar(value: string | undefined, fallback: number): number {
  const n = Number.parseInt(value ?? "", 10);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}
