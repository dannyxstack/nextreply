//! 平台相关能力。业务代码只调用这里的函数，不直接使用系统 API。

#[cfg(windows)]
mod windows;
#[cfg(windows)]
pub use windows::*;

#[cfg(not(windows))]
mod fallback {
    /// macOS 上 overlay 使用不抢焦点的 NSPanel，不需要记录 / 还原前台窗口（待实现）。
    pub fn foreground_window() -> Option<isize> {
        None
    }

    pub fn restore_foreground(_handle: isize) {}

    /// macOS 不还原焦点，也就没有 ESC 漏给聊天软件的问题。
    pub fn is_escape_down() -> bool {
        false
    }
}
#[cfg(not(windows))]
pub use fallback::*;
