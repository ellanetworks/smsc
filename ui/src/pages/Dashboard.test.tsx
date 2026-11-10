import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { authFor } from "@/contexts/AuthContext";
import Dashboard from "@/pages/Dashboard";
import { RoleID } from "@/queries/users";
import { formatDateTime } from "@/utils/dates";
import { clusterMember, clusterStatus } from "@/test/fixtures";
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

type Route = (url: URL) => Response;

const serve = ({
  replaced = 0,
  sso = { enabled: false } as { enabled: boolean; display_name?: string },
  routes = {} as Record<string, Route>,
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
        "/api/v1/diameter/peers": () => json(200, { result: { items: [] } }),
        ...routes,
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

    await within(card("Diameter Peers")).findByText("1/2");
    expect(titles("Overview")).toEqual([
      "This SMSC",
      "Diameter Peers",
      "SMPP",
      "High Availability",
    ]);
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

  it("counts the open Diameter peers and says whether the HSS is open", async () => {
    serve();
    render();

    expect(
      await within(card("Diameter Peers")).findByText("1/2"),
    ).toBeInTheDocument();
    expect(card("Diameter Peers")).toHaveTextContent("Open · HSS open");
    expect(
      screen.getByRole("link", { name: /Diameter Peers/ }),
    ).toHaveAccessibleDescription("Not open: mme.epc.example.org.");
  });

  it("says when the HSS is down", async () => {
    serve({
      routes: {
        "/api/v1/diameter": () =>
          json(200, {
            result: {
              ...diameter,
              peers: [
                peer("hss.epc.example.org", "down", "10.0.0.5"),
                peer("mme.epc.example.org", "open", "10.0.0.6"),
              ],
            },
          }),
      },
    });
    render();

    expect(
      await within(card("Diameter Peers")).findByText("1/2"),
    ).toBeInTheDocument();
    expect(card("Diameter Peers")).toHaveTextContent("HSS down");
  });

  it("counts the bound ESMEs and their sessions", async () => {
    serve();
    render();

    expect(await within(card("SMPP")).findByText("1/2")).toBeInTheDocument();
    expect(card("SMPP")).toHaveTextContent("Bound · 2 sessions");
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
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
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

    await within(card("Diameter Peers")).findByText("1/2");
    expect(
      screen.getByRole("link", { name: /Diameter Peers/ }),
    ).toHaveAttribute("href", "/diameter");
    expect(screen.getByRole("link", { name: /SMPP/ })).toHaveAttribute(
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

  it("names a configured peer that has not connected by its addresses", async () => {
    serve({
      routes: {
        "/api/v1/diameter": () =>
          json(200, {
            result: {
              ...diameter,
              peers: [
                { ...peer("", "connecting", ""), id: 1, configured: true },
                { ...peer("", "connecting", ""), id: 2, configured: true },
              ],
            },
          }),
        "/api/v1/diameter/peers": () =>
          json(200, {
            result: {
              items: [
                {
                  id: 1,
                  host: "",
                  addresses: ["192.0.2.10"],
                  port: 3868,
                  routes: [],
                },
                {
                  id: 2,
                  host: "",
                  addresses: ["192.0.2.20", "2001:db8::20"],
                  port: 3868,
                  routes: [],
                },
              ],
            },
          }),
      },
    });
    render();

    await within(card("Diameter Peers")).findByText("0/2");
    await waitFor(() =>
      expect(
        screen.getByRole("link", { name: /Diameter Peers/ }),
      ).toHaveAccessibleDescription(
        "Not open: 192.0.2.10, 192.0.2.20, 2001:db8::20.",
      ),
    );
  });

  it("says when there are no peers and no ESMEs", async () => {
    serve({
      routes: {
        "/api/v1/diameter": () =>
          json(200, { result: { ...diameter, peers: [] } }),
        "/api/v1/esmes": () => json(200, { result: { items: [] } }),
      },
    });
    render();

    expect(
      await within(card("Diameter Peers")).findByText("None"),
    ).toBeInTheDocument();
    expect(await within(card("SMPP")).findByText("None")).toBeInTheDocument();
  });

  it("shows no share when there are no messages", async () => {
    serve({
      routes: {
        "/api/v1/messages/counts": () =>
          json(200, {
            result: {
              pending: 0,
              delivered: 0,
              failed: 0,
              expired: 0,
              replaced: 0,
            },
          }),
      },
    });
    render();

    await screen.findByRole("table", { name: LAST_DAY });
    expect(rows(LAST_DAY).map((r) => r.slice(0, 3))).toEqual([
      ["Delivered", "0", "0%"],
      ["Pending", "0", "0%"],
      ["Failed", "0", "0%"],
      ["Expired", "0", "0%"],
      ["Total", "0", ""],
    ]);
  });

  it("says when a card fails to load", async () => {
    serve({
      routes: {
        "/api/v1/messages/counts": () => json(500, { error: "boom" }),
        "/api/v1/metrics": () => new Response("", { status: 500 }),
      },
    });
    render();

    expect(
      await within(card("Last 24 Hours")).findByText("Failed to load data."),
    ).toBeInTheDocument();
    expect(
      await within(card("Delivery Queue")).findByText("Failed to load data."),
    ).toBeInTheDocument();
    expect(
      within(card("Process Memory")).getByText("Failed to load data."),
    ).toBeInTheDocument();
  });

  it("shows N/A for metrics the SMSC does not report", async () => {
    serve({ routes: { "/api/v1/metrics": () => new Response("") } });
    render();

    expect(
      await within(card("Process Memory")).findByText("N/A"),
    ).toBeInTheDocument();
    expect(within(card("Database Size")).getByText("N/A")).toBeInTheDocument();
    expect(within(card("Up Since")).getByText("N/A")).toBeInTheDocument();
  });

  it("shows when the SMSC started", async () => {
    serve();
    render();

    expect(
      await within(card("Up Since")).findByText(
        formatDateTime(new Date(1.7e12).toISOString()),
      ),
    ).toBeInTheDocument();
  });

  it("opens Single Sign-On for an Admin", async () => {
    serve();
    render();

    await within(card("Single Sign-On")).findByText("Off");
    fireEvent.click(screen.getByRole("link", { name: /Single Sign-On/ }));

    expect(await screen.findByText("At /single-sign-on")).toBeInTheDocument();
  });

  it("opens the messages of a status from its chip", async () => {
    serve();
    render();

    await screen.findByRole("table", { name: LAST_DAY });
    fireEvent.click(screen.getByRole("link", { name: "Expired" }));

    expect(
      await screen.findByText("At /messages?status=expired&range=24h"),
    ).toBeInTheDocument();
  });

  it("describes the cards to assistive technologies", async () => {
    serve();
    render();

    await within(card("Process Memory")).findByText("35 MiB");
    expect(card("Process Memory")).toHaveAttribute("tabindex", "0");
    expect(card("Process Memory")).not.toHaveAttribute("aria-label");
    fireEvent.mouseOver(card("Process Memory"));

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "resident set size",
    );
    expect(card("Process Memory")).toHaveAccessibleDescription(
      /resident set size/,
    );
  });

  it("shows the healthy voters of the cluster", async () => {
    serve({
      routes: {
        "/api/v1/cluster": () =>
          json(200, {
            result: clusterStatus({
              healthy: false,
              members: [
                clusterMember({ leader: true }),
                clusterMember({ node_id: "b", healthy: false }),
                clusterMember({ node_id: "c", suffrage: "nonvoter" }),
              ],
            }),
          }),
      },
    });
    render();

    expect(
      await within(card("High Availability")).findByText("1/2"),
    ).toBeInTheDocument();
  });

  it("leaves the cluster health to the leader on a follower", async () => {
    serve({
      routes: {
        "/api/v1/cluster": () =>
          json(200, {
            result: clusterStatus({ leader_id: "b", healthy: undefined }),
          }),
      },
    });
    render();

    expect(
      await within(card("High Availability")).findByText("Follower"),
    ).toBeInTheDocument();
  });

  it("shows high availability off on a standalone node", async () => {
    serve({
      routes: {
        "/api/v1/cluster": () =>
          json(200, { result: clusterStatus({ clustered: false }) }),
      },
    });
    render();

    expect(
      await within(card("High Availability")).findByText("Off"),
    ).toBeInTheDocument();
  });

  it("hides high availability from users who cannot read the cluster", async () => {
    serve();
    render(authFor("ota@example.com", RoleID.Submitter));

    await screen.findByRole("heading", { level: 3, name: "This SMSC" });
    expect(
      screen.queryByRole("heading", { level: 3, name: "High Availability" }),
    ).not.toBeInTheDocument();
  });
});
