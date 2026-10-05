import { useEffect, useRef, useState } from "react";
import {
  cancelFlow,
  frameUrl,
  onSelectorFrame,
  selectionDone,
  selectorFrame,
  selectorReady,
  type CssRect,
  type PhysRect,
  type SelectorFrame,
} from "../shared/ipc";

interface Drag {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

const toRect = (d: Drag): CssRect => ({
  x: Math.min(d.x0, d.x1),
  y: Math.min(d.y0, d.y1),
  w: Math.abs(d.x1 - d.x0),
  h: Math.abs(d.y1 - d.y0),
});

/** 小于这个距离（CSS 像素）的拖动视为单击：有高亮窗口时选中窗口，否则忽略 */
const MIN_DRAG = 6;

/** 物理像素 → CSS 像素的比例（不依赖 devicePixelRatio，混合 DPI 下也正确） */
const cssScale = (f: SelectorFrame) => window.innerWidth / f.width;

const toCss = (r: PhysRect, s: number): CssRect => ({ x: r.x * s, y: r.y * s, w: r.w * s, h: r.h * s });

/** 找出包含该点（CSS 像素）的最上层窗口 */
function windowAt(f: SelectorFrame, x: number, y: number): CssRect | null {
  const s = cssScale(f);
  const px = x / s;
  const py = y / s;
  const hit = f.windows.find((w) => px >= w.x && px < w.x + w.w && py >= w.y && py < w.y + w.h);
  return hit ? toCss(hit, s) : null;
}

export function Selector() {
  const [frame, setFrame] = useState<SelectorFrame | null>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  const [hover, setHover] = useState<CssRect | null>(null);
  const dragging = useRef(false);

  useEffect(() => {
    const apply = (f: SelectorFrame | null) => {
      setDrag(null);
      dragging.current = false;
      setFrame(f);
      // 一出现就高亮鼠标下面的窗口，不必等鼠标移动
      const s = f ? cssScale(f) : 1;
      setHover(f?.cursor ? windowAt(f, f.cursor[0] * s, f.cursor[1] * s) : null);
    };
    selectorFrame().then((f) => f && apply(f));
    const unlisten = onSelectorFrame(apply);
    return () => {
      unlisten.then((fn) => fn());
    };
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") cancelFlow();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (!frame) return null;

  const dragRect = drag ? toRect(drag) : null;
  const isDrag = dragRect !== null && (dragRect.w >= MIN_DRAG || dragRect.h >= MIN_DRAG);
  // 拖动时显示拖出来的矩形；否则显示悬停识别到的窗口
  const shown = isDrag ? dragRect : hover;

  return (
    <div
      className="selector"
      onContextMenu={(e) => {
        e.preventDefault();
        cancelFlow();
      }}
      onPointerDown={(e) => {
        if (e.button !== 0) return;
        e.currentTarget.setPointerCapture(e.pointerId);
        dragging.current = true;
        setDrag({ x0: e.clientX, y0: e.clientY, x1: e.clientX, y1: e.clientY });
      }}
      onPointerMove={(e) => {
        if (dragging.current) {
          setDrag((d) => (d ? { ...d, x1: e.clientX, y1: e.clientY } : d));
        } else {
          setHover(windowAt(frame, e.clientX, e.clientY));
        }
      }}
      onPointerUp={(e) => {
        if (!dragging.current || !drag) return;
        dragging.current = false;
        const r = toRect({ ...drag, x1: e.clientX, y1: e.clientY });
        setDrag(null);
        if (r.w >= MIN_DRAG && r.h >= MIN_DRAG) {
          selectionDone(frame.session, r);
        } else if (r.w < MIN_DRAG && r.h < MIN_DRAG) {
          // 单击：选中鼠标下的窗口
          const w = windowAt(frame, e.clientX, e.clientY);
          if (w) selectionDone(frame.session, w);
        }
      }}
    >
      {/* key 变化时强制重新加载，确保 onLoad 每次都会触发 */}
      <img
        key={`${frame.session}-${frame.monitor}`}
        className="frame"
        src={frameUrl(frame)}
        draggable={false}
        onLoad={() => selectorReady(frame)}
        alt=""
      />
      {shown ? (
        <div className={`selection${isDrag ? "" : " window"}`} style={{ left: shown.x, top: shown.y, width: shown.w, height: shown.h }}>
          <span className="size">
            {isDrag
              ? `${Math.round(shown.w / cssScale(frame))} × ${Math.round(shown.h / cssScale(frame))}`
              : "单击选择此窗口 · 拖动自定义区域"}
          </span>
        </div>
      ) : (
        <div className="dim" />
      )}
      {!isDrag && <div className="hint">单击选择窗口，或拖动框选聊天区域 · Esc 取消</div>}
    </div>
  );
}
