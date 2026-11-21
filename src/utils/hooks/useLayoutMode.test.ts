import { describe, expect, it } from "vitest";
import {
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
