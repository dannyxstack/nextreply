import { useEffect, useRef, useState } from "react";
import { cancelFlow, frameUrl, onSelectorFrame, selectionDone, selectorFrame, selectorReady, type CssRect, type SelectorFrame } from "../shared/ipc";

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

/** 小于这个尺寸（CSS 像素）的拖动视为误点，不提交 */
const MIN_DRAG = 6;

export function Selector() {
  const [frame, setFrame] = useState<SelectorFrame | null>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  const dragging = useRef(false);

  useEffect(() => {
    const apply = (f: SelectorFrame | null) => {
      setDrag(null);
      dragging.current = false;
      setFrame(f);
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

  const rect = drag ? toRect(drag) : null;

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
        if (!dragging.current) return;
        setDrag((d) => (d ? { ...d, x1: e.clientX, y1: e.clientY } : d));
      }}
      onPointerUp={(e) => {
        if (!dragging.current || !drag) return;
        dragging.current = false;
        const r = toRect({ ...drag, x1: e.clientX, y1: e.clientY });
        if (r.w < MIN_DRAG || r.h < MIN_DRAG) {
          setDrag(null);
          return;
        }
        selectionDone(frame.session, r);
      }}
    >
      {/* key 变化时强制重新加载，确保 onLoad 每次都会触发 */}
      <img
        key={`${frame.session}-${frame.monitor}`}
        className="frame"
        src={frameUrl(frame)}
        draggable={false}
        onLoad={() => selectorReady(frame.session)}
        alt=""
      />
      {rect ? (
        <div className="selection" style={{ left: rect.x, top: rect.y, width: rect.w, height: rect.h }}>
          <span className="size">
            {Math.round(rect.w * devicePixelRatio)} × {Math.round(rect.h * devicePixelRatio)}
          </span>
        </div>
      ) : (
        <>
          <div className="dim" />
          <div className="hint">拖动框选聊天区域 · Esc 取消</div>
        </>
      )}
    </div>
  );
}
