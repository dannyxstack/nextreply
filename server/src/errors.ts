export type ErrorCode =
  | "unauthorized"
  | "bad_request"
  | "quota_exceeded"
  | "upstream_error"
  | "refusal"
  | "invalid_output"
  | "timeout"
  | "internal";

const STATUS: Record<ErrorCode, number> = {
  unauthorized: 401,
  bad_request: 400,
  quota_exceeded: 429,
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
  ) {
    super(message);
  }

  get status(): number {
    return STATUS[this.code];
  }

  toJSON() {
    return { error: { code: this.code, message: this.message } };
  }
}
