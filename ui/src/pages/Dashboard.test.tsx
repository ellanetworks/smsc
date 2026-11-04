import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { authFor } from "@/contexts/AuthContext";
import Dashboard from "@/pages/Dashboard";
import { RoleID } from "@/queries/users";
import { json, renderWithClient, stubApi } from "@/test/render";

const peer = (host: string, state: string, address: string) => ({
  host,
  realm: "epc.example.org",
  address,
  state,
  applications: ["s6c", "sgd"],
  since: "2026-09-30T12:05:18.843Z",
  configured: false,
});

const diameter = {
  host: "smsc.epc.example.org",
  realm: "epc.example.org",
  peers: [
    peer("hss.epc.example.org", "open", "10.0.0.5"),
    peer("mme.epc.example.org", "down", "10.0.0.6"),
  ],
  routes: [
    {
      realm: "epc.example.org",
      default: false,
      application: "s6c",
      peers: ["hss.epc.example.org"],
    },
  ],
};

const session = {
  bind_type: "transceiver",
  remote_address: "10.0.0.9:40000",
  bound_at: "2026-09-30T12:05:18.843Z",
};

const esme = (systemID: string, sessions: unknown[]) => ({
  system_id: systemID,
  user: "admin@example.com",
  allowed_networks: [],
  created_at: "2026-09-30T12:05:18.843Z",
  sessions,
});

const metrics = `# HELP ellasmsc_messages_pending Pending.
# TYPE ellasmsc_messages_pending gauge
ellasmsc_messages_pending{state="due",type="message"} 2
ellasmsc_messages_pending{state="due",type="status_report"} 1
ellasmsc_messages_pending{state="waiting",type="message"} 40
ellasmsc_messages_pending{state="waiting",type="status_report"} 2
ellasmsc_database_storage_bytes{file="main"} 1048576
ellasmsc_database_storage_bytes{file="wal"} 524288
process_resident_memory_bytes 36700160
process_start_time_seconds 1.7e+09
`;

const serve = ({
  replaced = 0,
  sso = { enabled: false } as { enabled: boolean; display_name?: string },
} = {}) => {
  const counts: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
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
        "/api/v1/auth/oidc": () => json(200, { result: sso }),
        "/api/v1/diameter": () => json(200, { result: diameter }),
        "/api/v1/esmes": () =>
          json(200, {
            result: {
              items: [esme("billing", [session, session]), esme("alerts", [])],
            },
          }),
        "/api/v1/metrics": () => new Response(metrics),
        "/api/v1/messages/counts": (url) => {
          counts.push(url);
          return json(200, {
            result: {
              pending: 10,
              delivered: 80,
              failed: 6,
              expired: 4,
              replaced,
            },
          });
        },
      }),
    ),
  );
  return counts;
};

const card = (title: string) =>
  screen
    .getByRole("heading", { level: 3, name: title })
    .closest(".MuiCard-root") as HTMLElement;

function Location() {
  const { pathname, search } = useLocation();
  return <p>At {pathname + search}</p>;
}

const render = (auth = authFor("admin@example.com", RoleID.Admin)) =>
  renderWithClient(
    <MemoryRouter>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="*" element={<Location />} />
      </Routes>
    </MemoryRouter>,
    auth,
  );

const rows = (table: string) =>
  within(screen.getByRole("table", { name: table }))
    .getAllByRole("row")
    .slice(1)
    .map((row) =>
      within(row)
        .getAllByRole("cell")
        .map((c) => c.textContent),
    );

const LAST_DAY = "Messages in the last 24 hours";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Dashboard", () => {
  it("groups the cards into Overview, Messages and System", async () => {
    serve();
    render();

    const titles = (section: string) =>
      within(screen.getByRole("region", { name: section }))
        .getAllByRole("heading", { level: 3 })
        .map((h) => h.textContent);

    await screen.findByRole("table", { name: "Diameter Peers" });
    expect(titles("Overview")).toEqual(["This SMSC", "Diameter Peers", "SMPP"]);
    expect(titles("Messages")).toEqual(["Last 24 Hours", "Delivery Queue"]);
    expect(titles("System")).toEqual([
      "Process Memory",
      "Database Size",
      "Single Sign-On",
      "Up Since",
    ]);
  });

  it("shows this SMSC's identity", async () => {
    serve();
    render();

    expect(
      await within(card("This SMSC")).findByText("001 / 01"),
    ).toBeInTheDocument();
    expect(card("This SMSC")).toHaveTextContent("+15550000000");
  });

  it("lists the Diameter peers, tagging the HSS", async () => {
    serve();
    render();

    await screen.findByRole("table", { name: "Diameter Peers" });
    expect(rows("Diameter Peers")).toEqual([
      ["hss.epc.example.orgHSS", "Open"],
      ["mme.epc.example.org", "Down"],
    ]);
  });

  it("lists the ESMEs, bound or not", async () => {
    serve();
    render();

    await screen.findByRole("table", { name: "ESMEs" });
    expect(rows("ESMEs")).toEqual([
      ["billing", "Bound"],
      ["alerts", "Not bound"],
    ]);
  });

  it("counts the messages of the last 24 hours by status", async () => {
    const counts = serve();
    const before = Date.now();
    render();

    await screen.findByRole("table", { name: LAST_DAY });
    expect(rows(LAST_DAY).map((r) => r.slice(0, 3))).toEqual([
      ["Delivered", "80", "80%"],
      ["Pending", "10", "10%"],
      ["Failed", "6", "6%"],
      ["Expired", "4", "4%"],
      ["Total", "100", ""],
    ]);
    const start = Date.parse(counts[0].searchParams.get("start") ?? "");
    expect(before - start).toBeGreaterThan(24 * 60 * 60_000 - 1000);
    expect(before - start).toBeLessThan(24 * 60 * 60_000 + 1000);
  });

  it("shows replaced messages only when there are some", async () => {
    serve({ replaced: 5 });
    render();

    await screen.findByRole("table", { name: LAST_DAY });
    expect(rows(LAST_DAY)[4].slice(0, 2)).toEqual(["Replaced", "5"]);
  });

  it("opens the messages of a status in the last 24 hours", async () => {
    serve();
    render();

    await screen.findByRole("table", { name: LAST_DAY });
    fireEvent.click(
      within(screen.getByRole("table", { name: LAST_DAY })).getByText("6"),
    );

    expect(
      await screen.findByText("At /messages?status=failed&range=24h"),
    ).toBeInTheDocument();
  });

  it("shows the delivery queue and the system", async () => {
    serve();
    render();

    expect(
      await within(card("Delivery Queue")).findByText("3"),
    ).toBeInTheDocument();
    expect(within(card("Delivery Queue")).getByText("42")).toBeInTheDocument();
    expect(card("Process Memory")).toHaveTextContent("35 MiB");
    expect(card("Database Size")).toHaveTextContent("1.5 MiB");
    expect(
      await within(card("Single Sign-On")).findByText("Off"),
    ).toBeInTheDocument();
  });

  it("names the identity provider when single sign-on is on", async () => {
    serve({ sso: { enabled: true, display_name: "Okta" } });
    render();

    expect(
      await within(card("Single Sign-On")).findByText("On"),
    ).toBeInTheDocument();
    expect(card("Single Sign-On")).toHaveTextContent("Okta");
  });

  it("opens Single Sign-On only for those who can read it", async () => {
    serve();
    render(authFor("ro@example.com", RoleID.ReadOnly));

    await within(card("Single Sign-On")).findByText("Off");
    fireEvent.click(card("Single Sign-On"));

    expect(screen.queryByText(/^At /)).not.toBeInTheDocument();
  });

  it("links the card titles to their pages", async () => {
    serve();
    render();

    await screen.findByRole("table", { name: "Diameter Peers" });
    expect(
      screen.getByRole("link", { name: "Diameter Peers" }),
    ).toHaveAttribute("href", "/diameter");
    expect(screen.getByRole("link", { name: "SMPP" })).toHaveAttribute(
      "href",
      "/smpp",
    );
    expect(screen.getByRole("link", { name: "This SMSC" })).toHaveAttribute(
      "href",
      "/operator",
    );
    expect(screen.getByRole("link", { name: "Last 24 Hours" })).toHaveAttribute(
      "href",
      "/messages?range=24h",
    );
  });
});
