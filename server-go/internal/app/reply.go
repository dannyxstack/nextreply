package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/apierr"
	"github.com/nextreply/server/internal/authn"
	"github.com/nextreply/server/internal/credits"
	"github.com/nextreply/server/internal/settings"
)

const maxImageB64Chars = 6_000_000

var (
	mediaTypes     = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
	idempotencyRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
	holdReleaseTTL = 5 * time.Second
)

// usageEvent 一次 /v1/reply 的元数据（不含任何内容），写入 usage_events 供后台统计。
type usageEvent struct {
	requestID, owner, deviceID, ip string
	plan                           credits.PlanID
	status, model, errorCode       string
	credits                        int
	latencyMs                      int64
	usage                          ai.Usage
	costMicros                     int64
}

func (s *Server) recordUsage(ctx context.Context, e usageEvent) {
	_, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO usage_events (request_id, owner, plan, status, credits, model, latency_ms, created_at,
		   device_id, ip, tokens_in, tokens_out, cache_read, cache_write, cost_micros, error_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.requestID, e.owner, e.plan, e.status, e.credits, nullStr(e.model), e.latencyMs, s.nowMs(),
		nullStr(e.deviceID), nullStr(e.ip), e.usage.Input, e.usage.Output, e.usage.CacheRead, e.usage.CacheWrite, e.costMicros, nullStr(e.errorCode))
	if err != nil {
		slog.Error("insert usage event failed", "err", err.Error())
	}
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request) error {
	started := s.now()
	requestID := authn.NewUUID()
	ctx := r.Context()
	caller, err := s.resolveCaller(r)
	if err != nil {
		return err
	}

	body, err := readJSON(r)
	if err != nil {
		return err
	}
	image := str(body, "image")
	mediaType := str(body, "media_type")
	if mediaType == "" {
		mediaType = "image/jpeg"
	}
	if image == "" {
		return apierr.New(apierr.BadRequest, "Missing image.")
	}
	if len(image) > maxImageB64Chars {
		return apierr.New(apierr.BadRequest, "Image is too large.")
	}
	if !mediaTypes[mediaType] {
		return apierr.New(apierr.BadRequest, "Unsupported media_type.")
	}

	ip := s.clientIP(r)
	event := usageEvent{requestID: requestID, owner: caller.Owner, deviceID: caller.DeviceID, ip: ip, plan: caller.Plan}
	reject := func(reason string, err error) error {
		event.status, event.errorCode = "rejected:"+reason, reason
		event.latencyMs = s.now().Sub(started).Milliseconds()
		s.recordUsage(ctx, event)
		return err
	}

	// 外层防线：同一 IP 每日请求上限（防止大量账号 / 设备从同一来源刷量）
	ipKey := s.ipKey("ip", ip)
	count, err := s.readCount(ctx, ipKey)
	if err != nil {
		return err
	}
	if count >= s.settings.IPDailyQuota() {
		return reject("ip_quota", apierr.New(apierr.RateLimited, "Too many requests from this network today."))
	}

	// 每日成本上限：只影响免费用户（暂停或降级模型），付费用户照常
	costCap, err := s.settings.CostCap(ctx)
	if err != nil {
		return err
	}
	if costCap.Hit && costCap.Action == settings.CapPauseFree && settings.IsFreeTier(caller.Plan) {
		return reject("cost_cap", apierr.New(apierr.ServiceBusy, "The service is busy right now, please try again later."))
	}

	// 预扣积分（含频率限制、每日上限）。幂等键由客户端提供，网络重试不会重复扣费
	plan := s.settings.Plan(caller.Plan)
	holdKey := r.Header.Get("Idempotency-Key")
	if !idempotencyRe.MatchString(holdKey) {
		holdKey = requestID
	}
	hold, err := s.credits.Reserve(ctx, caller.Owner, credits.ReserveInput{
		Key: holdKey, Cost: credits.ReplyCost, DailyCap: plan.DailyCap,
		PerMinute: credits.RatePerMinute, MaxConcurrent: credits.RateMaxConcurrent, DailyRefill: plan.DailyRefill,
	})
	if err != nil {
		return err
	}
	if !hold.OK() {
		msg := map[string]string{"insufficient_credits": "Not enough credits.", "rate_limited": "Too many requests, slow down.", "daily_cap": "Daily limit reached."}[hold.Code]
		return reject(hold.Code, apierr.WithDetails(apierr.Code(hold.Code), msg, map[string]any{
			"plan": caller.Plan, "remaining": hold.Remaining, "login_required": caller.Kind == "device",
		}))
	}

	// 客户端断开时请求的 ctx 会被取消；退还 / 确认积分必须用独立的 ctx，否则会留下悬空预扣
	settle := func(fn func(ctx context.Context, owner, key string) error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), holdReleaseTTL)
		defer cancel()
		if err := fn(c, caller.Owner, holdKey); err != nil {
			slog.Error("settle credit hold failed", "request_id", requestID, "err", err.Error())
		}
	}

	device := authn.DeviceHash(caller.DeviceID)
	model := s.settings.ModelFor(caller.Plan, costCap)
	out, err := s.ai.Generate(ctx, ai.Input{
		Model:       model.Model,
		Effort:      model.Effort,
		ImageBase64: image,
		MediaType:   mediaType,
		Locale:      optionalString(body, "locale", 20),
		DisplayName: optionalString(body, "display_name", 64),
	})
	if err != nil {
		settle(s.credits.Release)
		code := apierr.Internal
		var ae *apierr.Error
		if errors.As(err, &ae) {
			code = ae.Code
		}
		event.status, event.errorCode, event.model = "error:"+string(code), string(code), model.Model
		event.latencyMs = s.now().Sub(started).Milliseconds()
		s.recordUsage(ctx, event)
		slog.Info("reply_error", "request_id", requestID, "device", device, "plan", caller.Plan, "code", code, "latency_ms", event.latencyMs)
		return err
	}

	// 只有成功生成回复才扣积分；看不懂截图（非聊天、内容不足）不扣
	charged := out.Result.Status == ai.StatusOK
	if charged {
		settle(s.credits.Commit)
		if err := s.bump(context.WithoutCancel(ctx), ipKey); err != nil {
			slog.Error("bump ip counter failed", "err", err.Error())
		}
	} else {
		settle(s.credits.Release)
	}
	bal, err := s.credits.Balance(context.WithoutCancel(ctx), caller.Owner, plan.DailyRefill)
	if err != nil {
		return err
	}
	latency := s.now().Sub(started).Milliseconds()

	// 只记录元数据：不记录图片、聊天内容、回复内容
	slog.Info("reply", "request_id", requestID, "device", device, "plan", caller.Plan, "status", out.Result.Status, "model", out.Model,
		"fallback", out.FallbackUsed, "latency_ms", latency, "tokens_in", out.Usage.Input, "tokens_out", out.Usage.Output,
		"cache_read", out.Usage.CacheRead, "cache_write", out.Usage.CacheWrite, "client_version", optionalString(body, "client_version", 20))
	event.status, event.model, event.latencyMs, event.usage = string(out.Result.Status), out.Model, latency, out.Usage
	event.costMicros = ai.CostMicros(out.Model, out.Usage)
	s.settings.AddCost(event.costMicros)
	if charged {
		event.credits = credits.ReplyCost
	}
	s.recordUsage(ctx, event)

	return writeJSON(w, http.StatusOK, map[string]any{
		"status":   out.Result.Status,
		"analysis": out.Result.Analysis,
		"replies":  out.Result.Replies,
		"meta":     map[string]any{"request_id": requestID, "latency_ms": latency, "remaining_today": bal.Total},
		"credits":  map[string]any{"remaining": bal.Total, "plan": caller.Plan},
	})
}
