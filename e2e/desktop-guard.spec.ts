import { expect, test } from "@playwright/test";

test.describe("desktop guard", () => {
  test.skip(
    ({ viewport }) => (viewport?.width ?? 0) <= 767,
    "desktop project only"
  );

  test("desktop shell keeps the persistent sidebar and hides mobile controls", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("html")).toHaveAttribute(
      "data-layout",
      "desktop"
    );

    await expect(page.locator(".mobile-menu-button")).toBeHidden();
    await expect(page.locator(".mobile-search-toggle")).toBeHidden();
    await expect(page.locator(".mobile-list-action-bar")).toHaveCount(0);

    const sidebar = page.locator(".sidebar");
    await expect(sidebar).toBeVisible();
    const transform = await sidebar.evaluate(
      (element) => getComputedStyle(element).transform
    );
    // Persistent sidebar must not be pushed off-canvas on desktop.
    expect(transform === "none" || transform.startsWith("matrix(1,")).toBe(true);
  });

  test("desktop layout has no horizontal scroll", async ({ page }) => {
    await page.goto("/");
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
});
