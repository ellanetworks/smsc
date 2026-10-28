import { expect, test, type Page } from "@playwright/test";
import { assertNoA11yViolations } from "../a11y";

const ROUTES = [
  { label: "Operator", route: "/operator", heading: "Operator" },
  { label: "Diameter", route: "/diameter", heading: "Diameter" },
  { label: "SMPP", route: "/smpp", heading: /^SMPP/ },
  { label: "Messages", route: "/messages", heading: "Messages" },
  { label: "Users", route: "/users", heading: /^Users/ },
  { label: "Profile", route: "/profile", heading: "Profile" },
] as const;

// settle waits for the page's heading, and for its loading spinners to go.
const settle = async (page: Page, heading: string | RegExp) => {
  await expect(
    page.getByRole("heading", { level: 1, name: heading }),
  ).toBeVisible();
  await expect(page.getByRole("progressbar")).toHaveCount(0, {
    timeout: 15_000,
  });
};

for (const { label, route, heading } of ROUTES) {
  test(`${label} renders`, async ({ page }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(String(error)));

    await page.goto(route);
    await settle(page, heading);

    const body = (await page.locator("body").innerText()).trim();
    for (const placeholder of [
      /\bundefined\b/,
      /\bNaN\b/,
      /\[object Object\]/,
    ]) {
      expect(body, `${route} rendered a placeholder value`).not.toMatch(
        placeholder,
      );
    }

    expect(pageErrors, `${route} raised an uncaught error`).toEqual([]);

    await assertNoA11yViolations(page, label);
  });
}
