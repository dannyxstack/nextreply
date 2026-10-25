// 价格页：从服务端 GET /v1/plans 读取套餐并渲染，数值始终与服务端规则一致（TECH_DESIGN §10.2）。
// 请求失败时只显示提示，不显示可能过期的数字。
(function () {
  const root = document.getElementById("plans");
  if (!root) return;
  const api = document.body.dataset.api;
  const lang = document.body.dataset.lang === "zh" ? "zh" : "en";
  const home = lang === "zh" ? "/zh/" : "/";

  const T = {
    en: {
      names: { free: "Free", pro: "Pro", pro_plus: "Pro+" },
      month: "/ month",
      free: (p, d) => [
        `${p.daily_refill} replies every day`,
        `+${d.signup_bonus} bonus replies when you sign up (valid ${d.signup_bonus_days} days)`,
        "Sign in with your email — no card needed",
      ],
      paid: (p) => [
        `${p.monthly_credits.toLocaleString("en")} replies per month`,
        `Up to ${p.daily_cap} replies per day`,
        "Unused replies expire at the end of each billing period",
        "Cancel anytime",
      ],
      freeCta: "Download free",
      paidCta: "Upgrade in the app",
      paidNote: "Upgrade from the app: Settings → Account.",
      trial: (d) => `Not ready to sign up? Install the app and try ${d.trial_credits} replies without an account.`,
      loading: "Loading plans…",
      error: "Couldn't load plans right now. Please refresh the page or check again later.",
    },
    zh: {
      names: { free: "免费", pro: "Pro", pro_plus: "Pro+" },
      month: "/ 月",
      free: (p, d) => [
        `每天 ${p.daily_refill} 次回复`,
        `注册即送 ${d.signup_bonus} 次（${d.signup_bonus_days} 天内有效）`,
        "邮箱登录，无需绑卡",
      ],
      paid: (p) => [
        `每月 ${p.monthly_credits} 次回复`,
        `每天最多 ${p.daily_cap} 次`,
        "未用完的次数在每个计费周期结束时失效",
        "随时取消",
      ],
      freeCta: "免费下载",
      paidCta: "在应用内升级",
      paidNote: "在应用的 Settings → Account 中升级。",
      trial: (d) => `还不想注册？安装后不登录也能先试用 ${d.trial_credits} 次。`,
      loading: "正在加载套餐…",
      error: "暂时无法加载套餐，请刷新页面或稍后再试。",
    },
  }[lang];

  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  };

  function price(p) {
    const box = el("div", "price", p.price_usd ? `$${p.price_usd}` : "$0");
    if (p.price_usd) box.append(" ", el("small", null, T.month));
    return box;
  }

  function card(p, data) {
    const c = el("article", "plan" + (p.id === "pro" ? " featured" : ""));
    c.append(el("h3", null, T.names[p.id] || p.id), price(p));
    const ul = el("ul");
    for (const line of p.id === "free" ? T.free(p, data) : T.paid(p)) ul.append(el("li", null, line));
    c.append(ul);
    const a = el("a", "btn" + (p.id === "free" ? "" : " primary"), p.id === "free" ? T.freeCta : T.paidCta);
    a.href = home + "download/";
    if (p.id !== "free") a.title = T.paidNote;
    c.append(a);
    return c;
  }

  root.replaceChildren(el("p", "loading", T.loading));
  fetch(api.replace(/\/$/, "") + "/v1/plans", { headers: { accept: "application/json" } })
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error(r.status))))
    .then((data) => {
      root.replaceChildren(...data.plans.map((p) => card(p, data)));
      const trial = document.getElementById("trial");
      if (trial) trial.textContent = T.trial(data);
      const note = document.getElementById("paid-note");
      if (note) note.textContent = T.paidNote;
    })
    .catch(() => root.replaceChildren(el("p", "error", T.error)));
})();
