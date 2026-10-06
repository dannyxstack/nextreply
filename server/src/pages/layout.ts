// 服务端渲染的简单页面（登录、结账结果）。在用户的系统浏览器里打开。

export const esc = (s: string) =>
  s.replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]!);

export function page(title: string, body: string, head = ""): string {
  return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>${esc(title)} · NextReply</title>
<style>
  :root { --bg:#f7f8fc; --panel:#fff; --border:#e3e5ee; --text:#0f172a; --muted:#64748b; --accent:#6366f1; --danger:#dc2626; color-scheme: light dark; }
  @media (prefers-color-scheme: dark) { :root { --bg:#17181d; --panel:#1f2128; --border:#2f323c; --text:#e5e7eb; --muted:#9ca3af; --accent:#8b8ff8; --danger:#f87171; } }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; background:var(--bg); color:var(--text);
         font-family: system-ui, -apple-system, "Segoe UI", "Microsoft YaHei", sans-serif; padding:16px; }
  .card { width:100%; max-width:400px; background:var(--panel); border:1px solid var(--border); border-radius:14px; padding:28px; }
  h1 { font-size:20px; margin:0 0 6px; }
  p { color:var(--muted); font-size:14px; line-height:1.6; margin:0 0 16px; }
  label { display:block; font-size:13px; margin:12px 0 6px; }
  input { width:100%; font:inherit; padding:10px 12px; border:1px solid var(--border); border-radius:8px; background:var(--bg); color:var(--text); }
  button { width:100%; margin-top:16px; font:inherit; padding:10px; border:0; border-radius:8px; background:var(--accent); color:#fff; cursor:pointer; }
  button.secondary { background:transparent; color:var(--accent); border:1px solid var(--border); }
  button:disabled { opacity:.6; cursor:default; }
  .error { color:var(--danger); font-size:13px; margin-top:10px; min-height:1em; }
  .dev { margin-top:12px; padding:8px 10px; border-radius:8px; background:#fef3c7; color:#92400e; font-size:13px; }
  .plan { border:1px solid var(--border); border-radius:10px; padding:12px; margin-top:12px; }
  .plan b { font-size:16px; }
  .hidden { display:none; }
</style>
${head}
</head>
<body><main class="card">${body}</main></body>
</html>`;
}

export function messagePage(title: string, message: string): string {
  return page(title, `<h1>${esc(title)}</h1><p>${esc(message)}</p>`);
}
