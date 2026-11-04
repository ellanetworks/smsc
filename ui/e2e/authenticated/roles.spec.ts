import { expect, test } from "@playwright/test";
import { ROLES } from "../roles";

const NAVIGATION = {
  "Read Only": ["Dashboard", "Operator", "Diameter", "SMPP", "Messages"],
  "Network Manager": ["Dashboard", "Operator", "Diameter", "SMPP", "Messages"],
  Submitter: ["Messages"],
} as const;

for (const [name, role] of Object.entries(ROLES)) {
  test.describe(name, () => {
    test.use({ storageState: role.session });

    test("sees only its pages", async ({ page }) => {
      await page.goto("/");

      const nav = page.getByRole("navigation", { name: "Main" });
      await expect(nav.getByRole("link")).toHaveText([
        ...NAVIGATION[name as keyof typeof NAVIGATION],
      ]);
    });
  });
}
