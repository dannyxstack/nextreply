//! 全局快捷键：主快捷键（触发截图）+ 流程进行中临时注册的 Escape（取消）。

use tauri::{AppHandle, Manager};
use tauri_plugin_global_shortcut::{Code, GlobalShortcutExt, Shortcut, ShortcutEvent, ShortcutState};

use crate::{flow, state::AppState};

#[derive(Default)]
pub struct HotkeyState {
    main: Option<Shortcut>,
    escape_on: bool,
}

fn escape() -> Shortcut {
    Shortcut::new(None, Code::Escape)
}

pub fn parse(accelerator: &str) -> Result<Shortcut, String> {
    accelerator.parse::<Shortcut>().map_err(|e| format!("无效的快捷键 \"{accelerator}\"：{e}"))
}

/// 注册（或更换）主快捷键。先注册新的，成功后再注销旧的，失败时保持原状。
pub fn register_main(app: &AppHandle, accelerator: &str) -> Result<(), String> {
    let shortcut = parse(accelerator)?;
    let state = app.state::<AppState>();
    let mut hk = state.hotkeys.lock().unwrap();
    if hk.main.map(|s| s.id()) == Some(shortcut.id()) {
        return Ok(());
    }
    app.global_shortcut()
        .register(shortcut)
        .map_err(|e| format!("快捷键 \"{accelerator}\" 注册失败，可能已被其他软件占用：{e}"))?;
    if let Some(old) = hk.main.replace(shortcut) {
        let _ = app.global_shortcut().unregister(old);
    }
    Ok(())
}

/// 流程进行中注册全局 Escape，保证窗口没拿到焦点时 ESC 也能取消；回到空闲状态时注销。
pub fn set_escape(app: &AppHandle, on: bool) {
    let state = app.state::<AppState>();
    let mut hk = state.hotkeys.lock().unwrap();
    if hk.escape_on == on {
        return;
    }
    let gs = app.global_shortcut();
    let ok = if on { gs.register(escape()).is_ok() } else { gs.unregister(escape()).is_ok() };
    if ok {
        hk.escape_on = on;
    } else {
        log::warn!("failed to {} global Escape", if on { "register" } else { "unregister" });
    }
}

pub fn handle(app: &AppHandle, shortcut: &Shortcut, event: ShortcutEvent) {
    if event.state != ShortcutState::Pressed {
        return;
    }
    let is_escape = shortcut.id() == escape().id();
    let is_main = app.state::<AppState>().hotkeys.lock().unwrap().main.map(|s| s.id()) == Some(shortcut.id());
    // 在独立线程中执行：流程里会注册 / 注销快捷键，不能在快捷键回调内同步进行
    let app = app.clone();
    if is_escape {
        tauri::async_runtime::spawn_blocking(move || flow::cancel(&app, flow::CloseReason::Escape));
    } else if is_main {
        tauri::async_runtime::spawn_blocking(move || flow::start(&app));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_default_shortcut() {
        assert!(parse(crate::storage::settings::DEFAULT_SHORTCUT).is_ok());
        assert!(parse("Alt+Shift+Space").is_ok());
        // 设置页录制的格式（KeyboardEvent.code）
        assert!(parse("CommandOrControl+Shift+KeyR").is_ok());
        assert!(parse("Alt+Digit1").is_ok());
        assert!(parse("NotAKey+").is_err());
    }
}
