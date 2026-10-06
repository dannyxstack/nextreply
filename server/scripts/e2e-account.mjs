// 账户系统端到端测试：体验额度 → 邮箱登录（PKCE）→ 免费额度 → 模拟订阅 → 结束订阅 → 防刷规则 → 刷新令牌。
// 前提：本地服务以 DEV_MODE=true 且 MOCK_AI=true 运行（见 server/CLAUDE.md）。会消耗本机 IP 的每日限额，跑完用 npm run reset:limits 清理。
import { createHash, randomBytes, randomUUID } from "node:crypto";

const B = process.env.E2E_BASE_URL ?? "http://127.0.0.1:8787";
const b64url = (buf) => buf.toString("base64url");
let failures = 0;
const check = (name, cond, extra = "") => {
  console.log(`${cond ? "PASS" : "FAIL"}  ${name}${extra ? "  — " + extra : ""}`);
  if (!cond) failures++;
};
async function call(method, path, { token, body, form, headers = {} } = {}) {
  const h = { ...headers };
  if (token) h.authorization = `Bearer ${token}`;
  let payload;
  if (body) { h["content-type"] = "application/json"; payload = JSON.stringify(body); }
  if (form) { h["content-type"] = "application/x-www-form-urlencoded"; payload = new URLSearchParams(form).toString(); }
  const r = await fetch(B + path, { method, headers: h, body: payload, redirect: "manual" });
  const text = await r.text();
  let json = null;
  try { json = JSON.parse(text); } catch {}
  return { status: r.status, json, text, location: r.headers.get("location") };
}
const IMG = randomBytes(64).toString("base64"); // MOCK_AI 下不会真正解析图片
const reply = (token, key) => call("POST", "/v1/reply", { token, body: { image: IMG, media_type: "image/jpeg" }, headers: key ? { "idempotency-key": key } : {} });

async function login(deviceId, email) {
  const verifier = b64url(randomBytes(32));
  const challenge = b64url(createHash("sha256").update(verifier).digest());
  const redirect = "http://127.0.0.1:53999/callback";
  const page = await call("GET", `/auth/login?device_id=${deviceId}&redirect_uri=${encodeURIComponent(redirect)}&state=st1&code_challenge=${challenge}`);
  check("login page renders", page.status === 200 && page.text.includes("登录 NextReply"));
  const start = await call("POST", "/auth/email/start", { body: { email } });
  check("otp start returns dev code", start.status === 200 && /^\d{6}$/.test(start.json?.dev_code ?? ""), JSON.stringify(start.json));
  const bad = await call("POST", "/auth/email/verify", { body: { email, code: "000000", device_id: deviceId, redirect_uri: redirect, state: "st1", code_challenge: challenge } });
  check("wrong otp rejected", bad.status === 400);
  const ok = await call("POST", "/auth/email/verify", { body: { email, code: start.json.dev_code, device_id: deviceId, redirect_uri: redirect, state: "st1", code_challenge: challenge } });
  const url = new URL(ok.json?.redirect ?? "http://x/");
  check("verify redirects to loopback with code+state", url.origin === "http://127.0.0.1:53999" && url.searchParams.get("state") === "st1");
  const code = url.searchParams.get("code");
  const wrongPkce = await call("POST", "/v1/auth/token", { body: { code, code_verifier: b64url(randomBytes(32)), device_id: deviceId } });
  check("token exchange with wrong PKCE verifier rejected", wrongPkce.status === 401);
  // 授权码被错误 verifier 尝试后仍然有效（未被标记 used），用正确 verifier 换取
  const tok = await call("POST", "/v1/auth/token", { body: { code, code_verifier: verifier, device_id: deviceId } });
  check("token exchange succeeds", tok.status === 200 && tok.json?.access_token && tok.json?.refresh_token, String(tok.status));
  const replay = await call("POST", "/v1/auth/token", { body: { code, code_verifier: verifier, device_id: deviceId } });
  check("auth code is single-use", replay.status === 401);
  return tok.json;
}

// ---------- 1. 匿名体验 ----------
const dev1 = randomUUID();
const hw1 = createHash("sha256").update("machine-1-" + randomUUID()).digest("hex");
const reg = await call("POST", "/v1/device/register", { body: { device_id: dev1, hw_hash: hw1 } });
check("device register", reg.status === 200 && reg.json?.token);
const devToken = reg.json.token;
let me = await call("GET", "/v1/me", { token: devToken });
check("trial: 10 credits, no user", me.json?.plan?.id === "trial" && me.json?.credits?.total === 10 && me.json?.user === null, JSON.stringify(me.json?.credits));

const r1 = await reply(devToken, "idem-key-0001");
check("trial reply charges 1", r1.status === 200 && r1.json?.credits?.remaining === 9, JSON.stringify(r1.json?.credits ?? r1.json));
const r1b = await reply(devToken, "idem-key-0001");
check("retry with same idempotency key does not double charge", r1b.json?.credits?.remaining === 9, JSON.stringify(r1b.json?.credits));

// 同一台机器换了 device_id（重装）不再发体验额度
const dev1b = randomUUID();
const reg2 = await call("POST", "/v1/device/register", { body: { device_id: dev1b, hw_hash: hw1 } });
me = await call("GET", "/v1/me", { token: reg2.json.token });
check("reinstall on same machine gets no new trial", me.json?.credits?.total === 0, JSON.stringify(me.json?.credits));
const noCredit = await reply(reg2.json.token);
check("no credits → 402 with login_required", noCredit.status === 402 && noCredit.json?.error?.details?.login_required === true, JSON.stringify(noCredit.json));

// ---------- 2. 登录 → 免费套餐 ----------
const email = `Tester${Date.now()}+promo@Example.com`;
const t1 = await login(dev1, email);
me = await call("GET", "/v1/me", { token: t1.access_token });
check("free plan after signup: bonus 50 + daily 5", me.json?.plan?.id === "free" && me.json?.credits?.total === 55 && me.json?.user?.email === email, JSON.stringify(me.json?.credits));
const r2 = await reply(t1.access_token);
check("free reply charges daily bucket first", r2.status === 200 && r2.json?.credits?.remaining === 54);
me = await call("GET", "/v1/me", { token: t1.access_token });
check("daily bucket decremented", me.json?.credits?.buckets?.find((b) => b.bucket === "daily")?.remaining === 4, JSON.stringify(me.json?.credits?.buckets));

// ---------- 4. 模拟订阅 ----------
const t4 = t1;
const link = await call("POST", "/v1/billing/link", { token: t4.access_token, body: { purpose: "checkout", plan: "pro" } });
check("checkout link issued", link.status === 200 && link.json?.url?.includes("/billing/checkout?ticket="));
const checkout = await call("GET", new URL(link.json.url).pathname + new URL(link.json.url).search);
check("mock checkout page", checkout.status === 200 && checkout.text.includes("模拟支付成功"));
const ticketReuse = await call("GET", new URL(link.json.url).pathname + new URL(link.json.url).search);
check("checkout ticket is single-use", ticketReuse.status === 400);
const confirm = checkout.text.match(/name="ticket" value="([^"]+)"/)?.[1];
const done = await call("POST", "/billing/dev/complete", { form: { ticket: confirm, plan: "pro" } });
check("mock payment completes", done.status === 303);
me = await call("GET", "/v1/me", { token: t4.access_token });
check("pro plan with 1000 subscription credits", me.json?.plan?.id === "pro" && me.json?.subscription?.status === "active" &&
  me.json?.credits?.buckets?.find((b) => b.bucket === "subscription")?.remaining === 1000, JSON.stringify(me.json?.credits?.buckets));
const devAnon = await call("POST", "/v1/billing/link", { token: devToken, body: { purpose: "checkout", plan: "pro" } });
check("anonymous device cannot buy", devAnon.status === 401);

// ---------- 5. 结束订阅 ----------
const portal = await call("POST", "/v1/billing/link", { token: t4.access_token, body: { purpose: "portal" } });
const portalPage = await call("GET", new URL(portal.json.url).pathname + new URL(portal.json.url).search);
const expireTicket = portalPage.text.match(/action="\/billing\/dev\/expire"><input type="hidden" name="ticket" value="([^"]+)"/)?.[1];
const exp = await call("POST", "/billing/dev/expire", { form: { ticket: expireTicket } });
check("mock expire", exp.status === 200);
me = await call("GET", "/v1/me", { token: t4.access_token });
check("back to free, subscription credits revoked", me.json?.plan?.id === "free" && !me.json?.credits?.buckets?.some((b) => b.bucket === "subscription"), JSON.stringify(me.json?.credits?.buckets));

// ---------- 6. 频率限制 ----------
const burst = await Promise.all(Array.from({ length: 8 }, () => reply(t4.access_token)));
const codes = burst.map((r) => r.status);
check("burst of 8 requests is rate limited", codes.includes(429) && codes.filter((s) => s === 200).length <= 6, codes.join(","));

// ---------- 7. 其他防护 ----------
const disposable = await call("POST", "/auth/email/start", { body: { email: "x@mailinator.com" } });
check("disposable email rejected", disposable.status === 400);
const badRedirect = await call("GET", `/auth/login?device_id=${dev1}&redirect_uri=${encodeURIComponent("https://evil.example/callback")}&state=s&code_challenge=c`);
check("non-loopback redirect_uri rejected", badRedirect.status === 400);
const fresh = `resend+${Date.now()}@example.com`;
await call("POST", "/auth/email/start", { body: { email: fresh } });
const resend = await call("POST", "/auth/email/start", { body: { email: fresh } });
check("otp resend within 1 minute rejected", resend.status === 429);

// ---------- 3. 刷新令牌轮换 + 重用检测 ----------
const rf = await call("POST", "/v1/auth/refresh", { body: { refresh_token: t1.refresh_token, device_id: dev1 } });
check("refresh rotates", rf.status === 200 && rf.json?.refresh_token && rf.json.refresh_token !== t1.refresh_token);
const reuse = await call("POST", "/v1/auth/refresh", { body: { refresh_token: t1.refresh_token, device_id: dev1 } });
check("reusing old refresh token is rejected", reuse.status === 401 && reuse.json?.error?.details?.reason === "refresh_reused");
const afterReuse = await call("POST", "/v1/auth/refresh", { body: { refresh_token: rf.json.refresh_token, device_id: dev1 } });
check("whole token family revoked after reuse", afterReuse.status === 401);
const wrongDevice = await call("POST", "/v1/auth/refresh", { body: { refresh_token: "x".repeat(43), device_id: dev1 } });
check("invalid refresh token rejected", wrongDevice.status === 401);

// 同一台设备再注册第二个账号：不再送注册奖励
const t2 = await login(dev1, `second${Date.now()}@example.com`);
me = await call("GET", "/v1/me", { token: t2.access_token });
check("second account on same device gets no signup bonus", me.json?.credits?.total === 5, JSON.stringify(me.json?.credits));

console.log(failures === 0 ? "\nALL PASSED" : `\n${failures} FAILED`);
process.exit(failures ? 1 : 0);
