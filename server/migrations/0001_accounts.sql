-- 账号、设备、登录、订阅、用量元数据。积分余额和流水在每个用户的 Durable Object 里（见 src/credits/do.ts）。

CREATE TABLE users (
  id          TEXT PRIMARY KEY,
  email       TEXT NOT NULL,             -- 用于发信的原始邮箱
  email_norm  TEXT NOT NULL UNIQUE,      -- 规范化后的邮箱，用于唯一性判断（防止 gmail 点号 / + 后缀重复注册）
  status      TEXT NOT NULL DEFAULT 'active',  -- active | suspended
  created_at  INTEGER NOT NULL
);

CREATE TABLE devices (
  id                    TEXT PRIMARY KEY,
  user_id               TEXT REFERENCES users(id),
  hw_hash               TEXT,           -- 加盐后的硬件特征哈希，用于识别重装后换了 device_id 的同一台机器
  first_ip              TEXT,
  trial_granted         INTEGER NOT NULL DEFAULT 0,
  signup_bonus_claimed  INTEGER NOT NULL DEFAULT 0,
  created_at            INTEGER NOT NULL,
  last_seen             INTEGER NOT NULL
);
CREATE INDEX devices_user ON devices(user_id);
CREATE INDEX devices_hw ON devices(hw_hash);

-- 邮箱验证码（只存哈希）
CREATE TABLE otp_codes (
  email_norm  TEXT PRIMARY KEY,
  code_hash   TEXT NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0,
  expires_at  INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
);

-- 浏览器登录完成后发给桌面端的一次性授权码（PKCE）
CREATE TABLE auth_codes (
  code_hash       TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL,
  device_id       TEXT NOT NULL,
  code_challenge  TEXT NOT NULL,
  expires_at      INTEGER NOT NULL,
  used            INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE refresh_tokens (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  device_id   TEXT NOT NULL,
  family_id   TEXT NOT NULL,          -- 同一次登录产生的令牌链；检测到重用时整条链作废
  token_hash  TEXT NOT NULL UNIQUE,
  expires_at  INTEGER NOT NULL,
  revoked_at  INTEGER,
  created_at  INTEGER NOT NULL
);
CREATE INDEX refresh_family ON refresh_tokens(family_id);
CREATE INDEX refresh_user_device ON refresh_tokens(user_id, device_id);

CREATE TABLE subscriptions (
  user_id               TEXT PRIMARY KEY REFERENCES users(id),
  plan                  TEXT NOT NULL,     -- pro | pro_plus
  status                TEXT NOT NULL,     -- active | past_due | canceled
  provider              TEXT NOT NULL,     -- stripe | dev
  customer_id           TEXT,
  subscription_id       TEXT UNIQUE,
  current_period_start  INTEGER,
  current_period_end    INTEGER,
  cancel_at_period_end  INTEGER NOT NULL DEFAULT 0,
  updated_at            INTEGER NOT NULL
);
CREATE INDEX subscriptions_customer ON subscriptions(customer_id);

-- 网页端（结账页）用的一次性票据，由已登录的桌面端申请
CREATE TABLE web_tickets (
  ticket_hash  TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL,
  purpose      TEXT NOT NULL,
  expires_at   INTEGER NOT NULL,
  used         INTEGER NOT NULL DEFAULT 0
);

-- 用量元数据：不含任何截图或聊天内容
CREATE TABLE usage_events (
  request_id  TEXT PRIMARY KEY,
  owner       TEXT NOT NULL,     -- u:<user_id> | d:<device_id>
  plan        TEXT NOT NULL,
  status      TEXT NOT NULL,
  credits     INTEGER NOT NULL,
  model       TEXT,
  latency_ms  INTEGER,
  created_at  INTEGER NOT NULL
);
CREATE INDEX usage_owner_time ON usage_events(owner, created_at);
