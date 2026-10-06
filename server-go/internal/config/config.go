// Package config 从环境变量读取服务配置。变量名与旧版 Workers 服务保持一致，方便迁移。
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr   string
	DatabasePath string

	AnthropicAPIKey string
	TokenSecret     string

	// 付费套餐的默认模型和 effort
	Model  string
	Effort string
	// 每个套餐实际使用的模型（见 ModelFor）
	PlanModels map[string]ModelChoice
	Thinking   string
	Fallbacks  string
	MaxTokens  int64

	IPDailyQuota  int
	RegisterPerIP int

	// 对外地址，用于生成登录 / 结账链接
	PublicURL string
	// 本地开发模式：没配邮件服务时把验证码显示在页面上；没配 Stripe 时提供模拟支付。线上必须为 false
	DevMode bool
	// 开发测试用：不调用模型，返回固定结果（只在 DevMode 下生效）
	MockAI bool

	// 反向代理写入真实客户端 IP 的请求头（如 X-Real-IP）。
	// 只有在代理会覆盖该请求头时才能设置，否则客户端可以伪造 IP 绕过按 IP 的限额。
	ClientIPHeader string

	// 管理后台：设置 AdminToken 才启动，监听 AdminAddr（只应对本机开放），页面时间按 AdminTZ 显示
	AdminToken string
	AdminAddr  string
	AdminTZ    string

	ResendAPIKey     string
	EmailFrom        string
	TurnstileSiteKey string
	TurnstileSecret  string

	StripeSecretKey     string
	StripeWebhookSecret string
	StripePricePro      string
	StripePriceProPlus  string
}

func FromEnv() (*Config, error) {
	c := &Config{
		ListenAddr:          env("LISTEN_ADDR", ":8787"),
		DatabasePath:        env("DATABASE_PATH", "./data/nextreply.db"),
		AnthropicAPIKey:     os.Getenv("ANTHROPIC_API_KEY"),
		TokenSecret:         os.Getenv("TOKEN_SECRET"),
		Model:               env("MODEL", "claude-opus-5-5"),
		Effort:              env("EFFORT", "low"),
		Thinking:            env("THINKING", "auto"),
		Fallbacks:           env("FALLBACKS", "default"),
		MaxTokens:           int64(intEnv("MAX_TOKENS", 8000)),
		IPDailyQuota:        intEnv("IP_DAILY_QUOTA", 200),
		RegisterPerIP:       intEnv("REGISTER_PER_IP", 20),
		PublicURL:           strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:8787"), "/"),
		DevMode:             os.Getenv("DEV_MODE") == "true",
		ClientIPHeader:      os.Getenv("CLIENT_IP_HEADER"),
		AdminToken:          os.Getenv("ADMIN_TOKEN"),
		AdminAddr:           env("ADMIN_ADDR", "127.0.0.1:8788"),
		AdminTZ:             env("ADMIN_TZ", "Asia/Shanghai"),
		ResendAPIKey:        os.Getenv("RESEND_API_KEY"),
		EmailFrom:           env("EMAIL_FROM", "NextReply <noreply@example.com>"),
		TurnstileSiteKey:    os.Getenv("TURNSTILE_SITE_KEY"),
		TurnstileSecret:     os.Getenv("TURNSTILE_SECRET"),
		StripeSecretKey:     os.Getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripePricePro:      os.Getenv("STRIPE_PRICE_PRO"),
		StripePriceProPlus:  os.Getenv("STRIPE_PRICE_PRO_PLUS"),
	}
	c.MockAI = c.DevMode && os.Getenv("MOCK_AI") == "true"

	// 免费用户（trial 匿名体验 + free 已登录未订阅）默认用更便宜的模型；付费用户用 MODEL。
	// 每个套餐还可以用 MODEL_<PLAN> / EFFORT_<PLAN> 单独覆盖。
	paid := ModelChoice{Model: c.Model, Effort: c.Effort}
	free := ModelChoice{Model: env("MODEL_FREE", "claude-sonnet-5"), Effort: env("EFFORT_FREE", c.Effort)}
	c.PlanModels = map[string]ModelChoice{}
	for plan, base := range map[string]ModelChoice{"trial": free, "free": free, "pro": paid, "pro_plus": paid} {
		suffix := strings.ToUpper(plan)
		c.PlanModels[plan] = ModelChoice{Model: env("MODEL_"+suffix, base.Model), Effort: env("EFFORT_"+suffix, base.Effort)}
	}
	return c, c.validate()
}

type ModelChoice struct {
	Model  string
	Effort string
}

// ModelFor 返回某个套餐使用的模型；未知套餐按付费默认值处理。
func (c *Config) ModelFor(plan string) ModelChoice {
	if m, ok := c.PlanModels[plan]; ok {
		return m
	}
	return ModelChoice{Model: c.Model, Effort: c.Effort}
}

func (c *Config) validate() error {
	if c.TokenSecret == "" {
		return errors.New("TOKEN_SECRET is required")
	}
	// 线上的令牌密钥太短等于没有签名
	if !c.DevMode && len(c.TokenSecret) < 32 {
		return errors.New("TOKEN_SECRET must be at least 32 characters when DEV_MODE is off")
	}
	if c.AdminToken != "" && len(c.AdminToken) < 24 {
		return errors.New("ADMIN_TOKEN must be at least 24 characters")
	}
	if !c.DevMode && c.ResendAPIKey == "" {
		return errors.New("RESEND_API_KEY is required when DEV_MODE is off (login codes cannot be delivered)")
	}
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
