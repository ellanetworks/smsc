import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import App from "@/App";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = () =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
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
        "/api/v1/messages": () =>
          json(200, {
            result: { items: [], page: 1, per_page: 25, total_count: 0 },
          }),
      }),
    ),
  );

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
  it("renders the top bar, the navigation and the footer", () => {
    serve();
    renderAt("/cores");

    expect(
      screen.getByRole("img", { name: "Ella SMSC Logo" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("banner")).toHaveTextContent("Ella SMSC");
    expect(
      within(screen.getByRole("navigation", { name: "Main" }))
        .getAllByRole("link")
        .map((link) => [link.textContent, link.getAttribute("href")]),
    ).toEqual([
      ["Operator", "/operator"],
      ["Cores", "/cores"],
      ["Messages", "/messages"],
    ]);
    expect(screen.getByRole("contentinfo")).toBeInTheDocument();

    const docs = screen.getByRole("link", { name: "Documentation" });
    expect(docs).toHaveAttribute("href", "https://docs.ellanetworks.com");
    expect(docs).toHaveAttribute("target", "_blank");

    const bug = screen.getByRole("link", { name: "Report a bug" });
    expect(bug).toHaveAttribute(
      "href",
      "https://github.com/ellanetworks/smsc/issues/new/choose",
    );
    expect(bug).toHaveAttribute("target", "_blank");
  });

  it("opens the Cores page by default", async () => {
    serve();
    renderAt("/");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Cores" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cores" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(document.title).toBe("Cores · Ella SMSC");
  });

  it("navigates between pages", async () => {
    serve();
    renderAt("/cores");

    fireEvent.click(screen.getByRole("link", { name: "Operator" }));
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
});
