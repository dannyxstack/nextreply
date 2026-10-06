package authn

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const (
	secret = "test-secret-0123456789"
	device = "3f1c2b7e-9d4a-4c1e-8b2a-6f5d4e3c2b1a"
)

func TestDeviceTokenRoundTrip(t *testing.T) {
	id, ok := VerifyDeviceToken(secret, IssueDeviceToken(secret, device))
	if !ok || id != device {
		t.Fatalf("got %q %v", id, ok)
	}
}

func TestDeviceTokenRejects(t *testing.T) {
	if _, ok := VerifyDeviceToken(secret, IssueDeviceToken("other-secret-abcdefgh", device)); ok {
		t.Fatal("accepted token signed with another secret")
	}
	sig := strings.Split(IssueDeviceToken(secret, device), ".")[1]
	forged := base64.RawURLEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaa")) + "." + sig
	if _, ok := VerifyDeviceToken(secret, forged); ok {
		t.Fatal("accepted tampered device id")
	}
	for _, bad := range []string{"not-a-token", "a.b.c", ""} {
		if _, ok := VerifyDeviceToken(secret, bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// 与旧版 Workers 服务签发的 token 兼容（同一密钥下格式一致）
func TestDeviceTokenFormat(t *testing.T) {
	tok := IssueDeviceToken(secret, device)
	if strings.Contains(tok, "=") || strings.Count(tok, ".") != 1 {
		t.Fatalf("unexpected format %q", tok)
	}
}

func TestPKCERFC7636Example(t *testing.T) {
	if got := PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
}

func TestSafeEqual(t *testing.T) {
	if !SafeEqual("abc", "abc") || SafeEqual("abc", "abd") || SafeEqual("abc", "abcd") {
		t.Fatal("SafeEqual wrong")
	}
}

func TestAccessToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok := SignAccess(secret, "user-1", device, now)
	c, err := VerifyAccess(secret, tok, now.Add(time.Minute))
	if err != nil || c.Sub != "user-1" || c.Did != device {
		t.Fatalf("verify: %v %+v", err, c)
	}
	if _, err := VerifyAccess(secret, tok, now.Add(AccessTTL)); err == nil {
		t.Fatal("accepted expired token")
	}
	if _, err := VerifyAccess("another-secret-xxxxxxxx", tok, now); err == nil {
		t.Fatal("accepted wrong secret")
	}
	parts := strings.Split(tok, ".")
	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	if _, err := VerifyAccess(secret, noneHeader+"."+parts[1]+".", now); err == nil {
		t.Fatal("accepted alg none")
	}
}

func TestRandomDigits(t *testing.T) {
	d := RandomDigits(6)
	if len(d) != 6 || strings.Trim(d, "0123456789") != "" {
		t.Fatal(d)
	}
}
