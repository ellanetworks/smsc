import type { APIRequestContext } from "@playwright/test";
import { existsSync, readFileSync } from "node:fs";

const TOKEN_FILE = "e2e/.auth/admin-token.txt";

export const ADMIN_EMAIL = "e2e-admin@ellanetworks.com";
export const ADMIN_PASSWORD = "E2eAdminPassw0rd!";

export const RoleID = {
  Admin: 1,
  ReadOnly: 2,
  NetworkManager: 3,
  Submitter: 4,
} as const;

type Method = "get" | "post" | "put" | "delete";

export async function send(
  request: APIRequestContext,
  method: Method,
  path: string,
  opts: { token?: string; data?: unknown } = {},
) {
  return request[method](path, {
    data: opts.data as never,
    headers: opts.token ? { Authorization: `Bearer ${opts.token}` } : undefined,
  });
}

// json sends a request, and returns the result of its response.
export async function json<T>(
  request: APIRequestContext,
  method: Method,
  path: string,
  opts: { token?: string; data?: unknown } = {},
): Promise<T> {
  const response = await send(request, method, path, opts);

  if (!response.ok()) {
    throw new Error(
      `${method.toUpperCase()} ${path} -> ${response.status()} ${await response.text()}`,
    );
  }

  const body = await response.text();
  if (!body) return undefined as T;
  return (JSON.parse(body) as { result: T }).result;
}

export async function isInitialized(
  request: APIRequestContext,
): Promise<boolean> {
  const status = await json<{ initialized: boolean }>(
    request,
    "get",
    "/api/v1/status",
  );
  return status.initialized;
}

// ensureInitialized creates the first user, the e2e admin, unless the SMSC has users already.
export async function ensureInitialized(
  request: APIRequestContext,
): Promise<void> {
  if (await isInitialized(request)) return;

  const response = await send(request, "post", "/api/v1/init", {
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });

  if (!response.ok() && !(await isInitialized(request))) {
    throw new Error(
      `could not initialize the SMSC: ${response.status()} ${await response.text()}`,
    );
  }
}

// adminToken returns an access token of the e2e admin, the one auth.setup.ts saved if any.
export async function adminToken(request: APIRequestContext): Promise<string> {
  await ensureInitialized(request);

  if (existsSync(TOKEN_FILE)) {
    const cached = readFileSync(TOKEN_FILE, "utf8").trim();
    if (cached) return cached;
  }

  const login = await json<{ token: string }>(
    request,
    "post",
    "/api/v1/auth/login",
    { data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } },
  );
  return login.token;
}

export async function ensureUser(
  request: APIRequestContext,
  token: string,
  email: string,
  password: string,
  roleId: number,
): Promise<void> {
  const path = `/api/v1/users/${encodeURIComponent(email)}`;
  if ((await send(request, "get", path, { token })).ok()) return;

  const response = await send(request, "post", "/api/v1/users", {
    token,
    data: { email, password, role_id: roleId },
  });

  if (!response.ok() && !(await send(request, "get", path, { token })).ok()) {
    throw new Error(
      `could not create ${email}: ${response.status()} ${await response.text()}`,
    );
  }
}
