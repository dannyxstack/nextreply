-- 网页账户（TECH_DESIGN §10.3）。

-- 网页登录会话：cookie 里放随机令牌，这里只存哈希
CREATE TABLE web_sessions (
  token_hash  TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);
CREATE INDEX web_sessions_user ON web_sessions(user_id);

-- 删除过的账号：只保存加盐的规范化邮箱哈希，用于防止"删号重注册"反复领取注册赠送
CREATE TABLE deleted_emails (
  email_hash  TEXT PRIMARY KEY,
  deleted_at  INTEGER NOT NULL
);
