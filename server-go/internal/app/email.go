package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]{2,}$`)

func isValidEmail(email string) bool { return len(email) <= 254 && emailRe.MatchString(email) }

// normalizeEmail 规范化邮箱，用于判断"是不是同一个人"：小写；去掉 + 后缀；
// Gmail 还会忽略本地部分的点号（a.b@gmail.com 和 ab@gmail.com 是同一个邮箱）。
func normalizeEmail(email string) string {
	lower := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(lower, "@")
	if at < 0 {
		return lower
	}
	local, domain := lower[:at], lower[at+1:]
	if domain == "googlemail.com" {
		domain = "gmail.com"
	}
	local, _, _ = strings.Cut(local, "+")
	if domain == "gmail.com" {
		local = strings.ReplaceAll(local, ".", "")
	}
	return local + "@" + domain
}

// 常见一次性邮箱域名（批量注册刷赠送额度的主要来源）。上线后可以换成定期更新的完整列表。
var disposable = map[string]bool{}

func init() {
	for _, d := range strings.Fields(`mailinator.com guerrillamail.com guerrillamail.info sharklasers.com 10minutemail.com
		temp-mail.org tempmail.com tempmail.dev throwawaymail.com yopmail.com trashmail.com
		getnada.com maildrop.cc dispostable.com fakeinbox.com mohmal.com emailondeck.com
		mintemail.com mailnesia.com tempr.email discard.email spamgourmet.com moakt.com
		linshiyouxiang.net bccto.me chacuo.net 027168.com`) {
		disposable[d] = true
	}
}

func isDisposable(email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	return disposable[e[strings.LastIndex(e, "@")+1:]]
}

// sendLoginCode 发送登录验证码（Resend）。没配置邮件服务时：开发模式下返回验证码给页面显示，线上直接报错。
func (s *Server) sendLoginCode(ctx context.Context, to, code string) (devCode string, err error) {
	if s.cfg.ResendAPIKey == "" {
		if s.cfg.DevMode {
			return code, nil
		}
		return "", errors.New("email provider not configured")
	}
	body, _ := json.Marshal(map[string]any{
		"from":    s.cfg.EmailFrom,
		"to":      to,
		"subject": "NextReply 登录验证码：" + code,
		"text":    "你的 NextReply 登录验证码是 " + code + "，10 分钟内有效。\n如果不是你本人操作，请忽略这封邮件。",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.ResendAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("email send failed: %d", resp.StatusCode)
	}
	return "", nil
}

// verifyTurnstile Cloudflare Turnstile 人机验证。未配置时跳过。
func (s *Server) verifyTurnstile(ctx context.Context, token, ip string) (bool, error) {
	if s.cfg.TurnstileSecret == "" {
		return true, nil
	}
	if token == "" {
		return false, nil
	}
	form := url.Values{"secret": {s.cfg.TurnstileSecret}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var data struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false, err
	}
	return data.Success, nil
}
