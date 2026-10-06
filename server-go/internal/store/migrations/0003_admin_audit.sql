-- 管理后台操作审计（TECH_DESIGN §5.8 阶段 2）：每一次修改数据的操作都记一条，不可在后台删除。
CREATE TABLE admin_audit (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at  INTEGER NOT NULL,
  action      TEXT NOT NULL,     -- grant_credits | suspend_user | restore_user | revoke_sessions | unbind_device
  target      TEXT NOT NULL,     -- u:<user_id> | d:<device_id>
  detail      TEXT NOT NULL,     -- JSON：操作参数与原因
  actor       TEXT NOT NULL,     -- 操作者：Cloudflare Access 邮箱，否则为 admin
  ip          TEXT
);
CREATE INDEX admin_audit_target ON admin_audit(target, created_at);
CREATE INDEX admin_audit_time ON admin_audit(created_at);
