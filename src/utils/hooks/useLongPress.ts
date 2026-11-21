/* mobile: long-press entry point replacing hover-only context menus */
import React, { useCallback, useEffect, useRef } from "react";

export interface LongPressHandlers {
  onPointerDown: (event: React.PointerEvent) => void;
  onPointerMove: (event: React.PointerEvent) => void;
  onPointerUp: (event: React.PointerEvent) => void;
  onPointerCancel: (event: React.PointerEvent) => void;
  onClickCapture: (event: React.MouseEvent) => void;
}

export interface LongPressOptions {
  /** Hold duration before the callback fires; defaults to 500ms. */
  delay?: number;
  /** Movement allowance before the press is treated as a scroll. */
  moveThreshold?: number;
}

const DEFAULT_DELAY = 500;
const DEFAULT_MOVE_THRESHOLD = 8;

/**
 * Touch-only long press that coexists with the desktop context menu.
 * Mouse input is ignored so right-click behaviour is unchanged. After a long
 * press fires, the following click is suppressed to avoid also opening the
 * book while the menu is showing.
 */
export function useLongPress(
  onLongPress: (x: number, y: number) => void,
  options: LongPressOptions = {}
): LongPressHandlers {
  const delay = options.delay ?? DEFAULT_DELAY;
  const moveThreshold = options.moveThreshold ?? DEFAULT_MOVE_THRESHOLD;
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const startRef = useRef({ x: 0, y: 0 });
  const firedRef = useRef(false);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const onPointerDown = useCallback(
    (event: React.PointerEvent) => {
      if (event.pointerType === "mouse") return;
      clearTimer();
      firedRef.current = false;
      startRef.current = { x: event.clientX, y: event.clientY };
      const { clientX, clientY } = event;
      timerRef.current = setTimeout(() => {
        timerRef.current = null;
        firedRef.current = true;
        onLongPress(clientX, clientY);
      }, delay);
    },
    [clearTimer, delay, onLongPress]
  );

  const onPointerMove = useCallback(
    (event: React.PointerEvent) => {
      if (timerRef.current === null) return;
      const dx = event.clientX - startRef.current.x;
      const dy = event.clientY - startRef.current.y;
      if (Math.hypot(dx, dy) > moveThreshold) {
        clearTimer();
      }
    },
    [clearTimer, moveThreshold]
  );

  const onPointerUp = useCallback(() => {
    clearTimer();
  }, [clearTimer]);

  const onClickCapture = useCallback((event: React.MouseEvent) => {
    if (!firedRef.current) return;
    firedRef.current = false;
    event.preventDefault();
    event.stopPropagation();
  }, []);

  // 列表重建（换页、筛选、加载更多）可能在 500ms 内卸载卡片；
  // 卸载时清掉定时器，避免对已卸载卡片触发长按回调。
  useEffect(() => clearTimer, [clearTimer]);

  return {
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel: onPointerUp,
    onClickCapture,
  };
}
