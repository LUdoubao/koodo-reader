import { expect, test } from "@playwright/test";

test.describe("mobile shell", () => {
  test.skip(({ viewport }) => (viewport?.width ?? 0) > 767, "mobile project only");

  test("html carries the mobile layout fact and no horizontal scroll", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("html")).toHaveAttribute(
      "data-layout",
      "mobile"
    );

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("drawer opens from the header menu and closes after navigation", async ({
    page,
  }) => {
    await page.goto("/");
    const menuButton = page.locator(".mobile-menu-button");
    await expect(menuButton).toBeVisible();

    await menuButton.click();
    await expect(page.locator(".sidebar")).toHaveClass(/is-mobile-open/);
    await expect(page.locator(".mobile-drawer-mask")).toBeVisible();

    await page
      .locator(".mobile-drawer-mask")
      .click({ position: { x: 380, y: 400 } });
    await expect(page.locator(".sidebar")).not.toHaveClass(/is-mobile-open/);
    await expect(page.locator(".mobile-drawer-mask")).toHaveCount(0);
  });

  test("search collapses into an icon and expands into an overlay", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".header-search-container")).toBeHidden();

    await page.locator(".mobile-search-toggle").click();
    await expect(page.locator(".header-search-container")).toBeVisible();

    await page.locator(".mobile-search-close").click();
    await expect(page.locator(".header-search-container")).toBeHidden();
  });
});

test.describe("mobile shell escape hatch", () => {
  test("?layout=desktop forces the desktop shell on a narrow viewport", async ({
    page,
  }) => {
    await page.goto("/?layout=desktop");
    await expect(page.locator("html")).toHaveAttribute(
      "data-layout",
      "desktop"
    );
    await expect(page.locator(".mobile-menu-button")).toBeHidden();
  });
});
