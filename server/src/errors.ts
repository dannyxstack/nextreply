export type ErrorCode =
  | "unauthorized"
  | "bad_request"
  | "quota_exceeded"
  | "insufficient_credits"
  | "rate_limited"
  | "daily_cap"
  | "upstream_error"
  | "refusal"
  | "invalid_output"
  | "timeout"
  | "internal";

const STATUS: Record<ErrorCode, number> = {
  unauthorized: 401,
  bad_request: 400,
  quota_exceeded: 429,
  insufficient_credits: 402,
  rate_limited: 429,
  daily_cap: 429,
  upstream_error: 502,
  refusal: 502,
  invalid_output: 502,
  timeout: 504,
  internal: 500,
};

export class ApiError extends Error {
  constructor(
    public readonly code: ErrorCode,
    message: string,
    /** 附加给客户端的结构化信息（如当前套餐、是否需要登录） */
    public readonly details?: Record<string, unknown>,
  ) {
    super(message);
  }

  get status(): number {
    return STATUS[this.code];
  }

  toJSON() {
    return { error: { code: this.code, message: this.message, ...(this.details ? { details: this.details } : {}) } };
  }
}
