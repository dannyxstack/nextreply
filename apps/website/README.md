# NextReply 网站

`nextreply.novakitestudio.com`：首页、价格、下载、隐私政策、服务条款、退款政策，英文（`/`）和中文（`/zh/`）。
设计见 [TECH_DESIGN.md §10](../../TECH_DESIGN.md)。

纯静态：原生 HTML / CSS / 少量 JS，没有依赖，`node build.mjs` 生成 `dist/`。

## 目录

```text
apps/website/
├── site.json            网站地址、API 地址、下载链接、联系邮箱、法律页面是否为草稿
├── build.mjs            把布局套到各页面正文上，输出 dist/
├── src/
│   ├── layout.html      页头、页脚、语言切换、hreflang
│   ├── i18n.json        布局里的文案（导航、页脚等）
│   └── pages/en|zh/     各页面正文，第一行是 <!-- title: …; description: … -->
└── static/              原样复制到 dist/：style.css、pricing.js、favicon.svg、_headers
```

## 本地预览

```bash
cd apps/website
node build.mjs --api http://127.0.0.1:8787     # 价格页从本机服务端读取套餐
python3 -m http.server -d dist 8080            # 打开 http://127.0.0.1:8080
```

不带 `--api` 时使用 `site.json` 里的正式 API 地址。

## 修改内容

- **页面文字**：改 `src/pages/en/` 和 `src/pages/zh/` 下对应的文件，两种语言都要改。
- **价格**：不在网站里写。价格页运行时请求服务端 `GET /v1/plans`；每日上限、体验额度等在管理后台的「运行配置」里改，价格和每月次数在 `server-go/internal/credits/plans.go`。
- **下载链接**：填 `site.json` 的 `downloads.windows.url` / `version`，留空时按钮显示"即将推出"。
- **法律页面**：主体、地址、适用法律等确认后，替换页面中的 `[… to be confirmed]` / `[…待确认]`，填写 `contactEmail`，把 `legalDraft` 改为 `false` 并更新 `legalUpdated`。上线前请法务审阅。
- 页面里可以用 `{{key}}` 引用 `site.json` / `i18n.json` 的值（会转义），`{{{key}}}` 插入 HTML。引用不存在的键会让构建失败。

## 部署（Cloudflare Pages）

1. Cloudflare Pages 连接本仓库：根目录 `apps/website`，构建命令 `node build.mjs`，输出目录 `dist`。
2. 自定义域名 `nextreply.novakitestudio.com`。
3. `static/_headers` 会作为响应头生效（CSP 里放行了 API 域名，价格页需要）；换 API 域名时同步修改。
4. 可选：开启 Cloudflare Web Analytics（不写 cookie，不需要同意框）。

其他静态托管同样可用：上传 `dist/`，并按 `_headers` 配置响应头。
