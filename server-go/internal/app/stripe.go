package app

// Stripe 的最小 REST 客户端：只用到创建结账会话、客户门户会话、查询订阅和 webhook 验签。
// 字段按 API 版本 2025-03-31.basil 及之后的结构读取（周期在 subscription item 上，发票通过 parent 关联订阅），
// 同时兼容旧版本的字段位置。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	stripeAPI        = "https://api.stripe.com"
	stripeAPIVersion = "2025-03-31.basil"
	// webhook 时间戳容差，防止重放
	stripeTolerance = 5 * time.Minute
)

// 测试时替换成假的 Stripe 服务地址
var stripeBaseForTest = stripeAPI

type stripeClient struct {
	key  string
	http *http.Client
	base string
}

func (s *Server) stripe() *stripeClient {
	if s.cfg.StripeSecretKey == "" {
		return nil
	}
	return &stripeClient{key: s.cfg.StripeSecretKey, http: s.http, base: stripeBaseForTest}
}

func (c *stripeClient) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.key, "")
	req.Header.Set("Stripe-Version", stripeAPIVersion)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("stripe %s %s: %d %s %s", method, path, resp.StatusCode, e.Error.Type, e.Error.Message)
	}
	return json.Unmarshal(data, out)
}

type stripeSession struct {
	URL string `json:"url"`
}

func (c *stripeClient) createCheckout(ctx context.Context, form url.Values) (*stripeSession, error) {
	var out stripeSession
	return &out, c.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, &out)
}

func (c *stripeClient) createPortal(ctx context.Context, customer, returnURL string) (*stripeSession, error) {
	var out stripeSession
	return &out, c.do(ctx, http.MethodPost, "/v1/billing_portal/sessions", url.Values{"customer": {customer}, "return_url": {returnURL}}, &out)
}

func (c *stripeClient) getSubscription(ctx context.Context, id string) (*stripeSubscription, error) {
	var out stripeSubscription
	return &out, c.do(ctx, http.MethodGet, "/v1/subscriptions/"+url.PathEscape(id), nil, &out)
}

// stripeRef 兼容字段是 ID 字符串或展开后的对象两种情况。
type stripeRef string

func (r *stripeRef) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*r = stripeRef(s)
		return nil
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*r = stripeRef(obj.ID)
	return nil
}

type stripeSubscription struct {
	ID                 string            `json:"id"`
	Status             string            `json:"status"`
	Customer           stripeRef         `json:"customer"`
	CancelAtPeriodEnd  bool              `json:"cancel_at_period_end"`
	Metadata           map[string]string `json:"metadata"`
	CurrentPeriodStart int64             `json:"current_period_start"` // 旧版 API
	CurrentPeriodEnd   int64             `json:"current_period_end"`   // 旧版 API
	Items              struct {
		Data []struct {
			CurrentPeriodStart int64 `json:"current_period_start"`
			CurrentPeriodEnd   int64 `json:"current_period_end"`
			Price              struct {
				ID string `json:"id"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

func (s *stripeSubscription) priceID() string {
	if len(s.Items.Data) > 0 {
		return s.Items.Data[0].Price.ID
	}
	return ""
}

func (s *stripeSubscription) period() (start, end int64) {
	if len(s.Items.Data) > 0 && s.Items.Data[0].CurrentPeriodEnd > 0 {
		return s.Items.Data[0].CurrentPeriodStart, s.Items.Data[0].CurrentPeriodEnd
	}
	return s.CurrentPeriodStart, s.CurrentPeriodEnd
}

type stripeInvoice struct {
	ID           string    `json:"id"`
	Subscription stripeRef `json:"subscription"` // 旧版 API
	Parent       *struct {
		SubscriptionDetails *struct {
			Subscription stripeRef `json:"subscription"`
		} `json:"subscription_details"`
	} `json:"parent"`
	Lines struct {
		Data []struct {
			Period struct {
				End int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
}

func (inv *stripeInvoice) subscriptionID() string {
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil && inv.Parent.SubscriptionDetails.Subscription != "" {
		return string(inv.Parent.SubscriptionDetails.Subscription)
	}
	return string(inv.Subscription)
}

type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// verifyStripeSignature 校验 Stripe-Signature 头：t=<时间戳>,v1=<HMAC-SHA256(secret, "t.payload")>。
func verifyStripeSignature(payload []byte, header, secret string, now time.Time) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return errors.New("malformed signature header")
	}
	if d := now.Sub(time.Unix(t, 0)); d > stripeTolerance || d < -stripeTolerance {
		return errors.New("timestamp outside tolerance")
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(payload)
	expected := m.Sum(nil)
	for _, sig := range sigs {
		if b, err := hex.DecodeString(sig); err == nil && hmac.Equal(b, expected) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}
