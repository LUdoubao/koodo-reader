/* mobile: layout fact source for shell, lists, reader and dialogs */
import { useEffect, useState } from "react";

/**
 * Mirrors RESIZE_THROTTLE_MS from utils/common.ts. This module stays free of
 * browser-only imports so the breakpoint rules can be unit-tested in Node.
 */
const RESIZE_THROTTLE_MS = 300;

/** Leading + trailing throttle, matching utils/common.ts behaviour. */
function throttleResize(handler: () => void, wait: number): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let lastCall = 0;
  return () => {
    const now = Date.now();
    const remaining = wait - (now - lastCall);
    if (remaining <= 0) {
      lastCall = now;
      handler();
      return;
    }
    if (timer === null) {
      timer = setTimeout(() => {
        timer = null;
        lastCall = Date.now();
        handler();
      }, remaining);
    }
  };
}

export type LayoutMode = "mobile" | "desktop";

export interface LayoutSnapshot {
  /** Resolved shell layout; CSS consumes the same value through data-layout. */
  layout: LayoutMode;
  width: number;
  height: number;
  /** Coarse pointer capability, independent from the layout breakpoint. */
  isTouch: boolean;
}

/** Mobile layout applies at or below this CSS width; desktop starts at 768px. */
export const MOBILE_MAX_WIDTH = 767;
/** Session-only escape hatch used by QA and emergency rollback. */
export const LAYOUT_OVERRIDE_KEY = "koodo-layout-override";
export const LAYOUT_PARAM = "layout";

export function isLayoutMode(value: unknown): value is LayoutMode {
  return value === "mobile" || value === "desktop";
}

/** Pure breakpoint rule; exported so tests can cover the 767/768 boundary. */
export function resolveLayout(width: number): LayoutMode {
  return width <= MOBILE_MAX_WIDTH ? "mobile" : "desktop";
}

/**
 * ?layout= wins over the stored override. Invalid values are ignored, and a
 * missing sessionStorage degrades to a URL-only override for this load.
 */
export function readLayoutOverride(
  search: string,
  stored: string | null
): LayoutMode | null {
  const fromQuery = new URLSearchParams(search).get(LAYOUT_PARAM);
  if (isLayoutMode(fromQuery)) return fromQuery;
  if (isLayoutMode(stored)) return stored;
  return null;
}

/**
 * Saved "yes"/"no" always wins. Only a missing value falls back to the coarse
 * pointer capability, so an explicit opt-out is never silently re-enabled.
 */
export function resolveIsTouch(
  configValue: string | null | undefined,
  coarsePointer: boolean
): boolean {
  if (configValue === "yes") return true;
  if (configValue === "no") return false;
  return coarsePointer;
}

export function isCoarsePointer(): boolean {
  if (typeof window === "undefined" || !window.matchMedia) return false;
  return window.matchMedia("(pointer: coarse)").matches;
}

/** Card metric per layout; desktop values are the historical constants. */
export const CARD_METRICS: Record<
  LayoutMode,
  { width: number; height: number }
> = {
  mobile: { width: 104, height: 158 },
  desktop: { width: 133, height: 201 },
};

/**
 * Books per page from container size and layout. Desktop keeps the exact
 * historical formula so pagination counts do not change.
 */
export function countPerPage(
  width: number,
  height: number,
  layout: LayoutMode = "desktop"
): number {
  const metrics = CARD_METRICS[layout];
  const columns = Math.max(1, Math.floor(width / metrics.width));
  const rows = Math.max(1, Math.floor(height / metrics.height)) + 2;
  return columns * rows;
}

function readStoredOverride(): string | null {
  try {
    return window.sessionStorage.getItem(LAYOUT_OVERRIDE_KEY);
  } catch (error) {
    return null;
  }
}

function persistOverride(layout: LayoutMode | null) {
  if (!layout) return;
  try {
    window.sessionStorage.setItem(LAYOUT_OVERRIDE_KEY, layout);
  } catch (error) {
    // Storage may be unavailable in private mode; the URL parameter still
    // covers this load.
  }
}

function measure(): LayoutSnapshot {
  const width = window.innerWidth;
  const height = window.innerHeight;
  const override = readLayoutOverride(
    window.location.search,
    readStoredOverride()
  );
  persistOverride(override);
  return {
    layout: override ?? resolveLayout(width),
    width,
    height,
    isTouch: isCoarsePointer(),
  };
}

function applyLayout(snapshot: LayoutSnapshot) {
  document.documentElement.dataset.layout = snapshot.layout;
  const viewportHeight = window.visualViewport?.height;
  document.documentElement.style.setProperty(
    "--app-height",
    `${Math.round(viewportHeight ?? snapshot.height)}px`
  );
}

/**
 * Single source of truth shared by the shell. Writes data-layout on <html> and
 * --app-height from visualViewport, then keeps them in sync on resize,
 * orientation change and pointer capability change.
 */
export function useLayoutMode(): LayoutSnapshot {
  const [snapshot, setSnapshot] = useState<LayoutSnapshot>(measure);

  useEffect(() => {
    const sync = () => {
      setSnapshot((previous) => {
        const next = measure();
        if (
          previous.layout === next.layout &&
          previous.width === next.width &&
          previous.height === next.height &&
          previous.isTouch === next.isTouch
        ) {
          return previous;
        }
        return next;
      });
    };
    const handleResize = throttleResize(sync, RESIZE_THROTTLE_MS);
    const pointerQuery = window.matchMedia?.("(pointer: coarse)");

    window.addEventListener("resize", handleResize);
    window.addEventListener("orientationchange", handleResize);
    window.visualViewport?.addEventListener("resize", handleResize);
    pointerQuery?.addEventListener("change", handleResize);
    sync();

    return () => {
      window.removeEventListener("resize", handleResize);
      window.removeEventListener("orientationchange", handleResize);
      window.visualViewport?.removeEventListener("resize", handleResize);
      pointerQuery?.removeEventListener("change", handleResize);
    };
  }, []);

  useEffect(() => {
    applyLayout(snapshot);
  }, [snapshot]);

  return snapshot;
}
