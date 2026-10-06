import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { cancelFlow, copyReply, onOverlayState, openAccount, overlayResize, overlayState, type AiErrorCode, type OverlayPayload } from "../shared/ipc";

const STILL_THINKING_AFTER_MS = 4000;

const STYLE_ICON: Record<string, string> = { empathetic: "😌", funny: "😄", direct: "⚡" };

// 需求文档 §3.9 的错误文案
const ERROR_TEXT: Record<AiErrorCode, [string, string?]> = {
  network: ["Network unavailable."],
  unavailable: ["Unable to analyze this conversation.", "Try again."],
  insufficient_context: ["Not enough conversation context.", "Try selecting a slightly larger area."],
  not_a_conversation: ["I couldn't confidently identify the conversation.", "Try selecting the chat area again."],
  quota_exceeded: ["Today's free replies are used up.", "Come back tomorrow."],
  login_required: ["体验次数已用完", "登录即送 50 次回复额度。"],
  insufficient_credits: ["积分已用完", "明天会补充免费额度，或升级 Pro 获得更多。"],
  daily_cap: ["今天的使用次数已达上限", "明天再来，或升级套餐提高上限。"],
  rate_limited: ["请求太频繁了", "稍等几秒再试。"],
};

/** 这些错误需要用户去账户页处理（登录 / 升级） */
const ACCOUNT_ACTION: Partial<Record<AiErrorCode, string>> = {
  login_required: "登录领取额度",
  insufficient_credits: "查看账户 / 升级",
  daily_cap: "查看账户 / 升级",
};

export function Overlay() {
  const [state, setState] = useState<OverlayPayload | null>(null);
  const [stillThinking, setStillThinking] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    overlayState().then((s) => s && setState(s));
    const unlisten = onOverlayState(setState);
    return () => {
      unlisten.then((fn) => fn());
    };
  }, []);

  // Loading 超过 4 秒显示 "Still thinking..."
  useEffect(() => {
    setStillThinking(false);
    if (state?.kind !== "loading") return;
    const t = window.setTimeout(() => setStillThinking(true), STILL_THINKING_AFTER_MS);
    return () => window.clearTimeout(t);
  }, [state?.kind, state?.session]);

  // 把内容高度告诉 Rust，由 Rust 调整窗口尺寸并重新定位
  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    const report = () => overlayResize(Math.ceil(el.getBoundingClientRect().height));
    report();
    const ro = new ResizeObserver(report);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") return void cancelFlow();
      if (state?.kind === "result") {
        const i = Number.parseInt(e.key, 10) - 1;
        if (i >= 0 && i < state.replies.length) copyReply(i);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [state]);

  return (
    <div className="overlay-root" ref={rootRef}>
      <div className="card">{state && <Body state={state} stillThinking={stillThinking} />}</div>
    </div>
  );
}

function Body({ state, stillThinking }: { state: OverlayPayload; stillThinking: boolean }) {
  switch (state.kind) {
    case "loading":
      return (
        <div className="status">
          <span className="spinner" />
          <span>{stillThinking ? "Still thinking..." : "Analyzing conversation..."}</span>
          <Footer />
        </div>
      );
    case "copied":
      return <div className="copied">Copied ✓</div>;
    case "error": {
      const [title, hint] = ERROR_TEXT[state.code] ?? ERROR_TEXT.unavailable;
      return (
        <div className="status error">
          <div className="error-title">{title}</div>
          {hint && <div className="error-hint">{hint}</div>}
          {ACCOUNT_ACTION[state.code] && (
            <button className="action" onClick={() => openAccount()}>
              {ACCOUNT_ACTION[state.code]}
            </button>
          )}
          <Footer />
        </div>
      );
    }
    case "result":
      return (
        <>
          {state.summary && <div className="summary">{state.summary}</div>}
          <ul className="replies">
            {state.replies.map((r, i) => (
              <li key={i}>
                <button className="reply" onClick={() => copyReply(i)}>
                  <span className="reply-head">
                    <span className="reply-label">
                      {STYLE_ICON[r.style] ?? "💬"} {r.label}
                    </span>
                    <kbd>{i + 1}</kbd>
                  </span>
                  <span className="reply-text">{r.text}</span>
                </button>
              </li>
            ))}
          </ul>
          <Footer withKeys remaining={state.credits_remaining} />
        </>
      );
  }
}

function Footer({ withKeys = false, remaining }: { withKeys?: boolean; remaining?: number | null }) {
  const keys = withKeys ? "点击或按 1–3 复制 · Esc 关闭" : "Esc 关闭";
  return <div className="footer">{remaining != null ? `剩余 ${remaining} 次 · ${keys}` : keys}</div>;
}
