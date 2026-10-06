package app

// 服务端渲染的简单页面（登录、结账结果）。在用户的系统浏览器里打开。

import (
	"encoding/json"
	"html"
	"strings"
)

var esc = html.EscapeString

const pageStyle = `<style>
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
</style>`

func page(title, body, head string) string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>` + esc(title) + ` · NextReply</title>
` + pageStyle + `
` + head + `
</head>
<body><main class="card">` + body + `</main></body>
</html>`
}

func messagePage(title, message string) string {
	return page(title, "<h1>"+esc(title)+"</h1><p>"+esc(message)+"</p>", "")
}

type loginParams struct {
	DeviceID      string `json:"device_id"`
	RedirectURI   string `json:"redirect_uri"`
	State         string `json:"state"`
	CodeChallenge string `json:"code_challenge"`
	TurnstileKey  string `json:"-"`
}

// renderLoginPage 邮箱验证码登录页。验证成功后跳回桌面端的本机回调地址。
func renderLoginPage(p loginParams) string {
	turnstile, head := "", ""
	if p.TurnstileKey != "" {
		turnstile = `<div class="cf-turnstile" data-sitekey="` + esc(p.TurnstileKey) + `" style="margin-top:12px"></div>`
		head = `<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>`
	}
	// json.Marshal 默认把 < > & 转义成 < 等，可以安全地嵌入 <script>
	params, _ := json.Marshal(p)

	body := `
<h1>登录 NextReply</h1>
<p>输入邮箱获取验证码。新用户注册即送 50 次回复额度。</p>
<form id="step1">
  <label for="email">邮箱</label>
  <input id="email" type="email" autocomplete="email" required placeholder="you@example.com" />
  {{TURNSTILE}}
  <button id="send" type="submit">获取验证码</button>
</form>
<form id="step2" class="hidden">
  <label for="code">验证码已发送到 <span id="sentTo"></span></label>
  <input id="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required placeholder="6 位数字" />
  <div id="dev" class="dev hidden"></div>
  <button id="verify" type="submit">登录</button>
  <button id="back" type="button" class="secondary">换个邮箱</button>
</form>
<div id="err" class="error"></div>
<script>
const P = {{PARAMS}};
const $ = (id) => document.getElementById(id);
const show = (msg) => { $("err").textContent = msg || ""; };
async function post(url, body) {
  const r = await fetch(url, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error((data.error && data.error.message) || "请求失败，请稍后再试");
  return data;
}
$("step1").addEventListener("submit", async (e) => {
  e.preventDefault(); show(); $("send").disabled = true;
  try {
    const tokenEl = document.querySelector("[name=cf-turnstile-response]");
    const data = await post("/auth/email/start", { email: $("email").value, turnstile: tokenEl ? tokenEl.value : undefined });
    $("sentTo").textContent = $("email").value;
    if (data.dev_code) { $("dev").textContent = "开发模式（未配置邮件服务）：验证码 " + data.dev_code; $("dev").classList.remove("hidden"); }
    $("step1").classList.add("hidden"); $("step2").classList.remove("hidden"); $("code").focus();
  } catch (err) { show(err.message); if (window.turnstile) window.turnstile.reset(); }
  finally { $("send").disabled = false; }
});
$("step2").addEventListener("submit", async (e) => {
  e.preventDefault(); show(); $("verify").disabled = true;
  try {
    const data = await post("/auth/email/verify", { ...P, email: $("email").value, code: $("code").value.trim() });
    document.querySelector("main").innerHTML = "<h1>登录成功</h1><p>正在返回 NextReply，可以关闭此页面。</p>";
    location.href = data.redirect;
  } catch (err) { show(err.message); }
  finally { $("verify").disabled = false; }
});
$("back").addEventListener("click", () => { $("step2").classList.add("hidden"); $("step1").classList.remove("hidden"); show(); });
</script>`
	body = strings.Replace(body, "{{TURNSTILE}}", turnstile, 1)
	body = strings.Replace(body, "{{PARAMS}}", string(params), 1)
	return page("登录", body, head)
}
