package ai

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/nextreply/server/internal/apierr"
)

type Settings struct {
	APIKey    string
	Thinking  string
	Fallbacks string
	MaxTokens int64
}

type Input struct {
	// 本次请求使用的模型和 effort（按用户套餐选择）
	Model       string
	Effort      string
	ImageBase64 string
	MediaType   string
	Locale      string
	DisplayName string
}

type Usage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

type Output struct {
	Result       *Result
	Model        string
	Usage        Usage
	FallbackUsed bool
}

// Generator 抽象出模型调用，测试和 MOCK_AI 模式下替换成固定结果。
type Generator interface {
	Generate(ctx context.Context, in Input) (*Output, error)
}

// 服务端 fallbacks: "default" 只在这些模型上可用
var fallbackModels = map[string]bool{"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true}

var efforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

const requestTimeout = 25 * time.Second

type Claude struct {
	s      Settings
	client anthropic.Client
}

func NewClaude(s Settings) *Claude {
	return &Claude{
		s: s,
		client: anthropic.NewClient(
			option.WithAPIKey(s.APIKey),
			option.WithMaxRetries(1),
			option.WithRequestTimeout(requestTimeout),
		),
	}
}

func (c *Claude) params(in Input) anthropic.BetaMessageNewParams {
	p := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(in.Model),
		MaxTokens: c.s.MaxTokens,
		System: []anthropic.BetaTextBlockParam{{
			Text:         SystemPrompt,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(
				anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
					Data:      in.ImageBase64,
					MediaType: anthropic.BetaBase64ImageSourceMediaType(in.MediaType),
				}),
				anthropic.NewBetaTextBlock(BuildUserText(in.DisplayName, in.Locale)),
			),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format: anthropic.BetaJSONOutputFormatParam{Schema: ReplyJSONSchema},
		},
	}
	// "none" 或空：不传；Haiku 不支持 effort，传了会被拒绝
	if efforts[in.Effort] && !strings.HasPrefix(in.Model, "claude-haiku") {
		p.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(in.Effort)
	}
	switch c.s.Thinking {
	case "between_tools":
		p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfBetweenTools: &anthropic.BetaThinkingConfigBetweenToolsParam{}}
	case "adaptive":
		p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{}}
	default:
		// auto：不传，由模型默认行为决定
	}
	// 拒答兜底：在支持的模型上由服务端按拒答类别自动换模型重试
	if c.s.Fallbacks != "off" && fallbackModels[in.Model] {
		p.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	return p
}

func (c *Claude) Generate(ctx context.Context, in Input) (*Output, error) {
	if c.s.APIKey == "" {
		slog.Error("ANTHROPIC_API_KEY is not configured")
		return nil, apierr.New(apierr.Internal, "Server AI credentials are not configured.")
	}
	msg, err := c.client.Beta.Messages.New(ctx, c.params(in))
	if err != nil {
		return nil, mapError(ctx, err)
	}

	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, apierr.New(apierr.Refusal, "The model declined to analyze this conversation.")
	case anthropic.BetaStopReasonMaxTokens:
		return nil, apierr.New(apierr.InvalidOutput, "The model response was truncated.")
	}

	var sb strings.Builder
	for _, b := range msg.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	result, err := ParseResult(sb.String())
	if err != nil {
		// 只记录错误类型，不记录模型输出（其中包含聊天内容）
		slog.Warn("model output rejected", "reason", err.Error())
		return nil, apierr.New(apierr.InvalidOutput, "The model returned an unexpected shape.")
	}

	fallbackUsed := false
	for _, it := range msg.Usage.Iterations {
		if it.Type == "fallback_message" {
			fallbackUsed = true
		}
	}
	return &Output{
		Result: result,
		Model:  string(msg.Model),
		Usage: Usage{
			Input: msg.Usage.InputTokens, Output: msg.Usage.OutputTokens,
			CacheRead: msg.Usage.CacheReadInputTokens, CacheWrite: msg.Usage.CacheCreationInputTokens,
		},
		FallbackUsed: fallbackUsed,
	}, nil
}

// mapError 上游错误信息只写日志（不含用户内容），不透传给客户端。
func mapError(ctx context.Context, err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		slog.Error("anthropic api error", "status", apiErr.StatusCode, "request_id", apiErr.RequestID)
		switch {
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return apierr.New(apierr.Internal, "Server AI credentials are invalid.")
		case apiErr.StatusCode == 429 || apiErr.StatusCode == 529:
			return apierr.New(apierr.UpstreamError, "The AI service is busy.")
		case apiErr.StatusCode == 400:
			return apierr.New(apierr.UpstreamError, "The AI service rejected the request.")
		default:
			return apierr.New(apierr.UpstreamError, "AI service error.")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return apierr.New(apierr.Timeout, "The AI request timed out.")
	}
	slog.Error("anthropic unexpected error", "err", err.Error())
	return apierr.New(apierr.UpstreamError, "Unexpected error calling the AI service.")
}

// Mock 开发测试用的固定结果：测试积分扣减时不调用模型、不花钱。
type Mock struct{}

func (Mock) Generate(ctx context.Context, in Input) (*Output, error) {
	return &Output{
		Result: &Result{
			Status:   StatusOK,
			Analysis: Analysis{Language: "zh-CN", LatestMessage: "（模拟）", Summary: "这是模拟结果（MOCK_AI）", Emotion: "-", Intent: "-", Strategy: "-"},
			Replies: []Reply{
				{Style: "empathetic", Label: "得体", Text: "（模拟回复 1）"},
				{Style: "funny", Label: "轻松", Text: "（模拟回复 2）"},
				{Style: "direct", Label: "简洁", Text: "（模拟回复 3）"},
			},
		},
		Model: "mock",
	}, nil
}
