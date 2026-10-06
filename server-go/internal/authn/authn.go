// Package authn 包含令牌和密码学小工具：设备 token、access token（JWT HS256）、PKCE、随机串。
package authn

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

func IsValidDeviceID(id string) bool { return deviceIDRe.MatchString(id) }

func hmacSHA256(secret, msg []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return m.Sum(nil)
}

// ---------- 设备 token ----------
// 无状态：base64url(device_id) + "." + base64url(HMAC_SHA256(secret, device_id))。
// 与旧版 Workers 服务格式一致，使用同一个 TOKEN_SECRET 时已发出的 token 继续有效。

func IssueDeviceToken(secret, deviceID string) string {
	sig := hmacSHA256([]byte(secret), []byte(deviceID))
	return b64.EncodeToString([]byte(deviceID)) + "." + b64.EncodeToString(sig)
}

// VerifyDeviceToken 校验通过返回 device_id。
func VerifyDeviceToken(secret, token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", false
	}
	idBytes, err := b64.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sig, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	id := string(idBytes)
	if !IsValidDeviceID(id) {
		return "", false
	}
	if !hmac.Equal(sig, hmacSHA256([]byte(secret), idBytes)) {
		return "", false
	}
	return id, true
}

// DeviceHash 日志里只记录设备 ID 的短哈希。
func DeviceHash(deviceID string) string {
	sum := sha256.Sum256([]byte(deviceID))
	return hex.EncodeToString(sum[:4])
}

// ---------- access token（JWT HS256） ----------

const (
	AccessTTL = 15 * time.Minute
	issuer    = "nextreply"
)

type AccessClaims struct {
	Sub string `json:"sub"` // user_id
	Did string `json:"did"` // device_id
	Iss string `json:"iss"`
	Typ string `json:"typ"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var jwtHeader = b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

func SignAccess(secret, userID, deviceID string, now time.Time) string {
	claims := AccessClaims{Sub: userID, Did: deviceID, Iss: issuer, Typ: "access", Iat: now.Unix(), Exp: now.Add(AccessTTL).Unix()}
	payload, _ := json.Marshal(claims)
	signing := jwtHeader + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(hmacSHA256([]byte(secret), []byte(signing)))
}

func VerifyAccess(secret, token string, now time.Time) (*AccessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil || header.Alg != "HS256" {
		return nil, errors.New("bad header")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, hmacSHA256([]byte(secret), []byte(parts[0]+"."+parts[1]))) {
		return nil, errors.New("bad signature")
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("bad payload")
	}
	var c AccessClaims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, errors.New("bad payload")
	}
	if c.Iss != issuer || c.Typ != "access" || c.Sub == "" || c.Did == "" {
		return nil, errors.New("bad claims")
	}
	if now.Unix() >= c.Exp {
		return nil, errors.New("expired")
	}
	return &c, nil
}

// ---------- 随机串与哈希 ----------

// RandomToken 高熵随机令牌（refresh token、授权码、网页票据）
func RandomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return b64.EncodeToString(buf)
}

func RandomDigits(n int) string {
	buf := make([]byte, 4*n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteByte(byte('0' + binary.BigEndian.Uint32(buf[4*i:])%10))
	}
	return sb.String()
}

func NewUUID() string {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic(err)
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
}

func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// PKCEChallenge S256：code_challenge = base64url(sha256(code_verifier))
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(sum[:])
}

// SafeEqual 常量时间比较，避免计时攻击
func SafeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
