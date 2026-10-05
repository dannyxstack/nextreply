// 与 Rust 侧 commands.rs / flow.rs 对应的类型和调用封装。
import { convertFileSrc, invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

// ---------- selector ----------

export interface SelectorFrame {
  session: number;
  monitor: number;
}

export interface CssRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export const frameUrl = (f: SelectorFrame) => `${convertFileSrc(String(f.monitor), "frame")}?s=${f.session}`;

export const selectorFrame = () => invoke<SelectorFrame | null>("selector_frame");
export const selectorReady = (session: number) => invoke<void>("selector_ready", { session });
export const selectionDone = (session: number, rect: CssRect) =>
  invoke<void>("selection_done", { session, rect, viewportW: window.innerWidth, viewportH: window.innerHeight });
export const cancelFlow = () => invoke<void>("cancel_flow");
export const onSelectorFrame = (cb: (f: SelectorFrame) => void): Promise<UnlistenFn> =>
  listen<SelectorFrame>("selector:frame", (e) => cb(e.payload));

// ---------- overlay ----------

export type ReplyStyle = "empathetic" | "funny" | "direct";

export interface ReplySuggestion {
  style: ReplyStyle | string;
  label: string;
  text: string;
}

export type AiErrorCode = "network" | "quota_exceeded" | "unavailable" | "insufficient_context" | "not_a_conversation";

export type OverlayPayload =
  | { kind: "loading"; session: number }
  | { kind: "result"; session: number; summary: string; replies: ReplySuggestion[] }
  | { kind: "error"; session: number; code: AiErrorCode }
  | { kind: "copied"; session: number };

export const overlayState = () => invoke<OverlayPayload | null>("overlay_state");
export const overlayResize = (height: number) => invoke<void>("overlay_resize", { height });
export const copyReply = (index: number) => invoke<void>("copy_reply", { index });
export const onOverlayState = (cb: (p: OverlayPayload) => void): Promise<UnlistenFn> =>
  listen<OverlayPayload>("overlay:state", (e) => cb(e.payload));

// ---------- settings ----------

export interface SettingsView {
  shortcut: string;
  default_shortcut: string;
  server_url: string;
  display_name: string;
  launch_at_login: boolean;
  version: string;
}

export interface SettingsInput {
  shortcut: string;
  server_url: string;
  display_name: string;
  launch_at_login: boolean;
}

export const getSettings = () => invoke<SettingsView>("get_settings");
export const saveSettings = (input: SettingsInput) => invoke<SettingsView>("save_settings", { input });
