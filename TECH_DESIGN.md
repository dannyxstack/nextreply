# NextReply 技术方案（MVP）

> 对应需求：[REQUIREMENT.md](REQUIREMENT.md)
> 目标平台：Windows 10/11 优先，架构为 macOS 预留。

---

## 1. 技术选型

| 层 | 选型 | 说明 |
|---|---|---|
| 桌面壳 | **Tauri 2** | 体积小、常驻内存低，Rust 侧直接调用系统截图 / 窗口 API |
| 核心逻辑 | **Rust** | 快捷键、截图、窗口管理、状态机、网络请求、剪贴板 |
| UI | **React 18 + TypeScript + Vite** | 三个轻量页面：框选（selector）、回复卡片（overlay）、设置（settings） |
| 中转服务 | **Go（标准库 net/http）** | 单个静态二进制，Linux 上用 Docker Compose 部署；用官方 `anthropic-sdk-go` 调用 Claude |
| 存储 | **SQLite**（`modernc.org/sqlite`，纯 Go） | 账号、积分、订阅、限额计数都在一个文件里；单实例部署 |
| 反向代理 | **Caddy** | 自动 HTTPS；流式转发请求体，截图不落盘 |
| 模型 | 默认 `claude-opus-5-5`（effort `low`），可通过环境变量切换 | 见 §6 |

**原则**
- 客户端不持有任何模型厂商的 API Key；Prompt 和模型选择都放在服务端。
- 截图只在内存中存在；客户端和服务端都不落盘，也不记录内容。
- 平台差异放在 `platform/` 和 `capture/` 模块里，业务逻辑与平台无关。

---

## 2. 仓库结构

```text
cc-nextreply/
├── REQUIREMENT.md
├── TECH_DESIGN.md
├── CLAUDE.md                     # 给 AI coding agent 的工程规范
├── apps/desktop/                 # Tauri 桌面端
│   ├── index.html                # settings 页
│   ├── selector.html             # 框选页（每个显示器一个窗口）
│   ├── overlay.html              # 回复卡片页
│   ├── src/
│   │   ├── selector/  overlay/  settings/
│   │   └── shared/               # 协议类型、IPC 封装
│   └── src-tauri/
│       ├── tauri.conf.json
│       ├── capabilities/
│       └── src/
│           ├── main.rs / lib.rs  # 启动、插件、托盘装配
│           ├── flow.rs           # 状态机（唯一的流程入口和出口）
│           ├── tray.rs
│           ├── hotkey.rs
│           ├── capture/          # mod.rs(trait) · frames.rs(内存帧 + frame:// 协议) · crop.rs
│           ├── overlay/          # positioning.rs(纯函数 + 单测) · window.rs
│           ├── ai/               # provider.rs(trait) · proxy.rs(中转客户端) · types.rs
│           ├── clipboard.rs
│           ├── storage/          # settings.rs · secrets.rs(keyring)
│           ├── telemetry.rs
│           └── platform/         # windows.rs · macos.rs(预留)
└── server-go/                    # 中转服务（Go，部署说明见 server-go/README.md）
    ├── cmd/nextreply-server/     # 入口：serve / migrate / backup / healthcheck
    ├── internal/
    │   ├── app/                  # HTTP：路由、/v1/reply、账号登录、计费、Stripe、页面
    │   ├── ai/                   # prompt.go · schema.go（协议） · anthropic.go（Claude 调用）
    │   ├── credits/              # 套餐、分桶分配、预扣/确认/退还
    │   ├── authn/                # 设备 token、JWT、PKCE
    │   └── store/                # SQLite、迁移（migrations/*.sql）、备份
    ├── Dockerfile · docker-compose.yml · deploy/Caddyfile
```

---

## 3. 整体架构

```text
┌────────────────────────────── Desktop (Tauri) ──────────────────────────────┐
│ Rust Core                                                                    │
│  hotkey ─► flow(状态机) ─► capture ─► ai/proxy ─► overlay ─► clipboard      │
│             tray · storage · telemetry · platform                            │
│     ▲ IPC(invoke/event)       ▲ frame:// 自定义协议（内存帧 → WebView）      │
│ React: selector×N │ overlay │ settings                                       │
└────────────────────────────────────┬─────────────────────────────────────────┘
                                     │ HTTPS  POST /v1/reply (JPEG base64)
┌────────────────────────────────────▼─────────────────────────────────────────┐
│ Server (Go, Docker Compose: caddy → server, SQLite)                          │
│  auth → IP 限额 → 积分预扣 → prompt + schema → Claude → 校验 → 确认/退还     │
│  只记录元数据（延迟、token、模型、错误码），不记录图片和文字                  │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 3.1 状态机（`flow.rs`）

```text
Idle ──hotkey──► Selecting ──mouseup──► Analyzing ──ok/err──► Showing ──click──► Copied ──► Idle
                    │ESC                    │ESC                  │ESC/失焦
                    └────────────────────► Idle ◄────────────────┘
```

- 流程进行中再按快捷键：取消当前流程，重新开始。
- 进入非 Idle 状态时临时注册全局 `Escape` 快捷键，回到 Idle 时注销。这样即使窗口没拿到焦点，ESC 也能取消。
- Analyzing 状态下取消：中止 HTTP 请求的 tokio task，丢弃截图。
- 每次流程有一个 `session_id`，用于埋点，并且用来丢弃过期的异步结果。

---

## 4. 桌面端详细设计（Windows）

### 4.1 全局快捷键
- `tauri-plugin-global-shortcut`（底层 `RegisterHotKey`），默认值 `CommandOrControl+Shift+R`（按需求文档）。
- ⚠️ 这个快捷键和浏览器的"强制刷新"冲突，注册后浏览器里会失效。Settings 里可以修改。
- 注册失败时：托盘通知，并打开 Settings。

### 4.2 区域截图：先冻结画面再框选
1. 按下快捷键：记住前台窗口 `HWND`（`GetForegroundWindow`）。
2. `xcap` 截取所有显示器，得到 `RgbaImage`（物理像素），存在 `FrameStore`（内存）。
3. 每个显示器一个 selector 窗口（无边框、置顶、不显示在任务栏、`content_protected`），按该显示器的**物理坐标和尺寸**放置。窗口创建后复用，只隐藏不销毁。
4. selector 页面通过 `frame://localhost/<idx>?s=<session>` 加载冻结画面。协议处理函数从内存读取帧，编码成 JPEG 返回，不落盘。
5. 图片加载完成后，页面调用 `selector_ready`，Rust 再显示窗口，避免闪出上一次的旧画面。
6. 松开鼠标：页面上报选区（CSS 像素）和视口尺寸，Rust 按 `image.width / viewport.width` 换算成物理像素并裁剪。只允许在单个显示器内框选；小于 24×24 物理像素视为误点，直接取消。
7. 裁剪后立即释放所有整屏帧。

**悬停识别窗口**（类似 Snipaste）：截图的同一时刻按 Z 序枚举顶层窗口（`platform::visible_windows`），记录每个窗口的可见区域，连同鼠标位置一起交给 selector。
- 过滤：不可见、最小化、被系统隐藏（`DWMWA_CLOAKED`）、鼠标穿透（`WS_EX_TRANSPARENT`）、桌面（`Progman`/`WorkerW`）、本应用自己的窗口、小于 40px 的窗口。
- 位置用 `DWMWA_EXTENDED_FRAME_BOUNDS`，不用 `GetWindowRect`：后者在 Win10+ 会包含四周看不见的缩放边框。
- 前端按 Z 序找到鼠标下最上层的窗口并高亮；单击即选中该窗口，拖动照常自定义框选。
- 只识别到顶层窗口这一层。窗口内部元素（如 Chrome 内容区）需要 UI Automation，暂不做；macOS 版用 `CGWindowListCopyWindowInfo`（不需要辅助功能权限）。
- 整窗截图会带上侧边栏和联系人列表，Prompt 中要求模型只关注当前打开的对话。

### 4.3 图片预处理
- 裁剪 → 长边缩到不超过 1568px（Lanczos3）→ JPEG 质量 90 → base64，全部在内存中完成。
- 请求结束后丢弃（Rust 的 `Vec<u8>` 离开作用域即释放；P1 加 `zeroize`）。

### 4.4 Overlay
- 窗口复用；属性：透明、无边框、置顶、不显示在任务栏、`content_protected`、无阴影。宽 360 逻辑像素，高度由前端测量内容后上报（`overlay_resize`）。
- **位置算法**（`overlay/positioning.rs`，纯函数，有单测）：
  - 候选位置依次为：选区右侧 → 左侧 → 选区内右上角（不遮挡下方的最新消息）→ 所在显示器工作区的右下角。
  - 选出第一个能完整放进工作区的位置，最后限制在工作区范围内。
- 松开鼠标后立刻显示 Loading（"Analyzing conversation..."），4 秒后改为 "Still thinking..."。
- 点击回复（或按 `1`/`2`/`3`）：写入剪贴板 → `SetForegroundWindow` 还原之前的前台窗口 → 显示 "Copied ✓" 600ms → 隐藏。
- 关闭方式：ESC、失焦（仅在结果或错误状态下）、点击关闭按钮。

### 4.5 剪贴板
`tauri-plugin-clipboard-manager`，只写纯文本。

### 4.6 托盘
菜单：`AI Reply Assistant`（灰色标题）/ 快捷截图 / Settings / Quit。点击托盘图标打开 Settings。

### 4.7 设置与存储
- `settings.json` 存在应用配置目录：快捷键、服务端地址、显示名、开机启动。
- 设备 token 存在系统凭据库（`keyring`：Windows 凭据管理器 / macOS Keychain）。凭据库不可用时退回到设置文件。
- 开机启动：`tauri-plugin-autostart`。
- 单实例：`tauri-plugin-single-instance`。

### 4.8 埋点
- 事件：`capture_started` `capture_completed` `ai_request_started` `ai_request_completed` `reply_shown` `reply_clicked` `overlay_closed`。
- 字段：`event, ts, session_id, duration_ms, error_code`，**不包含任何内容**。
- MVP 先写本地 `telemetry/events.jsonl`（应用数据目录）；P2 再上报到服务端。
- 服务端同时记录每次请求的元数据（延迟、token、模型、结果），用来算成本和 P50。

---

## 5. 中转服务设计

### 5.1 接口

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `POST` | `/v1/device/register` | — | body `{device_id, hw_hash?}` → `{token}`；新设备按规则发放体验额度 |
| `GET` | `/auth/login` | — | 浏览器登录页（邮箱验证码），参数见 §5.3 |
| `POST` | `/auth/email/start`、`/auth/email/verify` | — | 登录页调用：发送 / 校验验证码 |
| `POST` | `/v1/auth/token` | — | 授权码 + PKCE verifier → access / refresh token |
| `POST` | `/v1/auth/refresh`、`/v1/auth/logout` | refresh token | 轮换 / 作废 |
| `GET` | `/v1/me` | 任一 | 套餐、各桶积分、今日用量、订阅状态、可购买套餐 |
| `POST` | `/v1/reply` | 任一 | 生成回复；可带 `Idempotency-Key` 头 |
| `POST` | `/v1/billing/link` | access | 申请结账 / 管理订阅的一次性网页链接 |
| `GET/POST` | `/billing/*` | 一次性票据 | 结账跳转、Stripe webhook、开发模式模拟支付 |
| `GET` | `/v1/health` | — | 健康检查 |

"任一"指 access token（已登录）或设备 token（匿名）。JWT 三段、设备 token 两段，服务端据此区分。

`POST /v1/reply` 请求：

```json
{ "image": "<base64>", "media_type": "image/jpeg", "locale": "zh-CN", "display_name": "Danny", "client_version": "0.1.0" }
```

成功响应（200）：

```json
{
  "status": "ok",
  "analysis": { "language": "zh-CN", "latest_message": "...", "summary": "...", "emotion": "...", "intent": "...", "strategy": "..." },
  "replies": [ { "style": "empathetic", "label": "得体", "text": "..." }, { "style": "funny", ... }, { "style": "direct", ... } ],
  "meta": { "request_id": "...", "latency_ms": 2310, "remaining_today": 54 },
  "credits": { "remaining": 54, "plan": "free" }
}
```

`status` 也可能是 `insufficient_context` 或 `not_a_conversation`，这两种情况 `replies` 为空数组，且**不扣积分**。

错误响应（非 200）：`{ "error": { "code": "...", "message": "...", "details": {...}? } }`

| HTTP | code | 客户端处理 |
|---|---|---|
| 401 | `unauthorized` | 登录令牌：刷新后重试一次；设备 token：重新注册后重试一次 |
| 402 | `insufficient_credits` | `details.login_required = true`（匿名）→ "体验次数已用完，登录领取额度"；否则 → "积分已用完，查看账户 / 升级" |
| 429 | `daily_cap` | 达到套餐每日上限 |
| 429 | `rate_limited` / `quota_exceeded` | 请求太频繁 / 网络维度超限 |
| 400 | `bad_request` | Unable to analyze this conversation. |
| 502 / 504 | `upstream_error` / `refusal` / `invalid_output` / `timeout` | Unable to analyze this conversation. Try again. |
| 网络失败 | — | Network unavailable. |

### 5.2 用户系统总览

```text
首次启动 ──► 匿名设备 token ──► 体验额度 10 次
                                  │ 用完 / 主动登录
                                  ▼
               系统浏览器邮箱验证码登录（PKCE + 本机回调）
                                  │ 设备绑定到账号
                                  ▼
                 免费：注册赠送 50 次（90 天）+ 每天 5 次
                                  │ 升级（Stripe Checkout）
                                  ▼
                 Pro：每月 1000 次 / Pro+：每月 3000 次（均有每日上限）
```

存储分工：
全部在一个 SQLite 文件里（WAL 模式），表结构见 `server-go/internal/store/migrations/`：
- **账号**：users、devices、otp_codes、auth_codes、refresh_tokens、subscriptions、web_tickets、usage_events（只有元数据）。
- **积分账户**（`u:<user_id>` 或 `d:<device_id>`）：credit_grants（积分桶）、credit_ledger（流水）、credit_holds（预扣）、credit_usage_day（当日用量）。所有积分操作在进程内互斥锁 + 事务中执行，扣费原子；因此服务端**单实例部署**。
- **ip_counters**：按 IP 的每日软限制计数。

### 5.3 登录（RFC 8252）
1. 桌面端开本机端口 `127.0.0.1:<随机>`，生成 `state` 和 PKCE `verifier / challenge`，用系统浏览器打开 `/auth/login?device_id&redirect_uri&state&code_challenge`。
2. 登录页：输入邮箱 → 发送 6 位验证码（Resend；开发模式且未配置时直接显示在页面上）→ 校验。
3. 服务端找到或创建用户（按规范化邮箱去重）、把设备绑定到账号、发放注册赠送，签发 5 分钟有效的一次性授权码，跳回 `redirect_uri?code&state`。**`redirect_uri` 只允许本机回环地址**。
4. 桌面端校验 `state`，用授权码 + verifier 换取令牌：access token（JWT，15 分钟，只在内存）、refresh token（30 天，存系统凭据库，每次刷新都轮换）。
5. 检测到已轮换的 refresh token 被再次使用 → 整条令牌链作废，强制重新登录。

### 5.4 积分
- **计费**：1 次成功回复 = 1 积分（`REPLY_COST`，以后可按模型加权）。
- **分桶**：trial（体验，不过期）、bonus（注册赠送，90 天）、daily（免费用户每日补充，当天有效）、subscription（订阅周期 + 3 天宽限）、topup（预留）。**先扣最快过期的桶**。
- **扣费流程**：`reserve`（预扣，同时检查频率和每日上限）→ 调用模型 → 成功且 `status=ok` 时 `commit`，否则 `release` 退还。超过 5 分钟未确认的预扣自动退还。
- **幂等**：客户端每次分析生成一个 `Idempotency-Key`，网络重试（进行中或已完成）都不会重复扣费。
- 套餐数值集中在 `server-go/internal/credits/plans.go`。

### 5.5 订阅支付
- 桌面端 `POST /v1/billing/link` 申请 10 分钟有效的一次性票据 → 系统浏览器打开 `/billing/checkout` → 跳转 Stripe Checkout。
- Webhook：`invoice.paid` 按发票号幂等发放本周期积分；`customer.subscription.updated/deleted` 同步状态；`charge.refunded` / `charge.dispute.created` 收回订阅积分。
- 套餐判定：订阅 `active` 或 `past_due` 且未超过周期结束 + 3 天 → 付费套餐，否则免费。
- **开发模式**（`DEV_MODE=true` 且未配置 Stripe）：结账页变成"模拟支付"，管理订阅页可"到期后取消 / 立即结束"，用于本地完整测试。

### 5.6 防刷
| 层 | 规则 |
|---|---|
| 体验额度 | 同一硬件哈希（加盐 MachineGuid）只发一次；同一 IP 每天最多 3 台新设备 |
| 注册 | 必须验证邮箱；拒绝一次性邮箱；邮箱规范化去重；可选 Turnstile；**每台设备、每个账号各只送一次注册赠送** |
| 验证码 | 10 分钟有效、最多试 5 次、同一邮箱 1 分钟内不能重发、同一 IP 每天 10 次 |
| 令牌 | access 15 分钟；refresh 绑定设备、每次轮换、重用即作废整条链；每账号最多 5 台设备 |
| 请求 | 每账户每分钟 6 次、同时 2 个；套餐每日上限；同一 IP 每日上限 |
| 支付 | Stripe Radar；退款 / 拒付收回积分 |

### 5.7 隐私
- 不存储截图和聊天内容；日志与 `usage_events` 只有 `request_id、账户、套餐、状态、模型、耗时、token 数`。
- 账号只关联用量元数据；注销时删除个人数据。
- 隐私政策写明：截图经服务端转发给 AI 服务商，不存储。

---

## 6. AI 调用

### 6.1 调用方式
- 官方 SDK `@anthropic-ai/sdk`，流式请求（`stream().finalMessage()`），防止 HTTP 超时。
- **结构化输出**：`output_config.format = { type: "json_schema", schema }`，返回后再用 zod 校验。
- **一次请求同时拿到分析和回复**，省掉一轮网络往返。
- System Prompt 加 `cache_control`（达到最小缓存长度后生效）。
- **拒答兜底**：在支持的模型上默认开启 `fallbacks: "default"`（beta `server-side-fallback-2026-07-01`）；如果最终 `stop_reason` 仍是 `refusal`，返回 `refusal` 错误。

### 6.2 模型配置（环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `MODEL` | `claude-opus-5-5` | 可改为 `claude-sonnet-5-5` / `claude-haiku-4-5` |
| `EFFORT` | `low` | Opus 5.5 不能关闭 thinking，用 `low` 压低延迟 |
| `THINKING` | `auto` | `auto`：Opus 不传；Sonnet 5.5 可设 `between_tools`（等同关闭思考）；Haiku 不传 |
| `FALLBACKS` | `default` | `off` 关闭 |
| `MAX_TOKENS` | `8000` | 包含 thinking |

上线前用 20–30 张真实截图测三种配置的延迟和质量，再确定默认值。

### 6.3 Prompt 要点
- 角色：沟通助手；先理解对话，再给出三种风格的回复。
- 判断谁是用户本人：靠右或彩色气泡（微信 / WhatsApp / Telegram / iMessage）；Slack/Discord 靠 `display_name` 判断。
- 回复规则：同语言、同正式程度、1–3 句、像真人说话、不编造事实、不替用户承诺金钱 / 时间 / 结果、不过度道歉、不把分析写进回复。
- 分析字段简短（每个不超过 20 字），放在 `replies` 前面，作为轻量的上下文理解，不要求模型输出推理过程。
- 不确定时返回 `insufficient_context` / `not_a_conversation`。

---

## 7. macOS 适配预留

| 方面 | 做法 |
|---|---|
| 截图 | `capture` trait 的 mac 实现走 ScreenCaptureKit（`xcap` 已封装），最低 macOS 14 |
| 权限 | 只申请"屏幕录制"；首次触发时检查并引导；**不申请辅助功能** |
| 菜单栏 | `ActivationPolicy::Accessory`，单色模板图标 |
| Overlay | NSPanel nonactivating（`tauri-nspanel`），`canJoinAllSpaces \| fullScreenAuxiliary`；不需要还原焦点 |
| 防截图 | `content_protected` → `sharingType = none`；冻结画面的截图方式本身不受影响 |
| 快捷键 | `CommandOrControl+Shift+R` 在 mac 上就是 `Cmd+Shift+R`，不需要辅助功能权限 |
| 分发 | Developer ID 签名 + 公证，Universal 包 |

---

## 8. 开发里程碑

| 阶段 | 内容 | 状态 |
|---|---|---|
| P0-1 | 脚手架、托盘、快捷键、状态机 | MVP 启动 |
| P0-2 | 多屏截图到内存、selector、裁剪 | MVP 启动 |
| P0-3 | 中转服务、Prompt、结构化输出 | MVP 启动 |
| P0-4 | Overlay、定位、复制、焦点还原 | MVP 启动 |
| P1 | Settings 完整化、错误处理细化、混合 DPI 测试、改快捷键、开机启动、BYOK 隐藏选项 | |
| 用户系统 1–2 | 匿名体验、邮箱登录、积分账户、注册赠送、订阅支付（Stripe / 开发模式模拟）、账户设置页 | 已实现，待联调 |
| 服务端 Go 重写 | 接口不变；SQLite 替代 D1 / Durable Object / KV；Docker Compose + Caddy 部署 | 已实现，待联调 |
| 用户系统 3–4 | 风控评分、手机验证、成本熔断；Google / Apple / 微信登录、国内支付 | |
| P2 | 流式逐条显示、埋点上报、签名、自动更新、视觉打磨 | |

---

## 9. 本地开发

```bash
# 中转服务（Go 1.27+）
cd server-go
DEV_MODE=true MOCK_AI=true TOKEN_SECRET=dev-secret go run ./cmd/nextreply-server   # http://127.0.0.1:8787
# 去掉 MOCK_AI 并设置 ANTHROPIC_API_KEY 即调用真实模型；线上部署见 server-go/README.md

# 桌面端
cd apps/desktop && npm install
npm run tauri dev
```

桌面端默认服务端地址：开发构建是 `http://127.0.0.1:8787`，正式构建在 Settings 中配置。
