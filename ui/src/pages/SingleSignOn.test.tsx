import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import SingleSignOn from "@/pages/SingleSignOn";
import type { OIDCSettings } from "@/queries/oidc";
import { json, renderWithClient, stubApi } from "@/test/render";

const settings = (overrides: Partial<OIDCSettings> = {}): OIDCSettings => ({
  enabled: true,
  external_url: "https://smsc.example.com",
  redirect_url: "https://smsc.example.com/api/v1/auth/oidc/callback",
  issuer: "https://idp.example.com/realms/smsc",
  client_id: "smsc",
  client_secret_set: true,
  display_name: "Keycloak",
  scopes: ["openid", "email", "profile"],
  session_lifetime_seconds: 86400,
  trust_unverified_email: false,
  ca_certificate: "",
  groups_claim: "groups",
  group_roles: [
    { group: "noc", role_id: 3 },
    { group: "ops", role_id: 2 },
  ],
  ...overrides,
});

const serve = ({
  current = settings(),
  discovery = () =>
    json(200, {
      result: {
        issuer: "https://idp.example.com/realms/smsc",
        authorization_endpoint: "",
        token_endpoint: "",
        jwks_uri: "",
        signing_algorithms: ["RS256"],
        keys: 1,
        pkce: true,
        issuer_parameter: true,
      },
    }),
}: {
  current?: OIDCSettings;
  discovery?: () => Response;
} = {}) => {
  const bodies: { path: string; body: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/oidc": (url, init) => {
          if (init?.method === "PUT") {
            const body = JSON.parse(String(init.body));
            bodies.push({ path: url.pathname, body });
            return json(200, { result: { ...current, ...body } });
          }
          return json(200, { result: current });
        },
        "/api/v1/oidc/discovery": (url, init) => {
          bodies.push({
            path: url.pathname,
            body: JSON.parse(String(init?.body)),
          });
          return discovery();
        },
      }),
    ),
  );
  return bodies;
};

const renderPage = () =>
  renderWithClient(
    <MemoryRouter>
      <SingleSignOn />
    </MemoryRouter>,
  );

const field = (label: string) =>
  screen.getByLabelText(label, { exact: false, selector: "input, textarea" });

const fill = (label: string, value: string) =>
  fireEvent.change(field(label), { target: { value } });

const openEditor = async () => {
  const edit = await screen.findByRole("button", { name: "Edit" });
  await waitFor(() => expect(edit).toBeEnabled());
  fireEvent.click(edit);
  return screen.findByRole("dialog");
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Single sign-on", () => {
  it("shows the settings and the redirect URL, never the secret", async () => {
    serve();
    renderPage();

    const table = await screen.findByRole("table", {
      name: "Single sign-on settings",
    });
    await within(table).findByText("Enabled");
    expect(
      within(table).getByText(
        "https://smsc.example.com/api/v1/auth/oidc/callback",
      ),
    ).toBeInTheDocument();
    expect(within(table).getByText("Set")).toBeInTheDocument();
    expect(within(table).getByText("24 hours")).toBeInTheDocument();
    expect(
      within(table).getByText("1. noc → Network Manager"),
    ).toBeInTheDocument();
    expect(within(table).getByText("2. ops → Read Only")).toBeInTheDocument();
  });

  it("asks for the external URL before showing a redirect URL", async () => {
    serve({
      current: settings({ enabled: false, external_url: "", redirect_url: "" }),
    });
    renderPage();

    expect(
      await screen.findByText("Set the external URL first."),
    ).toBeInTheDocument();
  });

  it("saves the settings, keeping the secret when left empty", async () => {
    const bodies = serve();
    renderPage();

    const dialog = await openEditor();
    fill("Display name", "Corporate SSO");
    fill("Session lifetime (hours)", "8");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move group 2 up" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toEqual({
      enabled: true,
      external_url: "https://smsc.example.com",
      issuer: "https://idp.example.com/realms/smsc",
      client_id: "smsc",
      client_secret: "",
      display_name: "Corporate SSO",
      scopes: ["openid", "email", "profile"],
      session_lifetime_seconds: 28800,
      trust_unverified_email: false,
      ca_certificate: "",
      groups_claim: "groups",
      group_roles: [
        { group: "ops", role_id: 2 },
        { group: "noc", role_id: 3 },
      ],
    });
  });

  it("refuses settings the API would refuse", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    const update = within(dialog).getByRole("button", { name: "Update" });

    for (const [label, value, message] of [
      [
        "Issuer URL",
        "http://idp.example.com",
        "An https URL with no query or fragment.",
      ],
      [
        "External URL",
        "https://smsc.example.com/ui",
        "The https address of Ella SMSC, with no path.",
      ],
      ["Scopes", "email profile", "Include openid."],
      [
        "Session lifetime (hours)",
        "0.01",
        "From 5 minutes (0.09) to 30 days (720).",
      ],
    ] as const) {
      const before = (field(label) as HTMLInputElement).value;
      fill(label, value);
      expect(await within(dialog).findByText(message)).toBeInTheDocument();
      expect(update).toBeDisabled();
      fill(label, before);
      await waitFor(() => expect(update).toBeEnabled());
    }

    fireEvent.click(within(dialog).getByRole("button", { name: "Add group" }));
    expect(
      await within(dialog).findByText(
        "Each group must be named, and appear once.",
      ),
    ).toBeInTheDocument();
    expect(update).toBeDisabled();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove group 3" }),
    );
    await waitFor(() => expect(update).toBeEnabled());
  });

  it("needs a client secret to enable single sign-on the first time", async () => {
    serve({
      current: settings({ enabled: false, client_secret_set: false }),
    });
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(within(dialog).getByRole("switch", { name: "Enabled" }));

    expect(
      await within(dialog).findByText("Enter the client secret."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Update" }),
    ).toBeDisabled();
  });

  it("tests the identity provider before saving", async () => {
    const bodies = serve();
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(within(dialog).getByRole("button", { name: "Test" }));

    expect(
      await within(dialog).findByText(
        "Ella SMSC can use this identity provider.",
      ),
    ).toBeInTheDocument();
    expect(bodies).toEqual([
      {
        path: "/api/v1/oidc/discovery",
        body: {
          issuer: "https://idp.example.com/realms/smsc",
          ca_certificate: "",
        },
      },
    ]);
  });

  it("shows why the identity provider cannot be used", async () => {
    serve({
      discovery: () =>
        json(502, { error: "the identity provider: discovery: 404 Not Found" }),
    });
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(within(dialog).getByRole("button", { name: "Test" }));

    expect(
      await within(dialog).findByText(/discovery: 404 Not Found/),
    ).toBeInTheDocument();
  });

  it("warns before trusting unverified email addresses", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(
      within(dialog).getByRole("switch", {
        name: "Trust email addresses from this provider",
      }),
    );

    expect(
      await within(dialog).findByText(
        /linked by email even when the provider has not verified it/,
      ),
    ).toBeInTheDocument();
  });
});
