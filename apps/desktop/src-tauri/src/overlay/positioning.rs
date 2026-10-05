//! Overlay 位置算法（纯函数）。所有坐标都是全局物理像素。
//!
//! 候选位置依次为：
//! 1. 选区右侧
//! 2. 选区左侧
//! 3. 选区内部右上角（不遮挡下方的最新消息）
//! 4. 工作区右下角（兜底）
//!
//! 选出第一个能完整放进工作区的位置，最后再限制在工作区范围内。

use crate::geom::Rect;

pub fn place_overlay(selection: Rect, size: (i32, i32), work_area: Rect, gap: i32) -> (i32, i32) {
    let (w, h) = size;
    let clamp_y = |y: i32| y.clamp(work_area.y + gap, (work_area.bottom() - h - gap).max(work_area.y + gap));

    let candidates = [
        (selection.right() + gap, clamp_y(selection.y)),
        (selection.x - gap - w, clamp_y(selection.y)),
        (selection.right() - w - gap, selection.y + gap),
    ];

    for (i, &(x, y)) in candidates.iter().enumerate() {
        let r = Rect::new(x, y, w, h);
        if !work_area.contains_rect(&r) {
            continue;
        }
        // 放在选区内部时，要求选区明显大于 overlay，并且只占选区上半部分
        if i == 2 && (selection.w < w + 2 * gap || r.bottom() > selection.y + selection.h * 2 / 3) {
            continue;
        }
        return (x, y);
    }

    let x = work_area.right() - w - gap;
    let y = work_area.bottom() - h - gap;
    (x.max(work_area.x), y.max(work_area.y))
}

#[cfg(test)]
mod tests {
    use super::*;

    const WA: Rect = Rect::new(0, 0, 1920, 1040);
    const SIZE: (i32, i32) = (360, 400);

    #[test]
    fn prefers_right_side() {
        let sel = Rect::new(100, 100, 600, 700);
        assert_eq!(place_overlay(sel, SIZE, WA, 12), (712, 100));
    }

    #[test]
    fn falls_back_to_left_when_right_is_full() {
        let sel = Rect::new(1300, 100, 600, 700);
        assert_eq!(place_overlay(sel, SIZE, WA, 12), (1300 - 12 - 360, 100));
    }

    #[test]
    fn clamps_vertically_into_work_area() {
        let sel = Rect::new(100, 900, 600, 100);
        let (_, y) = place_overlay(sel, SIZE, WA, 12);
        assert_eq!(y, 1040 - 400 - 12);
    }

    #[test]
    fn uses_inside_top_right_for_full_width_selection() {
        let sel = Rect::new(0, 0, 1920, 1040);
        assert_eq!(place_overlay(sel, SIZE, WA, 12), (1920 - 360 - 12, 12));
    }

    #[test]
    fn falls_back_to_corner() {
        // 选区宽但矮，左右都放不下，内部也放不下
        let sel = Rect::new(0, 500, 1920, 300);
        assert_eq!(place_overlay(sel, SIZE, WA, 12), (1920 - 360 - 12, 1040 - 400 - 12));
    }

    #[test]
    fn works_on_secondary_monitor_with_negative_origin() {
        let wa = Rect::new(-1920, 0, 1920, 1040);
        let sel = Rect::new(-1800, 100, 500, 600);
        assert_eq!(place_overlay(sel, SIZE, wa, 12), (-1800 + 500 + 12, 100));
    }
}
