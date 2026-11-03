import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import Profile from "@/pages/Profile";
import UserDetail from "@/pages/UserDetail";
import Users from "@/pages/Users";
import type { User } from "@/queries/users";
import { page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const user = (overrides: Partial<User> = {}): User => ({
  email: "nm@example.com",
  role_id: 3,
  role_pinned: false,
  password_set: true,
  oidc_linked: true,
  ...overrides,
});

const serve = (
  u: User,
  me: User = user({ email: "admin@example.com", role_id: 1 }),
) => {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const path = `/api/v1/users/${encodeURIComponent(u.email)}`;
  const record = (url: URL, init?: RequestInit) => {
    calls.push({
      method: String(init?.method),
      path: url.pathname,
      ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    return json(200, { result: { message: "ok" } });
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/users": (url, init) =>
          init?.method === "POST"
            ? record(url, init)
            : json(200, { result: page([me, u]) }),
        "/api/v1/users/me": () => json(200, { result: me }),
        "/api/v1/users/me/api-tokens": () => json(200, { result: page([]) }),
        [path]: (url, init) =>
          init?.method === "PUT" ? record(url, init) : json(200, { result: u }),
        [`${path}/password`]: record,
        [`${path}/oidc`]: record,
        [`${path}/api-tokens`]: () => json(200, { result: page([]) }),
      }),
    ),
  );
  return calls;
};

const renderAt = (path: string) =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/users" element={<Users />} />
        <Route path="/users/:email" element={<UserDetail />} />
        <Route path="/profile" element={<Profile />} />
      </Routes>
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Users and single sign-on", () => {
  it("lists how each user signs in, and pinned roles", async () => {
    serve(user({ password_set: false, role_pinned: true }));
    renderAt("/users");

    const grid = await screen.findByRole("grid", { name: "Users" });
    expect(await within(grid).findByText("Single sign-on")).toBeInTheDocument();
    expect(
      within(grid).getByText("Password, Single sign-on"),
    ).toBeInTheDocument();
    expect(
      within(grid).getByText("Network Manager (pinned)"),
    ).toBeInTheDocument();
  });

  it("creates a user without a password", async () => {
    const calls = serve(user());
    renderAt("/users");

    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Email", { exact: false }), {
      target: { value: "sso@example.com" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/users",
          body: { email: "sso@example.com", password: "", role_id: 2 },
        },
      ]),
    );
  });

  it("removes a user's password", async () => {
    const calls = serve(user());
    renderAt("/users/nm%40example.com");

    fireEvent.click(
      await screen.findByRole("button", { name: "Remove Password" }),
    );
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Remove",
      }),
    );

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: "/api/v1/users/nm%40example.com/password" },
      ]),
    );
    expect(
      await screen.findByText("Password removed successfully."),
    ).toBeInTheDocument();
  });

  it("offers to set a password to a user without one", async () => {
    serve(user({ password_set: false }));
    renderAt("/users/nm%40example.com");

    expect(
      await screen.findByRole("button", { name: "Set Password" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Remove Password" }),
    ).not.toBeInTheDocument();
  });

  it("unlinks a user from the identity provider", async () => {
    const calls = serve(user());
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Unlink" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Unlink",
      }),
    );

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: "/api/v1/users/nm%40example.com/oidc" },
      ]),
    );
  });

  it("pins a role", async () => {
    const calls = serve(user());
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Pin role/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/users/nm%40example.com",
          body: { role_id: 3, role_pinned: true },
        },
      ]),
    );
  });

  it("shows the server's reason when a role comes from groups", async () => {
    serve(user());
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/users/nm%40example.com": (_url, init) =>
            init?.method === "PUT"
              ? json(409, {
                  error:
                    "This user's role comes from their identity provider groups. Pin the role to change it.",
                })
              : json(200, { result: user() }),
          "/api/v1/users/nm%40example.com/api-tokens": () =>
            json(200, { result: page([]) }),
        }),
      ),
    );
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: /Role/ }));
    fireEvent.click(await screen.findByRole("option", { name: /Read Only/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    expect(
      await within(dialog).findByText(/Pin the role to change it/),
    ).toBeInTheDocument();
  });

  it("does not offer pinning to a user without single sign-on", async () => {
    serve(user({ oidc_linked: false }));
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByRole("checkbox", { name: /Pin role/ }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Unlink" }),
    ).not.toBeInTheDocument();
  });

  it("does not offer a password change to a user without a password", async () => {
    serve(
      user(),
      user({ email: "admin@example.com", role_id: 1, password_set: false }),
    );
    renderAt("/profile");

    expect(
      await screen.findByText(
        "You sign in with single sign-on, and have no password.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change Password" }),
    ).not.toBeInTheDocument();
  });
});
