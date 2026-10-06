mod account;
mod ai;
mod capture;
mod commands;
mod flow;
mod geom;
mod hotkey;
mod overlay;
mod platform;
mod selector;
mod state;
mod storage;
mod telemetry;
mod tray;

use tauri::{App, Manager, RunEvent, WindowEvent};
use tauri_plugin_autostart::MacosLauncher;

use crate::{state::AppState, storage::settings::Settings};

pub fn run() {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info,nextreply_lib=debug")).init();

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| commands::show_settings(app)))
        .plugin(tauri_plugin_global_shortcut::Builder::new().with_handler(hotkey::handle).build())
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        .register_asynchronous_uri_scheme_protocol("frame", capture::frames::protocol)
        .setup(setup)
        .on_window_event(|window, event| match (window.label(), event) {
            (commands::SETTINGS_LABEL, WindowEvent::CloseRequested { api, .. }) => {
                // 关闭设置窗口只是隐藏，应用继续常驻托盘
                api.prevent_close();
                let _ = window.hide();
            }
            (overlay::LABEL, WindowEvent::Focused(false)) => flow::on_overlay_blur(window.app_handle()),
            _ => {}
        })
        .invoke_handler(tauri::generate_handler![
            commands::selector_frame,
            commands::selector_ready,
            commands::selection_done,
            commands::cancel_flow,
            commands::overlay_state,
            commands::overlay_resize,
            commands::copy_reply,
            commands::get_settings,
            commands::save_settings,
            commands::account_status,
            commands::account_login,
            commands::account_logout,
            commands::billing_open,
            commands::open_account,
        ])
        .build(tauri::generate_context!())
        .expect("error while building NextReply")
        .run(|_app, event| {
            // 所有窗口都隐藏 / 关闭时不退出，只有托盘 Quit 才退出
            if let RunEvent::ExitRequested { api, code: None, .. } = event {
                api.prevent_exit();
            }
        });
}

fn setup(app: &mut App) -> Result<(), Box<dyn std::error::Error>> {
    #[cfg(target_os = "macos")]
    app.set_activation_policy(tauri::ActivationPolicy::Accessory);

    let handle = app.handle().clone();
    let settings_path = app.path().app_config_dir()?.join("settings.json");
    let data_dir = app.path().app_data_dir()?;

    let (settings, first_run) = Settings::load(&settings_path);
    if first_run {
        if let Err(e) = settings.save(&settings_path) {
            log::warn!("save initial settings: {e}");
        }
    }
    let shortcut = settings.shortcut.clone();
    app.manage(AppState::new(settings, settings_path, data_dir));

    tray::create(&handle)?;
    // 预先创建窗口，避免第一次按快捷键时等待 WebView 冷启动
    overlay::ensure(&handle)?;
    selector::prewarm(&handle);

    let hotkey_ok = match hotkey::register_main(&handle, &shortcut) {
        Ok(()) => true,
        Err(e) => {
            log::error!("{e}");
            false
        }
    };
    // 首次运行（展示隐私说明）或快捷键注册失败时打开设置窗口
    if first_run || !hotkey_ok {
        commands::show_settings(&handle);
    }
    log::info!("NextReply started, shortcut={shortcut}");
    Ok(())
}
