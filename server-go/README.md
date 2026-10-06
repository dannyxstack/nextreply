# NextReply 服务端（Go）

桌面端的中转服务：鉴权、积分、订阅支付，以及调用 Claude 分析聊天截图并返回 3 条回复。
接口与旧版 Cloudflare Workers 服务（已从仓库删除，见 git 历史）完全一致，桌面端不需要改动。

- 单个静态二进制 + 单个 SQLite 文件，`docker compose up` 即可部署
- 服务只提供 HTTP，不处理证书；HTTPS 交给 Cloudflare Tunnel 或宿主机 nginx（见下文「HTTPS」）
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
├── deploy/nginx.conf.example  宿主机 nginx 反向代理示例（Let's Encrypt）
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
- 服务器需要能访问 `api.anthropic.com`（以及配置了的 Resend、Stripe）

### 2. 配置

```bash
git clone <repo> && cd <repo>/server-go
cp .env.example .env
openssl rand -base64 48      # 生成 TOKEN_SECRET
vim .env
chmod 600 .env               # 里面有密钥
```

至少需要填：`PUBLIC_URL`、`ANTHROPIC_API_KEY`、`TOKEN_SECRET`、`RESEND_API_KEY`、`EMAIL_FROM`。

### 3. 启动

```bash
docker compose up -d --build
docker compose ps                             # server 应为 healthy
curl http://127.0.0.1:8787/v1/health          # {"ok":true,"model":"..."}
```

如果 `server` 一直不是 healthy，通常是配置有误导致服务启动即退出，用 `docker compose logs server` 查看原因，
例如 `TOKEN_SECRET is required`、`RESEND_API_KEY is required when DEV_MODE is off`。

国内服务器构建镜像时拉 Go 依赖较慢，在 `.env` 里加 `GOPROXY=https://goproxy.cn,direct`。

### 4. HTTPS

服务本身只监听 HTTP（默认只绑定本机 `127.0.0.1:8787`），HTTPS 由外部负责，三种场景任选：

| 场景 | 做法 | `.env` |
|---|---|---|
| **生产：Cloudflare（推荐）** | Cloudflare Tunnel 转发到本机服务，证书由 Cloudflare 管理，服务器不用开放任何入站端口 | `CLIENT_IP_HEADER=CF-Connecting-IP` |
| **生产：Let's Encrypt** | 宿主机 nginx + certbot 负责证书，转发到本机服务 | `CLIENT_IP_HEADER=X-Real-IP` |
| **测试机** | 不用 HTTPS，局域网直接访问 | `BIND_ADDR=0.0.0.0`，`PUBLIC_URL=http://<机器 IP>:8787` |

**Cloudflare Tunnel**

1. 域名托管在 Cloudflare。在 Cloudflare Zero Trust 控制台 → Networks → Tunnels 创建一个 Tunnel，
   按提示在服务器上安装 `cloudflared` 并用 token 注册为系统服务
2. 给 Tunnel 添加 Public Hostname：`api.example.com` → `http://localhost:8787`
3. `.env` 中 `PUBLIC_URL=https://api.example.com`、`CLIENT_IP_HEADER=CF-Connecting-IP`，然后 `docker compose up -d`

服务只绑定本机，外部请求只能经过 Tunnel 进来，`CF-Connecting-IP` 无法被伪造。
不要用 Cloudflare 代理（橙色云）+「Flexible」SSL 直连源站：Cloudflare 到服务器这一段是明文。

**nginx + Let's Encrypt**

按 [deploy/nginx.conf.example](deploy/nginx.conf.example) 配置站点，再执行 `certbot --nginx` 申请证书（自动续期）。
示例里的 `proxy_request_buffering off` 不能去掉：nginx 默认会把较大的请求体写进磁盘临时文件，截图会因此落盘。

### 5. 桌面端

在桌面端 Settings 里把服务端地址设为 `PUBLIC_URL` 的值。

### 6. Stripe（可选）

1. Stripe 后台创建两个按月订阅的价格，把 ID 填到 `STRIPE_PRICE_PRO`、`STRIPE_PRICE_PRO_PLUS`
2. 添加 Webhook 端点 `<PUBLIC_URL>/billing/webhook`（必须是 HTTPS），**API 版本选 `2025-03-31.basil` 或更新**，订阅以下事件：
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
| `PUBLIC_URL` | `http://127.0.0.1:8787` | 对外地址，用于生成登录 / 结账链接 |
| `ANTHROPIC_API_KEY` | — | Claude API 密钥 |
| `TOKEN_SECRET` | — | 令牌签名密钥；非开发模式至少 32 个字符 |
| `RESEND_API_KEY` / `EMAIL_FROM` | — | 登录验证码邮件；非开发模式必填 |
| `MODEL` / `EFFORT` | `claude-opus-5-5` / `low` | 付费用户（pro、pro_plus）的模型 |
| `MODEL_FREE` / `EFFORT_FREE` | `claude-sonnet-5` / 同 `EFFORT` | 免费用户（trial 匿名体验、free 已登录未订阅）的模型 |
| `MODEL_<PLAN>` / `EFFORT_<PLAN>` | — | 单个套餐覆盖，`<PLAN>` 为 `TRIAL`、`FREE`、`PRO`、`PRO_PLUS`。可选模型如 `claude-opus-5-5`、`claude-sonnet-5`、`claude-haiku-4-5`；effort 为 `low`…`max`，`none` 表示不传，Haiku 自动不传 |
| `THINKING` | `auto` | `auto` 不传；`adaptive`；`between_tools` |
| `FALLBACKS` | `default` | 拒答时服务端自动换模型；`off` 关闭 |
| `MAX_TOKENS` | `8000` | 包含 thinking |
| `IP_DAILY_QUOTA` | `200` | 同一 IP 每天最多成功回复次数 |
| `REGISTER_PER_IP` | `20` | 同一 IP 每天最多注册新设备数 |
| `TURNSTILE_SITE_KEY` / `TURNSTILE_SECRET` | — | 注册人机验证（可选） |
| `STRIPE_*` | — | 订阅支付（可选） |
| `DEV_MODE` | `false` | 开发模式；**线上必须为 false** |
| `MOCK_AI` | `false` | 开发模式下不调用模型 |
| `CLIENT_IP_HEADER` | 空 | 从哪个请求头取客户端 IP：Cloudflare 用 `CF-Connecting-IP`，nginx 用 `X-Real-IP`，直连留空。**只有在请求一定经过会覆盖该请求头的代理时才能设置**，否则可被伪造 |
| `BIND_ADDR` / `PORT` | `127.0.0.1` / `8787` | 宿主机上的监听地址和端口（只在 compose 中使用） |
| `LISTEN_ADDR` | `:8787` | 监听地址 |
| `DATABASE_PATH` | `./data/nextreply.db` | SQLite 文件；容器中为 `/data/nextreply.db` |

## 设计说明

- **单实例**：积分扣减用进程内互斥锁 + SQLite 事务保证原子性，所以只能跑一个 `server` 容器。
  需要多实例时，把 `credits` 和 `store` 换成 Postgres（行锁）+ Redis（限流计数）。
- **不处理证书**：TLS 在服务外部终止（Cloudflare / nginx），服务保持简单，换部署方式不用改代码。
- **隐私**：截图只在服务内存中处理。前置的 nginx 必须关闭请求缓冲（见上），并关闭访问日志（结账链接的 URL 里有一次性票据）。
  使用 Cloudflare 时截图会经过 Cloudflare 解密后转发，隐私政策中需要写明。
- **后台任务**：每 10 分钟清理过期的验证码、授权码、票据、IP 计数，并退还超过 5 分钟未确认的积分预扣。

## 接口

见 [TECH_DESIGN.md §5.1](../TECH_DESIGN.md)。
