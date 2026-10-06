# NextReply 中转服务 — 开发说明（Linux）

本目录是 NextReply 的服务端：Cloudflare Workers + Hono + `@anthropic-ai/sdk`。
仓库级规范见 [../CLAUDE.md](../CLAUDE.md)，架构与接口以 [../TECH_DESIGN.md](../TECH_DESIGN.md) §5–6 为准。

## 端口

| 端口 | 归属 | 说明 |
|---|---|---|
| **8787** | 本服务 | `npm run dev` 监听的地址 `127.0.0.1:8787`，桌面端设置页的"服务端地址"填它 |
| 1420 | 桌面端开发时的 vite | 与服务端无关 |
| 随机 | 桌面端登录回调 | 浏览器登录后跳回 Windows 本机，与服务器无关 |

## 首次配置

```bash
# Node 20+（Windows 开发机用的是 24）
nvm install 24 && nvm use 24

cd ~/nextreply/server
npm ci
cp .dev.vars.example .dev.vars   # 然后编辑，见下表
npm run db:migrate               # 初始化本地 D1（.wrangler/state/）
```

`.dev.vars`（**不进 git**，每台机器各自一份）：

| 变量 | 说明 |
|---|---|
| `ANTHROPIC_API_KEY` | Anthropic Console 的 API Key（与 Claude Code 登录用的订阅账号不是一回事）。建议这台服务器单独建一个 Key |
| `TOKEN_SECRET` | `openssl rand -hex 32` 生成；更换后已签发的设备 token / 登录令牌全部失效 |
| `DEV_MODE` | `true`：验证码显示在登录页上、提供模拟支付。**只能在本机或隧道后使用** |
| `PUBLIC_URL` | 登录页、结账链接的前缀，必须是**用户浏览器能打开的地址**。走 SSH 隧道时填隧道在 Windows 上的地址，如 `http://127.0.0.1:8788` |
| `MOCK_AI` | 可选。`true` 时不调用模型、返回固定结果（只在 `DEV_MODE` 下生效），用于测试扣费逻辑 |
| `RESEND_API_KEY` / `EMAIL_FROM` | 可选。配置后发真实邮件验证码 |
| `STRIPE_*` | 可选。配置后走真实 Stripe 测试模式，替代模拟支付 |

修改 `.dev.vars` 后必须**重启** `wrangler dev`，运行中不会重新读取。

## 常用命令

```bash
npm run dev            # 启动（真实 AI）
npm run dev:mock       # 启动（MOCK_AI=true，不花钱）
npm run typecheck
npm test               # 单元测试（vitest）
npm run test:e2e       # 账户系统端到端测试，需先 npm run dev:mock
npm run reset:limits   # 清空本地按 IP 的每日限额计数
npm run db:migrate     # 应用 migrations/ 到本地 D1
```

长期运行放在 tmux 里，断开 SSH 不会停：

```bash
tmux new -s nextreply 'npm run dev'    # 之后 tmux attach -t nextreply 查看日志
```

## 从 Windows 桌面端连接

**不要监听 `0.0.0.0` 把端口开放到公网**：开发模式会把验证码显示在页面上、提供模拟支付，任何人都能登录任意邮箱的账号、免费开通订阅。

用 SSH 隧道。在 Windows 上保持运行（Windows 本机 8787 被占用时用 8788）：

```bash
ssh -N -L 8788:127.0.0.1:8787 <用户>@<服务器>
```

- 本机 `.dev.vars`：`PUBLIC_URL=http://127.0.0.1:8788`
- 桌面端设置页"服务端地址"：`http://127.0.0.1:8788`

这台服务器的数据库和 Windows 本机的测试服务端互不相通。桌面端切换过来后旧的登录状态会失效，重新登录即可。

需要公网访问时：`DEV_MODE=false`、配置 Resend、走 HTTPS（Cloudflare Tunnel / nginx），或直接 `wrangler deploy` 到 Workers 测试环境。

## 代码结构

| 路径 | 内容 |
|---|---|
| `src/index.ts` | 路由组装、`/v1/reply`（预扣 → 调模型 → 确认 / 退还） |
| `src/ai/` | Prompt、结构化输出 schema、Claude 调用 |
| `src/account/` | 设备注册、邮箱验证码登录（PKCE）、令牌、`/v1/me`、调用方识别 |
| `src/credits/` | 套餐数值（`plans.ts`）、扣费纯函数（`allocate.ts`）、积分 Durable Object（`do.ts`） |
| `src/billing/` | 结账链接、Stripe webhook、开发模式模拟支付 |
| `src/pages/` | 服务端渲染的登录页、结果页 |
| `src/quota.ts` | 按 IP 的 KV 软限制 |
| `migrations/` | D1 表结构；新增表 / 字段时加新的编号文件，不要改已有文件 |
| `scripts/` | 端到端测试、清理本地限额 |

## 开发约定

- **隐私**：日志、`usage_events` 只记元数据；不记录截图、聊天内容、回复内容、邮箱全文。
- **扣费**：只在 `status=ok` 时 `commit`，其余一律 `release`。新增计费路径必须带幂等键。
- **余额只在 Durable Object 里改**，不要用 KV 或 D1 做余额（没有原子性）。
- **协议同步**：改 `/v1/reply`、`/v1/me` 的响应格式时，同时更新桌面端 `apps/desktop/src-tauri/src/ai/types.rs`、`account/`、`apps/desktop/src/shared/ipc.ts`。
- **改动后**：`npm run typecheck && npm test`；动了账户 / 积分 / 支付逻辑再跑 `npm run dev:mock` + `npm run test:e2e`（跑完 `npm run reset:limits`）。
- Windows 上同时有人在改桌面端：改之前 `git pull`，改完及时提交推送，不要两边同时改同一个文件。
