use std::{ffi::c_void, mem::size_of};

use windows_sys::Win32::{
    Foundation::{HWND, LPARAM, RECT},
    Graphics::Dwm::{DwmGetWindowAttribute, DWMWA_CLOAKED, DWMWA_EXTENDED_FRAME_BOUNDS},
    UI::{
        Input::KeyboardAndMouse::{GetAsyncKeyState, VK_ESCAPE},
        WindowsAndMessaging::{
            EnumWindows, GetClassNameW, GetForegroundWindow, GetWindowLongW, GetWindowRect, GetWindowThreadProcessId,
            IsIconic, IsWindow, IsWindowVisible, SetForegroundWindow, GWL_EXSTYLE, WS_EX_TRANSPARENT,
        },
    },
};

use crate::geom::Rect;

/// ESC 键当前是否处于按下状态（物理按键状态，不依赖焦点）。
pub fn is_escape_down() -> bool {
    (unsafe { GetAsyncKeyState(VK_ESCAPE as i32) } as u16 & 0x8000) != 0
}

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

/// 本机的稳定标识（Windows 安装时生成的 MachineGuid，重装应用不变）。只用于防刷，发送前会加盐哈希。
pub fn machine_id() -> Option<String> {
    use windows_sys::Win32::System::Registry::{RegGetValueW, HKEY_LOCAL_MACHINE, RRF_RT_REG_SZ};
    let key: Vec<u16> = "SOFTWARE\\Microsoft\\Cryptography\0".encode_utf16().collect();
    let value: Vec<u16> = "MachineGuid\0".encode_utf16().collect();
    let mut buf = [0u16; 64];
    let mut size = (buf.len() * 2) as u32;
    let rc = unsafe {
        RegGetValueW(
            HKEY_LOCAL_MACHINE,
            key.as_ptr(),
            value.as_ptr(),
            RRF_RT_REG_SZ,
            std::ptr::null_mut(),
            buf.as_mut_ptr() as *mut c_void,
            &mut size,
        )
    };
    if rc != 0 {
        return None;
    }
    let len = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
    Some(String::from_utf16_lossy(&buf[..len])).filter(|s| !s.is_empty())
}

/// 太小的窗口（托盘弹出的小控件、1px 的辅助窗口等）不参与识别
const MIN_WINDOW_SIZE: i32 = 40;

/// 当前所有可见顶层窗口的可见区域（全局物理像素），按叠放顺序排列，最上层在前。
/// 在截图的同一时刻调用，用于冻结画面上的悬停识别。
pub fn visible_windows() -> Vec<Rect> {
    unsafe extern "system" fn collect(hwnd: HWND, lparam: LPARAM) -> i32 {
        let out = &mut *(lparam as *mut Vec<Rect>);
        if let Some(r) = window_rect(hwnd) {
            out.push(r);
        }
        1 // 继续枚举
    }
    let mut out: Vec<Rect> = Vec::new();
    // EnumWindows 按 Z 序从上到下回调
    unsafe { EnumWindows(Some(collect), &mut out as *mut Vec<Rect> as LPARAM) };
    out
}

unsafe fn window_rect(hwnd: HWND) -> Option<Rect> {
    if IsWindowVisible(hwnd) == 0 || IsIconic(hwnd) != 0 {
        return None;
    }
    // 排除本应用自己的窗口（框选窗口、overlay、设置窗口）
    let mut pid = 0u32;
    GetWindowThreadProcessId(hwnd, &mut pid);
    if pid == std::process::id() {
        return None;
    }
    // 状态为可见但实际被系统隐藏的窗口：挂起的 UWP 应用、其他虚拟桌面上的窗口
    let mut cloaked = 0u32;
    let hr = DwmGetWindowAttribute(hwnd, DWMWA_CLOAKED as u32, &mut cloaked as *mut u32 as *mut c_void, size_of::<u32>() as u32);
    if hr == 0 && cloaked != 0 {
        return None;
    }
    // 鼠标穿透的覆盖层（各种悬浮提示、录屏标记）
    if GetWindowLongW(hwnd, GWL_EXSTYLE) as u32 & WS_EX_TRANSPARENT != 0 {
        return None;
    }
    // 桌面本身不作为可选窗口
    let mut class = [0u16; 32];
    let len = GetClassNameW(hwnd, class.as_mut_ptr(), class.len() as i32).max(0) as usize;
    let class = String::from_utf16_lossy(&class[..len]);
    if class == "Progman" || class == "WorkerW" {
        return None;
    }
    // 用 DWM 的可见边界：GetWindowRect 在 Win10+ 会包含四周看不见的缩放边框（约 7–11px）
    let mut r: RECT = std::mem::zeroed();
    let hr = DwmGetWindowAttribute(
        hwnd,
        DWMWA_EXTENDED_FRAME_BOUNDS as u32,
        &mut r as *mut RECT as *mut c_void,
        size_of::<RECT>() as u32,
    );
    if hr != 0 && GetWindowRect(hwnd, &mut r) == 0 {
        return None;
    }
    let (w, h) = (r.right - r.left, r.bottom - r.top);
    (w >= MIN_WINDOW_SIZE && h >= MIN_WINDOW_SIZE).then(|| Rect::new(r.left, r.top, w, h))
}
