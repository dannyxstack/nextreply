package app

// 服务端渲染的页面（桌面端登录、结账结果、网页账户），在用户的浏览器里打开；文案见 i18n.go。

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/nextreply/server/internal/credits"
)

var esc = html.EscapeString

const pageStyle = `<style>
  :root { --bg:#f7f8fc; --panel:#fff; --border:#e3e5ee; --text:#0f172a; --muted:#64748b; --accent:#4f46e5; --danger:#dc2626; --soft:#eef0ff; color-scheme: light dark; }
  @media (prefers-color-scheme: dark) { :root { --bg:#17181d; --panel:#1f2128; --border:#2f323c; --text:#e5e7eb; --muted:#9ca3af; --accent:#8b8ff8; --danger:#f87171; --soft:#23254a; } }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:flex; flex-direction:column; align-items:center; justify-content:center; background:var(--bg); color:var(--text);
         font-family: system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; padding:16px; }
  body.wide { justify-content:flex-start; padding-top:32px; }
  .card { width:100%; max-width:400px; background:var(--panel); border:1px solid var(--border); border-radius:14px; padding:28px; }
  body.wide .card { max-width:720px; }
  .topbar { width:100%; max-width:720px; display:flex; justify-content:space-between; align-items:center; margin-bottom:14px; font-size:14px; }
  .topbar a { color:var(--muted); text-decoration:none; }
  .topbar .brand { color:var(--text); font-weight:700; }
  h1 { font-size:20px; margin:0 0 6px; }
  h2 { font-size:16px; margin:26px 0 10px; }
  p { color:var(--muted); font-size:14px; line-height:1.6; margin:0 0 16px; }
  label { display:block; font-size:13px; margin:12px 0 6px; }
  input { width:100%; font:inherit; padding:10px 12px; border:1px solid var(--border); border-radius:8px; background:var(--bg); color:var(--text); }
  button, .button { display:inline-block; text-align:center; width:100%; margin-top:16px; font:inherit; padding:10px; border:0; border-radius:8px; background:var(--accent); color:#fff; cursor:pointer; text-decoration:none; }
  button.secondary, .button.secondary { background:transparent; color:var(--accent); border:1px solid var(--border); }
  button.danger { background:var(--danger); }
  button.inline { width:auto; margin:0; padding:4px 12px; font-size:13px; }
  button:disabled { opacity:.6; cursor:default; }
  form.inline { display:inline; margin:0; }
  .error { color:var(--danger); font-size:13px; margin-top:10px; min-height:1em; }
  .notice { padding:10px 12px; border-radius:8px; background:var(--soft); color:var(--text); font-size:14px; margin-bottom:16px; }
  .notice.bad { background:transparent; border:1px solid var(--danger); color:var(--danger); }
  .dev { margin-top:12px; padding:8px 10px; border-radius:8px; background:#fef3c7; color:#92400e; font-size:13px; }
  .plan { border:1px solid var(--border); border-radius:10px; padding:12px; margin-top:12px; }
  .plan b { font-size:16px; }
  .stats { display:grid; grid-template-columns:repeat(auto-fit, minmax(140px, 1fr)); gap:10px; }
  .stat { border:1px solid var(--border); border-radius:10px; padding:10px 12px; }
  .stat .k { color:var(--muted); font-size:12px; } .stat .v { font-size:20px; font-weight:600; }
  .stat .s { color:var(--muted); font-size:12px; }
  ul.rows { list-style:none; padding:0; margin:0; }
  ul.rows li { display:flex; justify-content:space-between; align-items:center; gap:12px; padding:9px 0; border-bottom:1px solid var(--border); font-size:14px; }
  ul.rows li:last-child { border-bottom:0; }
  ul.rows .muted, .muted { color:var(--muted); font-size:13px; }
  .danger-zone { border:1px solid var(--danger); border-radius:10px; padding:14px; margin-top:26px; }
  .danger-zone h2 { margin-top:0; color:var(--danger); }
  .hidden { display:none; }
</style>`

func htmlLang(l lang) string {
	if l == langZH {
		return "zh-CN"
	}
	return "en"
}

func page(l lang, title, body, head string) string {
	return `<!doctype html>
<html lang="` + htmlLang(l) + `">
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

func messagePage(l lang, title, message string) string {
	return page(l, title, "<h1>"+esc(title)+"</h1><p>"+esc(message)+"</p>", "")
}

type loginParams struct {
	DeviceID      string `json:"device_id"`
	RedirectURI   string `json:"redirect_uri"`
	State         string `json:"state"`
	CodeChallenge string `json:"code_challenge"`
	TurnstileKey  string `json:"-"`
}

// renderLoginPage 桌面端的邮箱验证码登录页。验证成功后跳回桌面端的本机回调地址。
func renderLoginPage(p loginParams, l lang) string {
	turnstile, head := "", ""
	if p.TurnstileKey != "" {
		turnstile = `<div class="cf-turnstile" data-sitekey="` + esc(p.TurnstileKey) + `" style="margin-top:12px"></div>`
		head = `<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>`
	}
	// json.Marshal 默认把 < > & 转义成 < 等，可以安全地嵌入 <script>
	params, _ := json.Marshal(p)
	text, _ := json.Marshal(map[string]string{
		"failed": tr(l, "login.failed"), "dev": tr(l, "otp.devCode", "%CODE%"),
		"done": tr(l, "login.done"), "doneMsg": tr(l, "login.doneMsg"),
	})

	body := `
<h1>{{HEADING}}</h1>
<p>{{INTRO}}</p>
<form id="step1">
  <label for="email">{{EMAIL}}</label>
  <input id="email" type="email" autocomplete="email" required placeholder="you@example.com" />
  {{TURNSTILE}}
  <button id="send" type="submit">{{SEND}}</button>
</form>
<form id="step2" class="hidden">
  <label for="code">{{SENTTO}} <span id="sentTo"></span></label>
  <input id="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required placeholder="{{CODEHINT}}" />
  <div id="dev" class="dev hidden"></div>
  <button id="verify" type="submit">{{SUBMIT}}</button>
  <button id="back" type="button" class="secondary">{{BACK}}</button>
</form>
<div id="err" class="error"></div>
<script>
const P = {{PARAMS}};
const T = {{TEXT}};
const $ = (id) => document.getElementById(id);
const show = (msg) => { $("err").textContent = msg || ""; };
async function post(url, body) {
  const r = await fetch(url, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error((data.error && data.error.message) || T.failed);
  return data;
}
$("step1").addEventListener("submit", async (e) => {
  e.preventDefault(); show(); $("send").disabled = true;
  try {
    const tokenEl = document.querySelector("[name=cf-turnstile-response]");
    const data = await post("/auth/email/start", { email: $("email").value, turnstile: tokenEl ? tokenEl.value : undefined });
    $("sentTo").textContent = $("email").value;
    if (data.dev_code) { $("dev").textContent = T.dev.replace("%CODE%", data.dev_code); $("dev").classList.remove("hidden"); }
    $("step1").classList.add("hidden"); $("step2").classList.remove("hidden"); $("code").focus();
  } catch (err) { show(err.message); if (window.turnstile) window.turnstile.reset(); }
  finally { $("send").disabled = false; }
});
$("step2").addEventListener("submit", async (e) => {
  e.preventDefault(); show(); $("verify").disabled = true;
  try {
    const data = await post("/auth/email/verify", { ...P, email: $("email").value, code: $("code").value.trim() });
    const main = document.querySelector("main");
    main.replaceChildren();
    const h = document.createElement("h1"); h.textContent = T.done;
    const p = document.createElement("p"); p.textContent = T.doneMsg;
    main.append(h, p);
    location.href = data.redirect;
  } catch (err) { show(err.message); }
  finally { $("verify").disabled = false; }
});
$("back").addEventListener("click", () => { $("step2").classList.add("hidden"); $("step1").classList.remove("hidden"); show(); });
</script>`
	body = strings.NewReplacer(
		"{{HEADING}}", esc(tr(l, "login.heading")),
		"{{INTRO}}", esc(tr(l, "login.intro", credits.SignupBonus)),
		"{{EMAIL}}", esc(tr(l, "login.email")),
		"{{SEND}}", esc(tr(l, "login.send")),
		"{{SENTTO}}", esc(tr(l, "login.sentTo")),
		"{{CODEHINT}}", esc(tr(l, "login.codeHint")),
		"{{SUBMIT}}", esc(tr(l, "login.submit")),
		"{{BACK}}", esc(tr(l, "login.back")),
		"{{TURNSTILE}}", turnstile,
		"{{PARAMS}}", string(params),
		"{{TEXT}}", string(text),
	).Replace(body)
	return page(l, tr(l, "login.title"), body, head)
}
