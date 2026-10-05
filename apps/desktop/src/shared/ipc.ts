// 与 Rust 侧 commands.rs / flow.rs 对应的类型和调用封装。
import { convertFileSrc, invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { getCurrentWebviewWindow } from "@tauri-apps/api/webviewWindow";

// ---------- selector ----------

/** Rust 侧的物理像素矩形（显示器内局部坐标） */
export interface PhysRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface SelectorFrame {
  session: number;
  monitor: number;
  /** 冻结画面的物理像素尺寸 */
  width: number;
  height: number;
  /** 截图时刻的顶层窗口，最上层在前 */
  windows: PhysRect[];
  /** 截图时鼠标在本显示器上的位置（物理像素） */
  cursor: [number, number] | null;
}

export interface CssRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export const frameUrl = (f: SelectorFrame) => `${convertFileSrc(String(f.monitor), "frame")}?s=${f.session}`;

/** 当前 selector 窗口负责的显示器编号（窗口标签为 selector-<n>）。 */
export const ownMonitor = (): number => Number(getCurrentWebviewWindow().label.replace("selector-", ""));

export const selectorFrame = () => invoke<SelectorFrame | null>("selector_frame");
export const selectorReady = (f: SelectorFrame) => invoke<void>("selector_ready", { session: f.session, monitor: f.monitor });
export const selectionDone = (session: number, rect: CssRect) =>
  invoke<void>("selection_done", { session, rect, viewportW: window.innerWidth, viewportH: window.innerHeight });
export const cancelFlow = () => invoke<void>("cancel_flow");
// 全局 listen() 会收到发给所有窗口的事件（emit_to 的目标不做过滤），
// 必须只接受属于本窗口显示器的帧，否则多屏时会显示别的屏幕的画面。
export const onSelectorFrame = (cb: (f: SelectorFrame) => void): Promise<UnlistenFn> => {
  const mine = ownMonitor();
  return listen<SelectorFrame>("selector:frame", (e) => {
    if (e.payload.monitor === mine) cb(e.payload);
  });
};

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
