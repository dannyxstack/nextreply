use serde::{Deserialize, Serialize};

/// 物理像素矩形（全局虚拟桌面坐标，或某个显示器内的局部坐标，视上下文而定）。
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Rect {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

impl Rect {
    pub const fn new(x: i32, y: i32, w: i32, h: i32) -> Self {
        Self { x, y, w, h }
    }

    pub fn right(&self) -> i32 {
        self.x + self.w
    }

    pub fn bottom(&self) -> i32 {
        self.y + self.h
    }

    pub fn center(&self) -> (i32, i32) {
        (self.x + self.w / 2, self.y + self.h / 2)
    }

    pub fn contains_point(&self, x: i32, y: i32) -> bool {
        x >= self.x && x < self.right() && y >= self.y && y < self.bottom()
    }

    pub fn contains_rect(&self, other: &Rect) -> bool {
        other.x >= self.x && other.y >= self.y && other.right() <= self.right() && other.bottom() <= self.bottom()
    }

    pub fn offset(&self, dx: i32, dy: i32) -> Rect {
        Rect::new(self.x + dx, self.y + dy, self.w, self.h)
    }

    /// 两个矩形的交集；不相交时返回 None。
    pub fn intersect(&self, other: &Rect) -> Option<Rect> {
        let x0 = self.x.max(other.x);
        let y0 = self.y.max(other.y);
        let x1 = self.right().min(other.right());
        let y1 = self.bottom().min(other.bottom());
        (x1 > x0 && y1 > y0).then(|| Rect::new(x0, y0, x1 - x0, y1 - y0))
    }
}

/// 把全局窗口矩形（按叠放顺序，最上层在前）裁剪到某个显示器内，并转换成显示器内的局部坐标。
/// 裁剪后太小的部分丢弃，顺序保持不变，前端按顺序命中即可得到最上层窗口。
pub fn windows_on_monitor(windows: &[Rect], monitor: Rect, min_size: i32) -> Vec<Rect> {
    windows
        .iter()
        .filter_map(|w| w.intersect(&monitor))
        .filter(|r| r.w >= min_size && r.h >= min_size)
        .map(|r| r.offset(-monitor.x, -monitor.y))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn intersects() {
        let a = Rect::new(0, 0, 100, 100);
        assert_eq!(a.intersect(&Rect::new(50, 50, 100, 100)), Some(Rect::new(50, 50, 50, 50)));
        assert_eq!(a.intersect(&Rect::new(100, 0, 10, 10)), None);
    }

    #[test]
    fn clips_windows_to_secondary_monitor() {
        let monitor = Rect::new(-2560, 0, 2560, 1600);
        let windows = [
            Rect::new(-100, 100, 400, 300), // 跨两块屏，左屏只露出 100px
            Rect::new(100, 100, 400, 300),  // 完全在主屏
            Rect::new(-2000, 200, 800, 600),
        ];
        assert_eq!(
            windows_on_monitor(&windows, monitor, 40),
            vec![Rect::new(2460, 100, 100, 300), Rect::new(560, 200, 800, 600)]
        );
    }
}
