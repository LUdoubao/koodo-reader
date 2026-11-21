import { describe, expect, it, vi } from "vitest";
import {
  createResizeThrottle,
  LAYOUT_OVERRIDE_KEY,
  MOBILE_MAX_WIDTH,
  readLayoutOverride,
  resolveIsTouch,
  resolveLayout,
} from "./useLayoutMode";

describe("resolveLayout", () => {
  it("treats 767px and below as mobile", () => {
    expect(resolveLayout(300)).toBe("mobile");
    expect(resolveLayout(767)).toBe("mobile");
  });

  it("treats 768px and above as desktop", () => {
    expect(resolveLayout(768)).toBe("desktop");
    expect(resolveLayout(1440)).toBe("desktop");
  });

  it("keeps the breakpoint constant in sync with the CSS width rule", () => {
    expect(MOBILE_MAX_WIDTH).toBe(767);
  });
});

describe("readLayoutOverride", () => {
  it("prefers a valid query parameter over stored state", () => {
    expect(readLayoutOverride("?layout=desktop", "mobile")).toBe("desktop");
    expect(readLayoutOverride("?layout=mobile", "desktop")).toBe("mobile");
  });

  it("falls back to session storage", () => {
    expect(readLayoutOverride("", "desktop")).toBe("desktop");
    expect(readLayoutOverride("", "mobile")).toBe("mobile");
  });

  it("ignores invalid values", () => {
    expect(readLayoutOverride("?layout=tablet", null)).toBeNull();
    expect(readLayoutOverride("", "yes")).toBeNull();
    expect(readLayoutOverride("?layout=", "desktop")).toBe("desktop");
  });
});

describe("resolveIsTouch", () => {
  it("keeps an explicit yes and no", () => {
    expect(resolveIsTouch("yes", false)).toBe(true);
    expect(resolveIsTouch("no", true)).toBe(false);
  });

  it("only auto-enables for a missing value on coarse pointers", () => {
    expect(resolveIsTouch(undefined, true)).toBe(true);
    expect(resolveIsTouch(null, false)).toBe(false);
    expect(resolveIsTouch("", false)).toBe(false);
  });
});

it("uses a stable session storage key", () => {
  expect(LAYOUT_OVERRIDE_KEY).toBe("koodo-layout-override");
});

describe("createResizeThrottle", () => {
  it("runs the leading call and coalesces trailing calls", () => {
    vi.useFakeTimers();
    const calls: number[] = [];
    const throttle = createResizeThrottle(() => calls.push(1), 300);

    throttle.run();
    throttle.run();
    expect(calls.length).toBe(1);

    vi.advanceTimersByTime(300);
    expect(calls.length).toBe(2);
    vi.useRealTimers();
  });

  it("drops the pending trailing call after cancel", () => {
    vi.useFakeTimers();
    const calls: number[] = [];
    const throttle = createResizeThrottle(() => calls.push(1), 300);

    throttle.run();
    throttle.run();
    expect(calls.length).toBe(1);

    throttle.cancel();
    vi.advanceTimersByTime(1000);
    expect(calls.length).toBe(1);
    vi.useRealTimers();
  });
});
