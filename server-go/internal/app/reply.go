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
)

const maxImageB64Chars = 6_000_000

var (
	mediaTypes     = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}
	idempotencyRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
	holdReleaseTTL = 5 * time.Second
)

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

	// 外层防线：同一 IP 每日请求上限（防止大量账号 / 设备从同一来源刷量）
	ipKey := s.ipKey("ip", s.clientIP(r))
	count, err := s.readCount(ctx, ipKey)
	if err != nil {
		return err
	}
	if count >= s.cfg.IPDailyQuota {
		return apierr.New(apierr.RateLimited, "Too many requests from this network today.")
	}

	// 预扣积分（含频率限制、每日上限）。幂等键由客户端提供，网络重试不会重复扣费
	plan := credits.Plans[caller.Plan]
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
		return apierr.WithDetails(apierr.Code(hold.Code), msg, map[string]any{
			"plan": caller.Plan, "remaining": hold.Remaining, "login_required": caller.Kind == "device",
		})
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
	model := s.cfg.ModelFor(string(caller.Plan))
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
		slog.Info("reply_error", "request_id", requestID, "device", device, "plan", caller.Plan, "code", code, "latency_ms", s.now().Sub(started).Milliseconds())
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
	creditsUsed := 0
	if charged {
		creditsUsed = credits.ReplyCost
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO usage_events (request_id, owner, plan, status, credits, model, latency_ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		requestID, caller.Owner, caller.Plan, out.Result.Status, creditsUsed, out.Model, latency, s.nowMs()); err != nil {
		slog.Error("insert usage event failed", "err", err.Error())
	}

	return writeJSON(w, http.StatusOK, map[string]any{
		"status":   out.Result.Status,
		"analysis": out.Result.Analysis,
		"replies":  out.Result.Replies,
		"meta":     map[string]any{"request_id": requestID, "latency_ms": latency, "remaining_today": bal.Total},
		"credits":  map[string]any{"remaining": bal.Total, "plan": caller.Plan},
	})
}
