# NextReply — 工程规范

AI 高情商回复助手：按全局快捷键框选聊天区域 → Vision 模型分析 → 浮层给出 3 条回复 → 点击复制。

- 产品需求：[REQUIREMENT.md](REQUIREMENT.md)
- 技术方案：[TECH_DESIGN.md](TECH_DESIGN.md)（架构、接口、状态机、平台适配都以此为准）

## 仓库结构

| 目录 | 内容 | 技术栈 |
|---|---|---|
| `apps/desktop/` | 桌面端 | Tauri 2 + Rust（核心逻辑）+ React 18 / TS（UI） |
| `apps/desktop/src-tauri/src/` | Rust 核心：`flow.rs` 状态机、`capture/`、`overlay/`、`ai/`、`hotkey.rs`、`platform/` | |
| `apps/desktop/src/` | 三个页面：`selector/`、`overlay/`、`settings/`；IPC 类型在 `shared/ipc.ts` | |
| `server-go/` | 中转服务（Linux + Docker Compose 部署） | Go + SQLite + `anthropic-sdk-go` |

## 常用命令

```bash
# 服务端（server-go/）
DEV_MODE=true MOCK_AI=true TOKEN_SECRET=dev-secret go run ./cmd/nextreply-server   # → http://127.0.0.1:8787
go vet ./... && go test ./...
docker compose up -d --build   # 部署，见 server-go/README.md

# 桌面端（apps/desktop/）
npm run tauri dev    # 启动应用（会自动起 vite）
npm run typecheck
cd src-tauri && cargo test
```

## 硬性原则

1. **隐私**：截图只在内存中；客户端和服务端都不落盘、不记录图片或聊天内容。日志 / 埋点只记录事件、耗时、错误码。
2. **客户端不持有模型 API Key**；Prompt、模型选择都在服务端（`server-go/internal/ai/`）。
3. **流程只走 `flow.rs`**：所有状态变化（开始、取消、出结果、复制）都通过 flow 的函数完成；异步结果必须校验 session。
4. **平台差异隔离**：系统 API 只能出现在 `platform/` 和 `capture/` 里，业务代码不写 `#[cfg(target_os)]`。
5. **不跨 await 持有 Mutex**。
6. **协议同步**：改 `/v1/reply` 的响应格式时，同时更新 `server-go/internal/ai/schema.go`、`src-tauri/src/ai/types.rs`、`src/shared/ipc.ts`。

## 不要做（MVP 范围外，见需求文档 §10）

自动发送消息、读取其他应用内容、辅助功能权限、聊天历史、云同步、复杂的 Prompt 编辑器。
（用户系统已按 TECH_DESIGN.md §5.2–5.6 纳入范围；新增登录方式、风控规则时同样先更新文档。）
需要这些时先更新 TECH_DESIGN.md 并确认。

## 风格

- Rust：`cargo fmt`；错误向上返回 `Result<_, String>` 或模块内错误类型，不在业务路径上 `unwrap()` 外部输入。
- Go：`gofmt`；错误向上返回，返回给客户端的错误统一用 `internal/apierr`。
- TS：严格模式；组件保持小而直接，不引入 UI 组件库和状态管理库。
- 注释用中文，解释"为什么"，不复述代码。
