import { describe, expect, it } from "vitest";
import { CARD_METRICS, countPerPage } from "../../../utils/hooks/useLayoutMode";

describe("book list pagination", () => {
  it("matches the historical desktop column count", () => {
    // 1050px shell minus the 220px sidebar leaves 830px for the grid.
    expect(countPerPage(830, 535, "desktop")).toBe(
      Math.floor(830 / 133) * (Math.floor(535 / 201) + 2)
    );
  });

  it("uses 104px cards and three columns on mobile", () => {
    expect(CARD_METRICS.mobile.width).toBe(104);
    // 390px viewport minus 24px padding: 3 columns.
    expect(countPerPage(366, 700, "mobile")).toBe(
      3 * (Math.floor(700 / 158) + 2)
    );
  });

  it("keeps at least one column and the two overscan rows", () => {
    expect(countPerPage(0, 0, "mobile")).toBe(1 * 3);
    expect(countPerPage(10, 10, "desktop")).toBe(1 * 3);
  });
});
