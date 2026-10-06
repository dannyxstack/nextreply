// Package apierr 定义返回给客户端的错误。错误码与桌面端约定一致（TECH_DESIGN §5.1）。
package apierr

import "net/http"

type Code string

const (
	Unauthorized        Code = "unauthorized"
	BadRequest          Code = "bad_request"
	NotFound            Code = "not_found"
	QuotaExceeded       Code = "quota_exceeded"
	InsufficientCredits Code = "insufficient_credits"
	RateLimited         Code = "rate_limited"
	DailyCap            Code = "daily_cap"
	UpstreamError       Code = "upstream_error"
	Refusal             Code = "refusal"
	InvalidOutput       Code = "invalid_output"
	Timeout             Code = "timeout"
	// 达到每日成本上限、暂停免费用户时返回；客户端按"服务暂不可用，稍后重试"处理
	ServiceBusy Code = "service_busy"
	Internal    Code = "internal"
)

var statusOf = map[Code]int{
	Unauthorized:        http.StatusUnauthorized,
	BadRequest:          http.StatusBadRequest,
	NotFound:            http.StatusNotFound,
	QuotaExceeded:       http.StatusTooManyRequests,
	InsufficientCredits: http.StatusPaymentRequired,
	RateLimited:         http.StatusTooManyRequests,
	DailyCap:            http.StatusTooManyRequests,
	UpstreamError:       http.StatusBadGateway,
	Refusal:             http.StatusBadGateway,
	InvalidOutput:       http.StatusBadGateway,
	Timeout:             http.StatusGatewayTimeout,
	ServiceBusy:         http.StatusServiceUnavailable,
	Internal:            http.StatusInternalServerError,
}

type Error struct {
	Code    Code
	Message string
	// 附加给客户端的结构化信息（如当前套餐、是否需要登录）
	Details map[string]any
}

func New(code Code, msg string) *Error { return &Error{Code: code, Message: msg} }

func WithDetails(code Code, msg string, details map[string]any) *Error {
	return &Error{Code: code, Message: msg, Details: details}
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func (e *Error) Status() int {
	if s, ok := statusOf[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func (e *Error) Body() map[string]any {
	inner := map[string]any{"code": e.Code, "message": e.Message}
	if e.Details != nil {
		inner["details"] = e.Details
	}
	return map[string]any{"error": inner}
}
