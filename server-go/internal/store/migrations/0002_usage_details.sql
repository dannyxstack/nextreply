-- 管理后台需要的用量明细（TECH_DESIGN §5.8）。仍然只有元数据，不含任何截图或聊天内容。
-- 被拒绝 / 出错的请求也记录：status 为 rejected:<原因> 或 error:<错误码>，credits = 0。

ALTER TABLE usage_events ADD COLUMN device_id TEXT;
ALTER TABLE usage_events ADD COLUMN ip TEXT;              -- 只用于风控排查，保留 30 天后清空
ALTER TABLE usage_events ADD COLUMN tokens_in INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_events ADD COLUMN tokens_out INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_events ADD COLUMN cache_read INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_events ADD COLUMN cache_write INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_events ADD COLUMN cost_micros INTEGER NOT NULL DEFAULT 0;  -- 估算成本，百万分之一美元
ALTER TABLE usage_events ADD COLUMN error_code TEXT;

CREATE INDEX usage_time ON usage_events(created_at);
CREATE INDEX usage_ip_time ON usage_events(ip, created_at);
CREATE INDEX devices_first_ip ON devices(first_ip);
