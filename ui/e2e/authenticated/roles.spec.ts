import { expect, test } from "@playwright/test";
import { ROLES } from "../roles";

// The navigation of each role is the pages it may load (ui/src/contexts/AuthContext.tsx).
const NAVIGATION = {
  "Read Only": ["Operator", "Diameter", "Messages"],
  "Network Manager": ["Operator", "Diameter", "Messages"],
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
