-- 运行时配置（TECH_DESIGN §5.8 阶段 3）：管理后台修改的值。没有记录的项使用环境变量 / 代码里的默认值。
CREATE TABLE settings (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  updated_by  TEXT NOT NULL
);
