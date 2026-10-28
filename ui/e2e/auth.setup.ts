import { test as setup } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import {
  ADMIN_EMAIL,
  ADMIN_PASSWORD,
  adminToken,
  ensureInitialized,
  ensureUser,
  send,
} from "./api";
import { ROLES, ROLE_PASSWORD } from "./roles";

const SESSION_FILE = "e2e/.auth/admin.json";
const TOKEN_FILE = "e2e/.auth/admin-token.txt";

// The session of a user is its refresh cookie: the UI gets an access token from it when a page loads.
setup("authenticate", async ({ request }) => {
  await ensureInitialized(request);

  const response = await send(request, "post", "/api/v1/auth/login", {
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });

  if (!response.ok()) {
    throw new Error(
      `could not authenticate: ${response.status()} ${await response.text()}`,
    );
  }

  await request.storageState({ path: SESSION_FILE });

  const { result } = (await response.json()) as { result: { token: string } };

  mkdirSync("e2e/.auth", { recursive: true });
  writeFileSync(TOKEN_FILE, result.token);
});

setup("seed the other roles", async ({ request, playwright, baseURL }) => {
  const token = await adminToken(request);

  for (const role of Object.values(ROLES)) {
    await ensureUser(request, token, role.email, ROLE_PASSWORD, role.roleId);

    const context = await playwright.request.newContext({ baseURL });
    const response = await context.post("/api/v1/auth/login", {
      data: { email: role.email, password: ROLE_PASSWORD },
    });

    if (!response.ok()) {
      throw new Error(
        `${role.email} could not log in: ${response.status()} ${await response.text()}`,
      );
    }

    await context.storageState({ path: role.session });
    await context.dispose();
  }
});
