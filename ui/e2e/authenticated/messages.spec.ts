import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { adminToken, json } from "../api";
import { ROLES } from "../roles";

// send submits a message to a number of its own, so that tests running at the same time find their row, and returns
// that number. With no core connected, the message stays pending.
const send = async (
  request: APIRequestContext,
  text: string,
  from = "+15550001000",
) => {
  const to = `+1555${String(Math.floor(Math.random() * 1e7)).padStart(7, "0")}`;
  await json(request, "post", "/api/v1/messages", {
    token: await adminToken(request),
    data: { from, to, text },
  });
  return to;
};

const openMessage = async (page: Page, to: string) => {
  await page.goto("/messages");
  await page.getByRole("gridcell", { name: to }).click();

  const panel = page.getByRole("complementary");
  await expect(
    panel.getByRole("heading", { name: /^Message \d+$/ }),
  ).toBeVisible();
  return panel;
};

test("the message panel opens under the app bar, and closes", async ({
  page,
  request,
}) => {
  const to = await send(request, "under the app bar");
  const panel = await openMessage(page, to);

  const bar = await page.getByRole("banner").boundingBox();
  const heading = await panel
    .getByRole("heading", { name: /^Message \d+$/ })
    .boundingBox();
  expect(heading!.y).toBeGreaterThanOrEqual(bar!.y + bar!.height);

  // A click fails when another element, such as the app bar, covers its target.
  await panel.getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("complementary")).toHaveCount(0);
});

test("the content shows only when asked for", async ({ page, request }) => {
  const to = await send(request, "shown on request");
  const panel = await openMessage(page, to);

  await expect(panel.getByText("shown on request")).toHaveCount(0);
  await panel.getByRole("button", { name: "Show content" }).click();
  await expect(panel.getByText("shown on request")).toBeVisible();
});

test("the time range filters the messages on the server", async ({
  page,
  request,
}) => {
  const to = await send(request, "in range");
  await page.goto(`/messages`);
  await page.getByLabel("To", { exact: true }).fill(to);
  await expect(page.getByRole("gridcell", { name: to })).toBeVisible();

  // The picker's From and To are in its popover, apart from the page's filters of the same names.
  await page.getByRole("button", { name: /^Time range:/ }).click();
  const picker = page.locator(".MuiPopover-paper");
  await picker.getByLabel("From", { exact: true }).fill("2020-01-01T00:00");
  await picker.getByLabel("To", { exact: true }).fill("2020-01-02T00:00");
  await page.keyboard.press("Escape");
  await expect(page.getByText("No messages.")).toBeVisible();

  await page.getByRole("button", { name: /^Time range:/ }).click();
  await page.getByRole("menuitem", { name: "Last 15 minutes" }).click();
  await expect(page.getByRole("gridcell", { name: to })).toBeVisible();
});

test("the sender filter finds a message sent from a name", async ({
  page,
  request,
}) => {
  const to = await send(request, "from a name", "Ella E2E");
  await page.goto("/messages");
  await page.getByLabel("From", { exact: true }).fill("Ella E2E");
  await expect(page.getByRole("gridcell", { name: to })).toBeVisible();

  await page.getByLabel("From", { exact: true }).fill("+15550001000");
  await expect(page.getByRole("gridcell", { name: to })).toHaveCount(0);
});

test.describe("Read Only", () => {
  test.use({ storageState: ROLES["Read Only"].session });

  test("sees a message without its content", async ({ page, request }) => {
    const to = await send(request, "not for read only");
    const panel = await openMessage(page, to);

    await expect(
      panel.getByRole("button", { name: "Show content" }),
    ).toHaveCount(0);
  });
});
