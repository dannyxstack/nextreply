import { getCurrentWindow } from "@tauri-apps/api/window";
import { useCallback, useEffect, useState } from "react";
import { accountLogin, accountLogout, accountStatus, billingOpen, onAccountChanged, type AccountStatus, type Bucket } from "../shared/ipc";

const BUCKET_LABEL: Record<Bucket, string> = {
  trial: "Trial",
  bonus: "Sign-up bonus",
  daily: "Free today",
  subscription: "Subscription",
  topup: "Top-up",
};

// 套餐名称属于界面文案，由客户端按界面语言显示（服务端返回的 label 只用于服务端页面）
const PLAN_LABEL: Record<string, string> = { trial: "Trial", free: "Free", pro: "Pro", pro_plus: "Pro+" };

const fmtDate = (ms: number) => new Date(ms).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" });

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
        {error ? <p className="account-error">Can't reach the service: {error}</p> : <small>Loading…</small>}
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
          <span className={`plan-badge plan-${plan.id}`}>{PLAN_LABEL[plan.id] ?? plan.label}</span>
        </div>
      ) : (
        <div className="account-row">
          <span>Not signed in · Free trial</span>
          <span className="plan-badge plan-trial">{PLAN_LABEL[plan.id] ?? plan.label}</span>
        </div>
      )}

      <div className="credits">
        <span className="credits-total">{credits.total}</span>
        <span className="credits-unit">replies left</span>
        <span className="credits-today">
          Today {credits.used_today} / {plan.daily_cap}
        </span>
      </div>
      {credits.buckets.length > 0 && (
        <ul className="buckets">
          {credits.buckets.map((b) => (
            <li key={b.bucket}>
              {BUCKET_LABEL[b.bucket]} {b.remaining}
              {b.expires_at && b.bucket !== "daily" ? ` (expires ${fmtDate(b.expires_at)})` : ""}
            </li>
          ))}
        </ul>
      )}

      {subscription && (
        <small className="sub-line">
          {subscription.cancel_at_period_end ? "Subscription ends on" : "Renews on"}{" "}
          {subscription.current_period_end ? fmtDate(subscription.current_period_end) : "—"}
          {subscription.status === "past_due" ? " · Payment failed, please update your payment method" : ""}
        </small>
      )}

      <div className="account-actions">
        {!status.logged_in && (
          <button className="primary" disabled={busy !== null} onClick={() => run("login", accountLogin)}>
            {busy === "login" ? "Finish signing in in your browser…" : "Sign in / Sign up (50 free replies)"}
          </button>
        )}

        {status.logged_in && !subscription &&
          billing.plans.map((p) => (
            <button key={p.id} className="primary" disabled={busy !== null} onClick={() => run("billing", () => billingOpen("checkout", p.id))}>
              Upgrade to {PLAN_LABEL[p.id] ?? p.label} · ${p.price_usd}/mo ({p.monthly_credits} replies)
            </button>
          ))}

        {status.logged_in && subscription && (
          <button className="secondary" disabled={busy !== null} onClick={() => run("billing", () => billingOpen("portal"))}>
            Manage subscription
          </button>
        )}

        {status.logged_in && (
          <button className="link" disabled={busy !== null} onClick={() => run("logout", accountLogout)}>
            Sign out
          </button>
        )}
      </div>

      {billing.dev && !billing.stripe && status.logged_in && <small>Dev mode: checkout is simulated, no real charges.</small>}
      {error && <p className="account-error">{error}</p>}
    </section>
  );
}
