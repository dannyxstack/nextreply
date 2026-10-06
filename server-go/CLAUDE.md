# NextReply 中转服务 — 开发说明（Linux）

本目录是 NextReply 的服务端：Go + SQLite + `anthropic-sdk-go`。
仓库级规范见 [../CLAUDE.md](../CLAUDE.md)，架构与接口以 [../TECH_DESIGN.md](../TECH_DESIGN.md) §5–6 为准，部署见 [README.md](README.md)。

## 端口

| 端口 | 归属 | 说明 |
|---|---|---|
| **8787** | 本服务 | 默认监听 `127.0.0.1:8787`，桌面端设置页的"服务端地址"填它（或隧道地址） |
| **8788** | 管理后台 | 设置 `ADMIN_TOKEN` 后启动，只监听本机；从 Windows 访问用 `ssh -N -L 8789:127.0.0.1:8788 …` 后打开 `http://127.0.0.1:8789` |
| 1420 | 桌面端开发时的 vite | 与服务端无关 |
| 随机 | 桌面端登录回调 | 浏览器登录后跳回 Windows 本机，与服务器无关 |

## 本地运行

```bash
# 开发模式：验证码显示在登录页、提供模拟支付；MOCK_AI 不调用模型、不花钱
DEV_MODE=true MOCK_AI=true TOKEN_SECRET=dev-secret PUBLIC_URL=http://127.0.0.1:8787 \
  ADMIN_TOKEN=local-admin-token-0123456789 go run ./cmd/nextreply-server
# 管理后台：http://127.0.0.1:8788（用户名 admin，密码为 ADMIN_TOKEN）

# 真实调用模型：去掉 MOCK_AI，加上 ANTHROPIC_API_KEY
```

数据库默认在 `./data/nextreply.db`，删掉即可重置。启动时自动执行迁移。
长期运行放在 tmux 里：`tmux new -s nextreply '...'`；或者直接用 `docker compose up -d`（见 README）。

## 常用命令

```bash
go vet ./... && go test ./...                       # 单元 + HTTP 集成测试
node scripts/e2e-account.mjs                        # 账户系统端到端测试，需先以 DEV_MODE + MOCK_AI 启动
go run ./cmd/nextreply-server reset-limits          # 清空按 IP 的每日限额计数（e2e 跑完后执行）
go run ./cmd/nextreply-server backup <文件>          # 数据库快照
sqlite3 -readonly data/nextreply.db                 # 直接查数据（只读）
```

Docker 中执行子命令：`docker compose exec server /nextreply-server reset-limits`。

## 从 Windows 桌面端连接

**开发模式（`DEV_MODE=true`）不能开放到局域网或公网**：验证码直接显示在页面上、提供模拟支付，任何人都能登录任意邮箱的账号、免费开通订阅。

用 SSH 隧道。在 Windows 上保持运行（Windows 本机 8787 被占用时用 8788）：

```bash
ssh -N -L 8788:127.0.0.1:8787 <用户>@<服务器>
```

- 服务端：`PUBLIC_URL=http://127.0.0.1:8788`（登录页、结账链接要在 Windows 的浏览器里打开）
- 桌面端设置页"服务端地址"：`http://127.0.0.1:8788`

`DEV_MODE=false`（需配置 Resend）时才可以用 `BIND_ADDR=0.0.0.0` 让局域网直接访问；公网一律走 HTTPS（Cloudflare Tunnel / nginx）。

## 代码结构

| 路径 | 内容 |
|---|---|
| `cmd/nextreply-server/` | 入口与子命令：serve / migrate / backup / reset-limits / healthcheck |
| `internal/app/reply.go` | `/v1/reply`：IP 限额 → 预扣 → 按套餐选模型调用 → 确认 / 退还 |
| `internal/app/account.go` · `identity.go` | 设备注册、邮箱验证码登录（PKCE）、令牌、`/v1/me`、调用方识别 |
| `internal/app/billing.go` · `stripe.go` | 结账链接、Stripe webhook、开发模式模拟支付 |
| `internal/app/pages.go` | 服务端渲染的登录页、结果页 |
| `internal/ai/` | Prompt、结构化输出 schema（协议）、Claude 调用、成本估算价目表（`pricing.go`） |
| `internal/admin/` | 管理后台：查询（`queries.go`）、页面模板（`templates/`）；监听 `ADMIN_ADDR`，Basic 认证 |
| `internal/credits/` | 套餐数值（`plans.go`）、扣费纯函数（`allocate.go`）、积分账户（`service.go`） |
| `internal/store/` | SQLite、迁移（`migrations/`）、备份、清理 |
| `internal/config/` | 环境变量（运行配置的默认值） |
| `internal/settings/` | 运行配置：可在后台修改的项、校验、覆盖值、成本上限；业务代码读模型 / 上限 / 限额都走这里 |
| `scripts/` | 端到端测试 |

## 开发约定

- **隐私**：日志、`usage_events` 只记元数据；不记录截图、聊天内容、回复内容、邮箱全文。
- **扣费**：只在 `status=ok` 时 `Commit`，其余一律 `Release`；退还 / 确认用不随请求取消的 context。新增计费路径必须带幂等键。
- **余额只通过 `credits.Service` 修改**：它用进程内互斥锁 + 事务保证原子性，所以服务只能单实例运行。
- **迁移**：新增表 / 字段时在 `internal/store/migrations/` 加新的编号文件，不要改已有文件。
- **协议同步**：改 `/v1/reply`、`/v1/me` 的响应格式时，同时更新桌面端 `apps/desktop/src-tauri/src/ai/types.rs`、`account/`、`apps/desktop/src/shared/ipc.ts`。
- **改动后**：`gofmt`、`go vet ./... && go test ./...`；动了账户 / 积分 / 支付逻辑再以 DEV_MODE + MOCK_AI 启动跑 `node scripts/e2e-account.mjs`（跑完 `reset-limits`）。
- Windows 上同时有人在改桌面端：改之前 `git pull`，改完及时提交推送，不要两边同时改同一个文件。
