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

	Model     string
	Effort    string
	Thinking  string
	Fallbacks string
	MaxTokens int64

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
	return c, c.validate()
}

func (c *Config) validate() error {
	if c.TokenSecret == "" {
		return errors.New("TOKEN_SECRET is required")
	}
	// 线上的令牌密钥太短等于没有签名
	if !c.DevMode && len(c.TokenSecret) < 32 {
		return errors.New("TOKEN_SECRET must be at least 32 characters when DEV_MODE is off")
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
