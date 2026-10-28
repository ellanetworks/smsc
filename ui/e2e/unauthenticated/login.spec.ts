import { expect, test } from "@playwright/test";
import { assertNoA11yViolations } from "../a11y";
import { ADMIN_EMAIL, ADMIN_PASSWORD, ensureInitialized } from "../api";

test.beforeAll(async ({ request }) => {
  await ensureInitialized(request);
});

test("an initialized SMSC routes the root path to login", async ({ page }) => {
  await page.goto("/");

  await expect(page).toHaveURL(/\/login$/);
});

test("logging in lands on the Diameter page", async ({ page }) => {
  await page.goto("/login");

  await page.getByLabel(/^Email/).fill(ADMIN_EMAIL);
  await page.getByLabel(/^Password/).fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: "Login" }).click();

  await expect(page).toHaveURL(/\/diameter$/);
});

test("bad credentials keep the user on the login page", async ({ page }) => {
  await page.goto("/login");

  await page.getByLabel(/^Email/).fill(ADMIN_EMAIL);
  await page.getByLabel(/^Password/).fill("wrong-password");
  await page.getByRole("button", { name: "Login" }).click();

  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page).toHaveURL(/\/login$/);
});

test("an admin-only route sends a visitor to login", async ({ page }) => {
  await page.goto("/users");

  await expect(page).toHaveURL(/\/login$/);
});

test("the login page has no accessibility violations", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("button", { name: "Login" })).toBeVisible();

  await assertNoA11yViolations(page, "Login");
});
