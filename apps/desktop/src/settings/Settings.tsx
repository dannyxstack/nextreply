import { useEffect, useState, type KeyboardEvent } from "react";
import { AccountSection } from "./AccountSection";
import { getSettings, saveSettings, type SettingsInput, type SettingsView } from "../shared/ipc";

const isMac = navigator.userAgent.includes("Mac");
const MODIFIER_KEYS = new Set(["Control", "Shift", "Alt", "Meta"]);

/** 把按键事件转换成 Tauri 快捷键格式，如 "CommandOrControl+Shift+KeyR"。只按修饰键时返回 null。 */
function toAccelerator(e: KeyboardEvent): string | null {
  if (MODIFIER_KEYS.has(e.key)) return null;
  const parts: string[] = [];
  if (isMac ? e.metaKey : e.ctrlKey) parts.push("CommandOrControl");
  if (isMac && e.ctrlKey) parts.push("Control");
  if (!isMac && e.metaKey) parts.push("Super");
  if (e.altKey) parts.push("Alt");
  if (e.shiftKey) parts.push("Shift");
  if (parts.length === 0) return null; // 全局快捷键必须带修饰键
  parts.push(e.code);
  return parts.join("+");
}

/** 显示用：CommandOrControl+Shift+KeyR → Ctrl + Shift + R */
function prettyAccelerator(acc: string): string {
  return acc
    .split("+")
    .map((p) => {
      if (p === "CommandOrControl") return isMac ? "⌘" : "Ctrl";
      if (p.startsWith("Key")) return p.slice(3);
      if (p.startsWith("Digit")) return p.slice(5);
      return p;
    })
    .join(" + ");
}

const toInput = (s: SettingsView): SettingsInput => ({
  shortcut: s.shortcut,
  server_url: s.server_url,
  display_name: s.display_name,
  launch_at_login: s.launch_at_login,
});

export function Settings() {
  const [view, setView] = useState<SettingsView | null>(null);
  const [form, setForm] = useState<SettingsInput | null>(null);
  const [recording, setRecording] = useState(false);
  const [message, setMessage] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    getSettings().then((s) => {
      setView(s);
      setForm(toInput(s));
    });
  }, []);

  if (!view || !form) return null;

  const update = (patch: Partial<SettingsInput>) => {
    setForm({ ...form, ...patch });
    setMessage(null);
  };

  const onSave = async () => {
    setSaving(true);
    try {
      const saved = await saveSettings(form);
      setView(saved);
      setForm(toInput(saved));
      setMessage({ kind: "ok", text: "Saved" });
    } catch (e) {
      setMessage({ kind: "error", text: String(e) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className="settings">
      <header>
        <h1>NextReply</h1>
        <span className="version">v{view.version}</span>
      </header>

      <AccountSection />

      <section>
        <h2>General</h2>
        <label className="field">
          <span>Global shortcut</span>
          <input
            className={recording ? "recording" : ""}
            readOnly
            value={recording ? "Press the new shortcut…" : prettyAccelerator(form.shortcut)}
            onFocus={() => setRecording(true)}
            onBlur={() => setRecording(false)}
            onKeyDown={(e) => {
              e.preventDefault();
              if (e.key === "Escape") return void e.currentTarget.blur();
              const acc = toAccelerator(e);
              if (acc) {
                update({ shortcut: acc });
                e.currentTarget.blur();
              }
            }}
          />
          <small>
            Click, then press a key combination. Default: {prettyAccelerator(view.default_shortcut)} (overrides the browser's hard reload).
            {form.shortcut !== view.default_shortcut && (
              <button className="link" onClick={() => update({ shortcut: view.default_shortcut })}>
                Reset to default
              </button>
            )}
          </small>
        </label>
        <label className="field">
          <span>Your display name in chats (optional)</span>
          <input
            value={form.display_name}
            maxLength={64}
            placeholder="e.g. Danny"
            onChange={(e) => update({ display_name: e.target.value })}
          />
          <small>Helps the AI tell which messages are yours in left-aligned chats like Slack or Discord.</small>
        </label>
      </section>

      <section>
        <h2>AI</h2>
        <label className="field">
          <span>Server URL</span>
          <input value={form.server_url} onChange={(e) => update({ server_url: e.target.value })} spellCheck={false} />
          <small>Uses the built-in NextReply service. Custom API keys are not supported yet.</small>
        </label>
      </section>

      <section>
        <h2>Privacy</h2>
        <label className="check">
          <input type="checkbox" checked disabled />
          <span>Do not save screenshots</span>
        </label>
        <p className="privacy">
          Screenshots are analyzed only when you trigger the shortcut. They are not continuously recorded.
          <br />
          Screenshots stay in memory, are relayed through the NextReply service to the AI provider for analysis, and are discarded right after. They are never written to disk or stored on the server.
        </p>
      </section>

      <section>
        <h2>Startup</h2>
        <label className="check">
          <input type="checkbox" checked={form.launch_at_login} onChange={(e) => update({ launch_at_login: e.target.checked })} />
          <span>Launch at login</span>
        </label>
      </section>

      <footer>
        {message && <span className={`message ${message.kind}`}>{message.text}</span>}
        <button className="primary" onClick={onSave} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </button>
      </footer>
    </main>
  );
}
