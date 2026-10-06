# NextReply 服务端（Go）

桌面端的中转服务：鉴权、积分、订阅支付，以及调用 Claude 分析聊天截图并返回 3 条回复。
接口与旧版 Cloudflare Workers 服务（已从仓库删除，见 git 历史）完全一致，桌面端不需要改动。

- 单个静态二进制 + 单个 SQLite 文件，`docker compose up` 即可部署
- 截图只在内存中处理；日志和数据库只记录元数据（请求 ID、账户、套餐、状态、模型、耗时、token 数）

## 目录

```text
server-go/
├── cmd/nextreply-server/   入口：serve / migrate / backup / healthcheck
├── internal/
│   ├── app/                HTTP 层：路由、/v1/reply、账号登录、计费、Stripe、页面
│   ├── ai/                 Prompt、结果 Schema（与桌面端协议同步）、Claude 调用
│   ├── credits/            积分：套餐、分桶分配、预扣/确认/退还
│   ├── authn/              设备 token、access token（JWT）、PKCE
│   ├── store/              SQLite 打开、迁移（migrations/*.sql 内嵌进二进制）、备份、清理
│   ├── config/             环境变量
│   └── apierr/             错误码
├── deploy/Caddyfile        HTTPS 反向代理
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

## 本地开发

需要 Go 1.27+。

```bash
cd server-go
go test ./...

# 开发模式：验证码显示在登录页、提供模拟支付；MOCK_AI 不调用模型
DEV_MODE=true MOCK_AI=true TOKEN_SECRET=dev-secret-change-me PUBLIC_URL=http://127.0.0.1:8787 \
  go run ./cmd/nextreply-server
```

服务监听 `http://127.0.0.1:8787`（桌面端开发构建的默认地址），数据库默认写在 `./data/nextreply.db`。
要真正调用模型，去掉 `MOCK_AI` 并设置 `ANTHROPIC_API_KEY`。

## 部署（Docker Compose）

### 1. 准备服务器

- 一台 Linux 服务器，装好 Docker 和 Docker Compose 插件
- 一个域名，A/AAAA 记录指向服务器
- 防火墙放行 80、443（Caddy 用 80 端口完成证书验证）
- 服务器需要能访问 `api.anthropic.com`（以及配置了的 Resend、Stripe）

### 2. 配置

```bash
git clone <repo> && cd <repo>/server-go
cp .env.example .env
openssl rand -base64 48      # 生成 TOKEN_SECRET
vim .env
```

至少需要填：`DOMAIN`、`PUBLIC_URL`、`ANTHROPIC_API_KEY`、`TOKEN_SECRET`、`RESEND_API_KEY`、`EMAIL_FROM`。
`.env` 里有密钥，权限设为只有自己可读：`chmod 600 .env`。

### 3. 启动

```bash
docker compose up -d --build
docker compose ps                         # server 应为 healthy
curl https://<DOMAIN>/v1/health           # {"ok":true,"model":"..."}
```

国内服务器构建镜像时拉 Go 依赖较慢，在 `.env` 里加 `GOPROXY=https://goproxy.cn,direct`。

### 4. 桌面端

在桌面端 Settings 里把服务端地址设为 `https://<DOMAIN>`。

### 5. Stripe（可选）

1. Stripe 后台创建两个按月订阅的价格，把 ID 填到 `STRIPE_PRICE_PRO`、`STRIPE_PRICE_PRO_PLUS`
2. 添加 Webhook 端点 `https://<DOMAIN>/billing/webhook`，**API 版本选 `2025-03-31.basil` 或更新**，订阅以下事件：
   `checkout.session.completed`、`invoice.paid`、`customer.subscription.updated`、`customer.subscription.deleted`、`charge.refunded`、`charge.dispute.created`
3. 把签名密钥填到 `STRIPE_WEBHOOK_SECRET`，`STRIPE_SECRET_KEY` 填 API 密钥，然后 `docker compose up -d`
4. 在 Stripe 后台开启客户门户（Customer portal），用户才能自助管理订阅

处理失败的 webhook 会返回 500，Stripe 会自动重试；所有处理都是幂等的（同一张发票只发一次积分）。

## 运维

| 操作 | 命令 |
|---|---|
| 查看日志 | `docker compose logs -f server`（JSON，一行一个事件） |
| 升级 | `git pull && docker compose up -d --build`（启动时自动执行数据库迁移） |
| 重启 | `docker compose restart server` |
| 备份 | 见下 |

### 备份

数据库在命名卷 `data` 里。服务运行中可以直接生成一致的快照：

```bash
mkdir -p backups && sudo chown 65532:65532 backups    # 容器内是非 root 用户 65532
docker compose exec server /nextreply-server backup /backups/nextreply-$(date +%F).db
```

建议用 cron 每天执行一次，并把 `backups/` 同步到其他机器或对象存储。
恢复：停服务，把快照复制到卷里的 `/data/nextreply.db`（同时删掉旧的 `-wal`、`-shm` 文件），再启动。

### 从 Cloudflare Workers 版迁移

- `.env` 里沿用旧的 `TOKEN_SECRET`：已经发出的设备 token 继续有效，客户端无感
- D1 / Durable Object 中的账号和积分数据不会自动迁移；用户重新登录后按邮箱找回账号，但旧积分需要另写脚本导入
  （还在测试阶段、没有真实用户时，直接切换即可）

## 配置项

| 变量 | 默认 | 说明 |
|---|---|---|
| `DOMAIN` | — | Caddy 申请证书用的域名（只在 compose 中使用） |
| `PUBLIC_URL` | `http://127.0.0.1:8787` | 对外地址，用于生成登录 / 结账链接 |
| `ANTHROPIC_API_KEY` | — | Claude API 密钥 |
| `TOKEN_SECRET` | — | 令牌签名密钥；非开发模式至少 32 个字符 |
| `RESEND_API_KEY` / `EMAIL_FROM` | — | 登录验证码邮件；非开发模式必填 |
| `MODEL` | `claude-opus-5-5` | 可换成 `claude-sonnet-5-5`、`claude-haiku-4-5` |
| `EFFORT` | `low` | `low`…`max`；`none` 表示不传（Haiku 4.5） |
| `THINKING` | `auto` | `auto` 不传；`adaptive`；`between_tools` |
| `FALLBACKS` | `default` | 拒答时服务端自动换模型；`off` 关闭 |
| `MAX_TOKENS` | `8000` | 包含 thinking |
| `IP_DAILY_QUOTA` | `200` | 同一 IP 每天最多成功回复次数 |
| `REGISTER_PER_IP` | `20` | 同一 IP 每天最多注册新设备数 |
| `TURNSTILE_SITE_KEY` / `TURNSTILE_SECRET` | — | 注册人机验证（可选） |
| `STRIPE_*` | — | 订阅支付（可选） |
| `DEV_MODE` | `false` | 开发模式；**线上必须为 false** |
| `MOCK_AI` | `false` | 开发模式下不调用模型 |
| `CLIENT_IP_HEADER` | 空 | 从哪个请求头取客户端 IP；compose 中设为 `X-Real-IP`。**只有在代理会覆盖该请求头时才能设置**，否则可被伪造 |
| `LISTEN_ADDR` | `:8787` | 监听地址 |
| `DATABASE_PATH` | `./data/nextreply.db` | SQLite 文件；容器中为 `/data/nextreply.db` |

## 设计说明

- **单实例**：积分扣减用进程内互斥锁 + SQLite 事务保证原子性，所以只能跑一个 `server` 容器。
  需要多实例时，把 `credits` 和 `store` 换成 Postgres（行锁）+ Redis（限流计数）。
- **隐私**：Caddy 流式转发请求体，不会把截图写入磁盘临时文件；如果改用 Nginx，必须设置
  `proxy_request_buffering off` 或足够大的 `client_body_buffer_size`，否则大请求体会落盘。
  Caddy 未开启访问日志（结账链接的 URL 里有一次性票据）。
- **后台任务**：每 10 分钟清理过期的验证码、授权码、票据、IP 计数，并退还超过 5 分钟未确认的积分预扣。

## 接口

见 [TECH_DESIGN.md §5.1](../TECH_DESIGN.md)。
