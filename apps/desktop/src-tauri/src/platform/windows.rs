use windows_sys::Win32::{
    Foundation::HWND,
    UI::WindowsAndMessaging::{GetForegroundWindow, IsWindow, SetForegroundWindow},
};

/// 记录当前前台窗口（按下快捷键时调用，此时前台还是聊天软件）。
pub fn foreground_window() -> Option<isize> {
    let hwnd = unsafe { GetForegroundWindow() };
    if hwnd.is_null() {
        None
    } else {
        Some(hwnd as isize)
    }
}

/// 复制完成后把焦点还给聊天软件，用户可以直接 Ctrl+V。
pub fn restore_foreground(handle: isize) {
    let hwnd = handle as HWND;
    unsafe {
        if IsWindow(hwnd) != 0 {
            SetForegroundWindow(hwnd);
        }
    }
}
