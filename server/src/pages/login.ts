import { esc, page } from "./layout";

export interface LoginParams {
  deviceId: string;
  redirectUri: string;
  state: string;
  codeChallenge: string;
  turnstileSiteKey?: string;
}

/** 邮箱验证码登录页。验证成功后跳回桌面端的本机回调地址。 */
export function loginPage(p: LoginParams): string {
  const turnstile = p.turnstileSiteKey
    ? `<div class="cf-turnstile" data-sitekey="${esc(p.turnstileSiteKey)}" style="margin-top:12px"></div>`
    : "";
  const head = p.turnstileSiteKey ? `<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>` : "";
  const params = JSON.stringify({
    device_id: p.deviceId,
    redirect_uri: p.redirectUri,
    state: p.state,
    code_challenge: p.codeChallenge,
  }).replace(/</g, "\\u003c");

  return page(
    "登录",
    `
<h1>登录 NextReply</h1>
<p>输入邮箱获取验证码。新用户注册即送 50 次回复额度。</p>
<form id="step1">
  <label for="email">邮箱</label>
  <input id="email" type="email" autocomplete="email" required placeholder="you@example.com" />
  ${turnstile}
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
const P = ${params};
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
</script>`,
    head,
  );
}
