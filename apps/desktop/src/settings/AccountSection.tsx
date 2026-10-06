import { getCurrentWindow } from "@tauri-apps/api/window";
import { useCallback, useEffect, useState } from "react";
import { accountLogin, accountLogout, accountStatus, billingOpen, onAccountChanged, type AccountStatus, type Bucket } from "../shared/ipc";

const BUCKET_LABEL: Record<Bucket, string> = {
  trial: "体验",
  bonus: "注册赠送",
  daily: "今日免费",
  subscription: "订阅",
  topup: "加购",
};

const fmtDate = (ms: number) => new Date(ms).toLocaleDateString("zh-CN");

type Busy = null | "login" | "logout" | "billing";

export function AccountSection() {
  const [status, setStatus] = useState<AccountStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);

  const refresh = useCallback(async () => {
    try {
      setStatus(await accountStatus());
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useEffect(() => {
    refresh();
    const unlistenAccount = onAccountChanged(refresh);
    // 从浏览器付款回来时窗口重新获得焦点，顺便刷新套餐和积分
    const unlistenFocus = getCurrentWindow().onFocusChanged(({ payload }) => payload && refresh());
    return () => {
      unlistenAccount.then((fn) => fn());
      unlistenFocus.then((fn) => fn());
    };
  }, [refresh]);

  const run = async (kind: Busy, fn: () => Promise<void>) => {
    setBusy(kind);
    setError(null);
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  if (!status) {
    return (
      <section>
        <h2>Account</h2>
        {error ? <p className="account-error">无法连接服务：{error}</p> : <small>加载中…</small>}
      </section>
    );
  }

  const { plan, credits, subscription, billing } = status;

  return (
    <section>
      <h2>Account</h2>

      {status.logged_in && status.user ? (
        <div className="account-row">
          <span className="account-email">{status.user.email}</span>
          <span className={`plan-badge plan-${plan.id}`}>{plan.label}</span>
        </div>
      ) : (
        <div className="account-row">
          <span>未登录 · 体验模式</span>
          <span className="plan-badge plan-trial">{plan.label}</span>
        </div>
      )}

      <div className="credits">
        <span className="credits-total">{credits.total}</span>
        <span className="credits-unit">次可用</span>
        <span className="credits-today">
          今日已用 {credits.used_today} / {plan.daily_cap}
        </span>
      </div>
      {credits.buckets.length > 0 && (
        <ul className="buckets">
          {credits.buckets.map((b) => (
            <li key={b.bucket}>
              {BUCKET_LABEL[b.bucket]} {b.remaining}
              {b.expires_at && b.bucket !== "daily" ? `（${fmtDate(b.expires_at)} 到期）` : ""}
            </li>
          ))}
        </ul>
      )}

      {subscription && (
        <small className="sub-line">
          {subscription.cancel_at_period_end ? "订阅将于" : "下次续费"}{" "}
          {subscription.current_period_end ? fmtDate(subscription.current_period_end) : "—"}
          {subscription.cancel_at_period_end ? " 结束" : ""}
          {subscription.status === "past_due" ? " · 扣款失败，请更新付款方式" : ""}
        </small>
      )}

      <div className="account-actions">
        {!status.logged_in && (
          <button className="primary" disabled={busy !== null} onClick={() => run("login", accountLogin)}>
            {busy === "login" ? "请在浏览器中完成登录…" : "登录 / 注册（送 50 次）"}
          </button>
        )}

        {status.logged_in && !subscription &&
          billing.plans.map((p) => (
            <button key={p.id} className="primary" disabled={busy !== null} onClick={() => run("billing", () => billingOpen("checkout", p.id))}>
              升级 {p.label} · ${p.price_usd}/月（{p.monthly_credits} 次）
            </button>
          ))}

        {status.logged_in && subscription && (
          <button className="secondary" disabled={busy !== null} onClick={() => run("billing", () => billingOpen("portal"))}>
            管理订阅
          </button>
        )}

        {status.logged_in && (
          <button className="link" disabled={busy !== null} onClick={() => run("logout", accountLogout)}>
            退出登录
          </button>
        )}
      </div>

      {billing.dev && !billing.stripe && status.logged_in && <small>开发模式：支付为模拟流程，不会产生真实扣费。</small>}
      {error && <p className="account-error">{error}</p>}
    </section>
  );
}
