# AI 高情商回复助手 MVP 需求文档

## 1. 项目简介

开发一款运行于 Windows / macOS 的桌面工具。

用户在微信、WhatsApp、Telegram、Slack、Discord 等聊天软件中遇到“不知道怎么回复”的消息时，可以通过全局快捷键框选聊天区域。

应用自动截取该区域，调用 Vision AI 理解聊天上下文，并生成若干条不同风格的回复建议。

用户点击其中一条后，回复内容自动复制到剪贴板，用户回到聊天软件直接粘贴发送。

产品核心目标：

> 在任何聊天软件旁边，通过一次快捷键快速获得合适的回复建议。

---

# 2. MVP 核心流程

用户流程：

```text
正在使用任意聊天软件
        ↓
按全局快捷键
例如：
Windows: Ctrl + Shift + R
macOS: Cmd + Shift + R
        ↓
屏幕进入框选状态
        ↓
用户拖动选择聊天区域
        ↓
截图
        ↓
截图发送给 Vision AI
        ↓
AI 判断聊天上下文
        ↓
生成 3 条回复
        ↓
屏幕边缘出现小型 Overlay
        ↓
用户点击一条回复
        ↓
自动复制到 Clipboard
        ↓
Overlay 自动关闭
        ↓
用户 Ctrl/Cmd + V
```

整个流程不要求用户打开主应用。

---

# 3. MVP 功能

## 3.1 系统托盘应用

应用启动后常驻后台。

Windows：

```text
System Tray
```

macOS：

```text
Menu Bar
```

托盘菜单包含：

```text
AI Reply Assistant

快捷截图
Settings
Quit
```

应用主窗口不是 MVP 的主要入口。

---

# 3.2 全局快捷键

默认快捷键：

```text
Windows:
Ctrl + Shift + R

macOS:
Cmd + Shift + R
```

用户可以在 Settings 中修改。

快捷键应该在其他软件处于焦点状态时正常工作。

例如：

```text
WeChat
WhatsApp Desktop
Telegram
Slack
Discord
Chrome
Edge
Safari
```

---

# 3.3 区域截图

触发快捷键后：

1. 当前屏幕轻微变暗。
2. 鼠标变成截图选择状态。
3. 用户拖动矩形区域。
4. 松开鼠标后完成截图。

行为类似：

```text
Windows Snipping Tool

macOS Cmd + Shift + 4
```

要求：

- ESC 取消截图
- 支持多显示器
- 支持 Retina / HiDPI
- 截图完成后不保存到磁盘
- 默认只在内存中存在
- AI 请求完成后释放截图

MVP 不需要截图编辑功能。

---

# 3.4 AI 图片理解

截图完成后，将图片发送给 Vision Model。

AI 需要识别：

- 聊天中有哪些角色
- 哪些消息属于用户本人
- 哪些消息属于对方
- 最近一条对方消息
- 当前聊天上下文
- 对方大致情绪
- 对方可能表达的真实意图
- 适合的回复策略

不需要传统 OCR pipeline。

优先：

```text
Screenshot
    ↓
Vision Model
    ↓
Structured JSON
```

建议 AI 首先输出结构化数据：

```json
{
  "language": "zh-CN",
  "latest_message": "你最近是不是很忙？",
  "conversation_summary": "对方感觉最近被冷落",
  "emotion": "slightly upset",
  "intent": "seeking attention",
  "recommended_strategy": "acknowledge emotion and explain briefly"
}
```

然后生成回复。

---

# 3.5 回复生成

默认生成 3 条回复。

MVP 可以固定三种风格：

```text
1. 得体 / 高情商
2. 轻松 / 有趣
3. 简洁 / 直接
```

例如：

```text
对方：
你最近是不是很忙，都没怎么找我。

AI：

得体
最近确实有点忙，不过不是故意冷落你，等我忙完这阵请你吃饭补偿一下 😄

轻松
被你发现了😂 最近确实有点忙，但还没忙到把你忘了。

简洁
最近确实比较忙，消息回得慢了点，抱歉，不是故意冷落你。
```

要求回复：

- 尽量像真人说话
- 不要有明显 AI 腔
- 不要过度解释
- 默认控制在 1～3 句话
- 保持和原对话相同语言
- 保留对话的正式程度
- 如果上下文无法判断，不要编造事实

---

# 3.6 Overlay 回复卡片

AI 返回结果后，在当前屏幕显示一个轻量级 floating overlay。

UI 示例：

```text
┌─────────────────────────────────┐
│ 对方可能有点失落                │
│                                 │
│ 😌 得体                         │
│ 最近确实有点忙，不过不是故意... │
│                                 │
│ 😄 轻松                         │
│ 被你发现了😂 最近确实有点忙...  │
│                                 │
│ ⚡ 简洁                         │
│ 最近比较忙，回消息慢了点...     │
│                                 │
│ Esc 关闭                        │
└─────────────────────────────────┘
```

设计要求：

- Always On Top
- 小尺寸
- 不占据屏幕中心大面积空间
- 默认出现在截图区域右侧或附近
- 如果右侧空间不足，自动寻找合适位置
- 不要遮挡截图中的最后几条聊天消息
- Overlay 本身不能被后续 screenshot 捕获
- ESC 可以关闭
- 点击其他地方可以关闭

MVP 不做完整 Chat UI。

---

# 3.7 点击复制

点击某条回复：

```text
copy reply → system clipboard
```

然后：

```text
Overlay close
```

可以显示 1 秒左右反馈：

```text
Copied ✓
```

MVP 不需要自动发送消息。

也不需要自动控制微信 / WhatsApp 输入框。

这样可以降低：

- 系统权限
- 平台适配成本
- 误操作风险

---

# 3.8 Loading 状态

AI 请求过程中显示简单状态：

```text
Analyzing conversation...
```

如果请求超过一定时间，可以显示：

```text
Still thinking...
```

不要阻塞其他系统操作。

用户按 ESC 可以随时取消。

---

# 3.9 错误处理

至少处理以下情况：

### AI API 错误

```text
Unable to analyze this conversation.
Try again.
```

### 网络错误

```text
Network unavailable.
```

### 截图内容不足

例如只截到一条消息：

```text
Not enough conversation context.

Try selecting a slightly larger area.
```

### 模型无法判断聊天

```text
I couldn't confidently identify the conversation.
Try selecting the chat area again.
```

---

# 4. Settings

MVP Settings 页面只需要：

```text
General
- Global shortcut

AI
- API Provider
- API Key
- Model

Privacy
- Do not save screenshots: ON

Startup
- Launch at login
```

API Provider 第一版可以只支持一个。

建议代码层保留抽象接口：

```ts
interface AIProvider {
  analyzeConversation(image): Promise<ConversationAnalysis>
  generateReplies(context): Promise<ReplySuggestion[]>
}
```

以后可以增加：

```text
OpenAI
Anthropic
Gemini
Local Model
```

---

# 5. AI 返回格式

不要直接让模型返回自由文本。

要求模型返回 JSON。

示例：

```json
{
  "analysis": {
    "language": "zh-CN",
    "summary": "对方因为用户最近回复较少而有一些失落。",
    "emotion": "slightly upset",
    "intent": "seeking reassurance"
  },
  "replies": [
    {
      "style": "empathetic",
      "label": "得体",
      "text": "最近确实有点忙，不过不是故意冷落你，消息回慢了点，抱歉。"
    },
    {
      "style": "funny",
      "label": "轻松",
      "text": "被你发现了😂 最近确实有点忙，不过还没忙到把你忘了。"
    },
    {
      "style": "direct",
      "label": "简洁",
      "text": "最近比较忙，回复慢了一些，不是故意不找你。"
    }
  ]
}
```

客户端负责渲染。

---

# 6. Prompt 目标

System Prompt 大致需要告诉模型：

你是一个沟通助手。

你的任务不是简单改写文字，而是根据聊天截图：

1. 理解最近几轮对话。
2. 判断谁是用户本人、谁是对方。
3. 判断对方最新消息的意图和情绪。
4. 给出自然、真实、适合直接发送的回复。

规则：

- 回复应该像普通人聊天。
- 避免 AI 味。
- 避免长篇大论。
- 不编造不存在的信息。
- 不擅自替用户承诺金钱、时间、工作结果。
- 不过度道歉。
- 不把自己的分析写入回复。
- 默认输出 3 个不同策略。
- 回复使用和聊天相同的主要语言。

---

# 7. 技术架构建议

优先选择能够同时开发 Windows / macOS 的桌面技术。

建议评估：

```text
Tauri
+
Rust
+
React / TypeScript
```

或者：

```text
Electron
+
React / TypeScript
```

如果更看重：

```text
快速 MVP
```

Electron 开发更快。

如果更看重：

```text
体积
性能
原生能力
长期产品
```

可以考虑 Tauri。

推荐模块结构：

```text
src/

app/
    tray
    settings

capture/
    screenshot
    region-selector
    monitor-manager

overlay/
    reply-window
    positioning

hotkey/
    global-shortcut

ai/
    provider
    prompts
    parser

clipboard/
    clipboard-service

storage/
    settings
```

尽量避免不同功能之间高度耦合。

---

# 8. OS 相关要求

## macOS

需要研究：

```text
Screen Recording Permission
Accessibility Permission
Global Hotkeys
Always-on-top Window
Multi-monitor
Retina Screenshot
```

原则：

只申请实现当前功能真正需要的权限。

如果 MVP 不需要 Accessibility，则先不要申请。

---

## Windows

需要支持：

```text
Windows 10
Windows 11
```

需要处理：

```text
Global Hotkey
Multi-monitor
DPI Scaling
Always-on-top Window
Screenshot capture
Clipboard
```

Overlay 尽量设置为：

```text
excluded from screen capture
```

避免下一次截图捕获到自己的 Overlay。

---

# 9. 隐私原则

这是核心产品要求。

默认：

```text
Screenshot 不写入磁盘

Screenshot 不进入历史记录

AI 请求结束立即释放

聊天内容不在客户端长期保存
```

UI 中需要明确告诉用户：

```text
Screenshots are analyzed only when you trigger the shortcut.
They are not continuously recorded.
```

未来可以增加：

```text
Local OCR
PII masking
Local models
```

MVP 不要求实现。

---

# 10. MVP 明确不做

第一版不要实现：

```text
× 自动读取所有聊天内容

× 24/7 屏幕录制

× Relationship Memory

× 联系人数据库

× 自动发送消息

× 微信 API integration

× WhatsApp API integration

× Slack / Discord bot

× Browser Extension

× Mobile App

× AI Keyboard

× 用户登录系统

× 云同步

× 聊天历史管理

×复杂 Prompt 编辑器
```

核心原则：

> 第一版只验证用户是否愿意在“遇到难回复消息”时主动按快捷键。

---

# 11. MVP 成功指标

最核心需要记录：

```text
capture_started
capture_completed
ai_request_started
ai_request_completed
reply_shown
reply_clicked
overlay_closed
```

重点指标：

### Activation

用户安装后是否成功完成第一次：

```text
Screenshot → AI reply → Copy
```

### Reply Selection Rate

```text
选择某条 AI 回复的次数
/
成功生成回复次数
```

### Time To Reply

从：

```text
触发快捷键
```

到：

```text
回复复制完成
```

耗时。

目标：

```text
P50 < 5 秒
```

### Daily Usage

观察：

```text
用户每天触发多少次 Reply
```

这是最重要的 PMF 信号之一。

---

# 12. MVP 验收标准

满足以下条件即可认为 MVP 完成：

1. Windows / macOS 可以正常安装运行。
2. 应用可以后台常驻。
3. 全局快捷键可以呼出截图。
4. 可以拖拽选择屏幕区域。
5. 支持多显示器。
6. 截图能够正常传给 Vision AI。
7. AI 可以识别大部分普通聊天截图。
8. 返回 3 条回复建议。
9. Overlay 正常显示在当前应用上方。
10. 点击回复可以复制到系统剪贴板。
11. ESC 可以关闭整个流程。
12. 截图不会默认写入磁盘。
13. 网络/API 异常不会导致应用崩溃。
14. Overlay 不应该影响正常输入和鼠标操作。
15. Windows DPI / macOS Retina 下 UI 和截图坐标正确。

---

# 13. 开发优先级

建议开发顺序：

```text
P0
全局快捷键
↓
区域截图
↓
AI Vision API
↓
JSON 回复结果
↓
Overlay
↓
Clipboard
```

完成以上流程后，再处理：

```text
P1
Settings
错误处理
多显示器
DPI
Loading UI
快捷键修改
开机启动
```

最后：

```text
P2
UI polish
动画
模型切换
埋点
自动更新
```

不要在 P0 阶段投入过多时间做视觉设计。

---

# 14. 后续版本方向

MVP 验证成功后，可以依次考虑：

### V2：自动识别聊天区域

```text
Hotkey
↓
Automatically detect current conversation
↓
无需用户手动框选
```

### V3：Accessibility Context

优先从 OS Accessibility API 获取聊天文字：

```text
Accessibility
+
Screenshot Vision fallback
```

降低：

- token cost
- latency
- 识别错误

### V4：Personal Voice

学习用户最终实际发送的表达风格。

### V5：Relationship Context

为不同联系人记忆：

```text
关系
表达风格
沟通习惯
历史上下文
```

但这些都不属于 MVP。

---

# 15. 核心产品原则

开发过程中优先遵循：

```text
1. 快
2. 少操作
3. 不打断当前聊天
4. 不要求用户写 Prompt
5. 不长期保存私人聊天
6. 回复必须像真人
```

整个产品最重要的体验不是：

> AI 能回答多少问题。

而是：

> 用户看到一条难回复的信息之后，能否在几秒之内得到一句愿意直接发送的话。
```

这版已经可以直接作为 Claude Code / Codex 的项目上下文。建议实际开发时再额外拆成一份 **`CLAUDE.md / AGENTS.md` 工程实现规范**，把技术栈、目录结构、跨平台 abstraction、禁止过度设计、每阶段任务顺序写进去，这样 AI coding agent 会稳定很多。
