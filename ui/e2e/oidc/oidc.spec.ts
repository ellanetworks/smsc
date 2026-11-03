import {
  expect,
  test,
  type APIRequestContext,
  type BrowserContext,
  type Page,
} from "@playwright/test";
import { readFileSync } from "node:fs";
import {
  ADMIN_EMAIL,
  ADMIN_PASSWORD,
  RoleID,
  ensureInitialized,
  json,
  send,
} from "../api";

const SMSC_PORT = process.env.ELLA_E2E_OIDC_PORT ?? "15443";
const KEYCLOAK_PORT = process.env.KEYCLOAK_E2E_PORT ?? "18443";
const EXTERNAL_URL = `https://localhost:${SMSC_PORT}`;
const ISSUER = `https://keycloak:${KEYCLOAK_PORT}/realms/ella`;
const CA_CERTIFICATE = readFileSync(
  new URL("../.build/oidc/ca.pem", import.meta.url),
  "utf8",
);
const SESSION_COOKIE = "__Host-smsc_session";
const LIFETIME_SECONDS = 300;

const ALICE = { username: "alice", email: "alice@example.com" };
const BOB = { username: "bob", email: "bob@example.com" };
const CAROL = { username: "carol", email: "carol@example.com" };

type User = { email: string; role_id: number; oidc_linked: boolean };
type GroupRole = { group: string; role_id: number };

let adminToken = "";

async function configureOIDC(
  request: APIRequestContext,
  opts: { enabled?: boolean; groupRoles?: GroupRole[] } = {},
) {
  const groupRoles = opts.groupRoles ?? [];
  await json(request, "put", "/api/v1/oidc", {
    token: adminToken,
    data: {
      enabled: opts.enabled ?? true,
      external_url: EXTERNAL_URL,
      issuer: ISSUER,
      client_id: "ella-smsc",
      client_secret: "e2e-client-secret",
      display_name: "Keycloak",
      scopes: ["openid", "email", "profile"],
      session_lifetime_seconds: LIFETIME_SECONDS,
      trust_unverified_email: false,
      ca_certificate: CA_CERTIFICATE,
      groups_claim: groupRoles.length > 0 ? "groups" : "",
      group_roles: groupRoles,
    },
  });
}

async function keycloakLogin(page: Page, username: string) {
  await page.goto("/login");
  await page.getByRole("link", { name: "Sign in with Keycloak" }).click();
  await page.waitForURL((url) => url.host === `keycloak:${KEYCLOAK_PORT}`);
  await page.locator("#username").fill(username);
  await page.locator("#password").fill(`${username}-password`);
  await page.locator("#kc-login").click();
  await page.waitForURL((url) => url.host === `localhost:${SMSC_PORT}`);
}

async function getUser(request: APIRequestContext, email: string) {
  return send(request, "get", `/api/v1/users/${encodeURIComponent(email)}`, {
    token: adminToken,
  });
}

async function sessionCookie(context: BrowserContext) {
  const cookies = await context.cookies(EXTERNAL_URL);
  return cookies.find((cookie) => cookie.name === SESSION_COOKIE);
}

async function refresh(page: Page) {
  return page.request.post("/api/v1/auth/refresh");
}

test.describe.configure({ mode: "serial" });

test.beforeAll(async ({ request }) => {
  await ensureInitialized(request);
  const login = await json<{ token: string }>(
    request,
    "post",
    "/api/v1/auth/login",
    { data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } },
  );
  adminToken = login.token;
});

test("Keycloak is discovered through the CA certificate", async ({
  request,
}) => {
  const discovery = await json<{ issuer: string; pkce: boolean }>(
    request,
    "post",
    "/api/v1/oidc/discovery",
    {
      token: adminToken,
      data: { issuer: ISSUER, ca_certificate: CA_CERTIFICATE },
    },
  );

  expect(discovery.issuer).toBe(ISSUER);
  expect(discovery.pkce).toBe(true);
});

test("an existing user is linked on first login", async ({ page, request }) => {
  await configureOIDC(request);

  if ((await getUser(request, ALICE.email)).status() === 404) {
    await json(request, "post", "/api/v1/users", {
      token: adminToken,
      data: {
        email: ALICE.email,
        password: "AliceE2ePassw0rd!",
        role_id: RoleID.ReadOnly,
      },
    });
  }

  await keycloakLogin(page, ALICE.username);

  expect(new URL(page.url()).pathname).not.toBe("/login");
  expect(await sessionCookie(page.context())).toBeDefined();
  expect((await refresh(page)).status()).toBe(200);

  const user = await json<User>(
    request,
    "get",
    `/api/v1/users/${encodeURIComponent(ALICE.email)}`,
    { token: adminToken },
  );
  expect(user.oidc_linked).toBe(true);
  expect(user.role_id).toBe(RoleID.ReadOnly);
});

test("an unknown user is refused without a group mapping", async ({
  page,
  request,
}) => {
  await configureOIDC(request);

  await keycloakLogin(page, BOB.username);

  await expect(page).toHaveURL(/\/login\?error=not_set_up$/);
  await expect(
    page.getByText(
      "Your account has not been set up in Ella SMSC. Contact an administrator.",
    ),
  ).toBeVisible();
  expect(await sessionCookie(page.context())).toBeUndefined();
  expect((await getUser(request, BOB.email)).status()).toBe(404);
});

test("a user in a mapped group is created with the group's role", async ({
  page,
  request,
}) => {
  await configureOIDC(request, {
    groupRoles: [{ group: "smsc-operators", role_id: RoleID.NetworkManager }],
  });

  await keycloakLogin(page, CAROL.username);

  expect(new URL(page.url()).pathname).not.toBe("/login");
  expect((await refresh(page)).status()).toBe(200);

  const user = await json<User>(
    request,
    "get",
    `/api/v1/users/${encodeURIComponent(CAROL.email)}`,
    { token: adminToken },
  );
  expect(user.oidc_linked).toBe(true);
  expect(user.role_id).toBe(RoleID.NetworkManager);
});

test("a user in no mapped group is refused", async ({ page, request }) => {
  await configureOIDC(request, {
    groupRoles: [{ group: "smsc-operators", role_id: RoleID.NetworkManager }],
  });

  await keycloakLogin(page, BOB.username);

  await expect(page).toHaveURL(/\/login\?error=no_group$/);
  await expect(
    page.getByText(
      "None of your groups gives access to Ella SMSC. Contact an administrator.",
    ),
  ).toBeVisible();
  expect((await getUser(request, BOB.email)).status()).toBe(404);
});

test("the OIDC session lasts the session lifetime and ends when OIDC is disabled", async ({
  page,
  request,
}) => {
  await configureOIDC(request);

  const before = Date.now() / 1000;
  await keycloakLogin(page, ALICE.username);
  const after = Date.now() / 1000;

  const cookie = await sessionCookie(page.context());
  expect(cookie).toBeDefined();
  expect(cookie!.expires).toBeGreaterThanOrEqual(
    Math.floor(before) + LIFETIME_SECONDS - 5,
  );
  expect(cookie!.expires).toBeLessThanOrEqual(
    Math.ceil(after) + LIFETIME_SECONDS + 5,
  );

  const refreshed = await refresh(page);
  expect(refreshed.status()).toBe(200);
  const { result } = (await refreshed.json()) as { result: { token: string } };
  const me = await json<User>(request, "get", "/api/v1/users/me", {
    token: result.token,
  });
  expect(me.email).toBe(ALICE.email);

  await configureOIDC(request, { enabled: false });

  expect((await refresh(page)).status()).toBe(401);
  expect(
    (
      await send(request, "get", "/api/v1/users/me", { token: result.token })
    ).status(),
  ).toBe(401);
  expect(
    (await send(request, "get", "/api/v1/users", { token: adminToken })).ok(),
  ).toBe(true);
});
