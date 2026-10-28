import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import App from "@/App";
import { RoleID } from "@/queries/users";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

// serve answers as an SMSC with a session of a user of role, until a logout, or without a session when role is
// null.
const serve = (role: RoleID | null = RoleID.Admin) => {
  let session = role;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/auth/refresh": () =>
          session === null
            ? json(401, { error: "No valid session" })
            : json(200, {
                result: { token: makeToken("user@example.com", session) },
              }),
        "/api/v1/auth/logout": () => {
          session = null;
          return new Response(null, { status: 204 });
        },
        "/api/v1/diameter": () =>
          json(200, {
            result: {
              host: "smsc.example.org",
              realm: "example.org",
              routes: [],
              peers: [],
            },
          }),
        "/api/v1/operator": () =>
          json(200, {
            result: {
              mcc: "001",
              mnc: "01",
              service_centre_address: "+15550000000",
              numbering: {
                country_code: "1",
                national_prefix: "",
                international_prefix: "",
              },
            },
          }),
        "/api/v1/delivery": () =>
          json(200, {
            result: {
              default_validity_seconds: 604800,
              retry_intervals_seconds: [],
            },
          }),
        "/api/v1/status": () =>
          json(200, {
            result: { version: "v0.0.1", revision: "", initialized: true },
          }),
        "/api/v1/esmes": () => json(200, { result: { items: [] } }),
        "/api/v1/messages": () =>
          json(200, {
            result: { items: [], page: 1, per_page: 25, total_count: 0 },
          }),
      }),
    ),
  );
};

const renderAt = (path: string) =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders the top bar, the navigation and the footer", async () => {
    serve();
    renderAt("/diameter");

    expect(
      await screen.findByRole("img", { name: "Ella SMSC Logo" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("banner")).toHaveTextContent("Ella SMSC");
    expect(
      await within(screen.getByRole("banner")).findByText("v0.0.1"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Main" }))
        .getAllByRole("link")
        .map((link) => [link.textContent, link.getAttribute("href")]),
    ).toEqual([
      ["Operator", "/operator"],
      ["Diameter", "/diameter"],
      ["SMPP", "/smpp"],
      ["Messages", "/messages"],
      ["Users", "/users"],
    ]);
    expect(
      within(screen.getByRole("navigation", { name: "Main" })).getByText(
        "System",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("contentinfo")).toBeInTheDocument();

    const docs = screen.getByRole("link", { name: "Documentation" });
    expect(docs).toHaveAttribute("href", "https://docs.ellanetworks.com/smsc/");
    expect(docs).toHaveAttribute("target", "_blank");

    const bug = screen.getByRole("link", { name: "Report a bug" });
    expect(bug).toHaveAttribute(
      "href",
      "https://github.com/ellanetworks/smsc/issues/new/choose",
    );
    expect(bug).toHaveAttribute("target", "_blank");
  });

  it("opens the Diameter page by default", async () => {
    serve();
    renderAt("/");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Diameter" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Diameter" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(document.title).toBe("Diameter · Ella SMSC");
  });

  it("navigates between pages", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(await screen.findByRole("link", { name: "Operator" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Operator" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("+15550000000")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("link", { name: "Messages" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Messages" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Messages" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("opens the SMPP page", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(await screen.findByRole("link", { name: "SMPP" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "SMPP (0)" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "SMPP" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("shows a Read Only user the SMPP page", async () => {
    serve(RoleID.ReadOnly);
    renderAt("/smpp");

    expect(
      await screen.findByRole("heading", { level: 1, name: "SMPP (0)" }),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Main" }))
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Operator", "Diameter", "SMPP", "Messages"]);
  });

  it("goes to the login page without a session", async () => {
    serve(null);
    renderAt("/messages");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Login" }),
    ).toBeInTheDocument();
  });

  it("shows a Submitter only the Messages page", async () => {
    serve(RoleID.Submitter);
    renderAt("/smpp");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Messages" }),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Main" }))
        .getAllByRole("link")
        .map((link) => link.textContent),
    ).toEqual(["Messages"]);
    expect(screen.queryByText("System")).not.toBeInTheDocument();
  });

  it("logs out from the account menu", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(
      await screen.findByRole("button", { name: "Account menu" }),
    );
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Log Out" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Login" }),
    ).toBeInTheDocument();
    expect(
      vi
        .mocked(fetch)
        .mock.calls.some(([input]) => String(input) === "/api/v1/auth/logout"),
    ).toBe(true);
  });

  it("does not refresh the session on navigation", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(await screen.findByRole("link", { name: "Operator" }));
    await screen.findByRole("heading", { level: 1, name: "Operator" });
    const refreshes = () =>
      vi
        .mocked(fetch)
        .mock.calls.filter(
          ([input]) => String(input) === "/api/v1/auth/refresh",
        ).length;
    const started = refreshes();

    fireEvent.click(screen.getByRole("link", { name: "Messages" }));
    await screen.findByRole("heading", { level: 1, name: "Messages" });
    fireEvent.click(screen.getByRole("link", { name: "Users" }));
    await screen.findByRole("heading", { level: 1, name: /^Users/ });

    expect(refreshes()).toBe(started);
  });

  it("redirects the former Cores page", async () => {
    serve();
    renderAt("/cores");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Diameter" }),
    ).toBeInTheDocument();
  });

  it("offers System, Light and Dark themes, with System checked", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(
      await screen.findByRole("button", { name: "Account menu" }),
    );

    expect(theme("System")).toHaveAttribute("aria-checked", "true");
    expect(theme("Light")).toHaveAttribute("aria-checked", "false");
    expect(theme("Dark")).toHaveAttribute("aria-checked", "false");
  });

  it("closes the menu and checks the theme chosen", async () => {
    serve();
    renderAt("/diameter");

    fireEvent.click(
      await screen.findByRole("button", { name: "Account menu" }),
    );
    fireEvent.click(theme("Dark"));
    await waitFor(() =>
      expect(screen.queryByRole("menu")).not.toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Account menu" }));
    expect(theme("Dark")).toHaveAttribute("aria-checked", "true");
    expect(theme("System")).toHaveAttribute("aria-checked", "false");
    expect(document.documentElement).toHaveClass("dark");
  });
});

const theme = (label: string) =>
  screen.getByRole("menuitemradio", { name: `${label} theme` });
