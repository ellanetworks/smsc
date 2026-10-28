import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Diameter from "@/pages/Diameter";
import { authFor } from "@/contexts/AuthContext";
import { RoleID } from "@/queries/users";
import { json, renderWithClient, stubApi } from "@/test/render";

const status = {
  host: "smsc.example.org",
  realm: "epc.example.org",
  peers: [
    {
      host: "hss.epc.example.org",
      realm: "epc.example.org",
      address: "10.0.0.5",
      state: "open",
      applications: ["s6c", "sgd", "16777999"],
      since: "2026-09-30T12:05:18.843Z",
      configured: false,
    },
    {
      host: "",
      realm: "",
      address: "10.0.0.6",
      state: "connecting",
      applications: [],
      since: "2026-09-30T12:05:18.843Z",
      configured: false,
    },
    {
      id: 1,
      host: "dea1.ipx.example.net",
      realm: "ipx.example.net",
      address: "192.0.2.10",
      state: "open",
      applications: ["s6c", "sgd"],
      since: "2026-09-30T12:05:18.843Z",
      configured: true,
    },
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

const dea = {
  id: 1,
  host: "dea1.ipx.example.net",
  addresses: ["192.0.2.10", "2001:db8::10"],
  port: 3868,
  routes: [
    { default: true, application: "sgd", priority: 0 },
    {
      realm: "epc.example.org",
      application: "s6c",
      priority: 1,
    },
  ],
  status: {
    state: "open",
    host: "dea1.ipx.example.net",
    realm: "ipx.example.net",
    since: "2026-09-30T12:05:18.843Z",
  },
};

type Call = { method: string; path: string; body?: unknown };

const serve = (
  diameter: () => Response,
  peers: (url: URL, init?: RequestInit) => Response = () =>
    json(200, { result: { items: [dea] } }),
) => {
  const calls: Call[] = [];
  const recorded =
    (route: (url: URL, init?: RequestInit) => Response) =>
    (url: URL, init?: RequestInit) => {
      if (init?.method && init.method !== "GET") {
        calls.push({
          method: init.method,
          path: url.pathname,
          ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
        });
      }
      return route(url, init);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/diameter": diameter,
        "/api/v1/diameter/peers": recorded(peers),
        "/api/v1/diameter/peers/1": recorded(() => json(200, { result: dea })),
      }),
    ),
  );

  return calls;
};

const connectedGrid = () =>
  screen.getByRole("grid", { name: "Connected Peers" });
const configuredGrid = () =>
  screen.getByRole("grid", { name: "Configured Peers" });

const rowCells = (text: string, grid = connectedGrid()) =>
  within(
    within(grid).getByText(text).closest(".MuiDataGrid-row") as HTMLElement,
  )
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const settingRows = () =>
  Array.from(
    screen
      .getByRole("table", { name: "Diameter settings" })
      .querySelectorAll(":scope > tbody > tr"),
  ).map((row) => row.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Diameter", () => {
  it("shows this SMSC, the HSS settings and its connected peers", async () => {
    serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />);

    // The configured peer is listed with the configured peers only.
    expect(
      await screen.findByRole("heading", {
        level: 2,
        name: "Connected Peers (2)",
      }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(settingRows()).toEqual([
        "Hostsmsc.example.org",
        "Realmepc.example.org",
      ]),
    );
    expect(
      within(connectedGrid())
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual([
      "Host",
      "Realm",
      "Address",
      "State",
      "HSS",
      "Applications",
      "Since",
    ]);
    expect(rowCells("10.0.0.5")).toEqual([
      "hss.epc.example.org",
      "epc.example.org",
      "10.0.0.5",
      "open",
      "yes",
      "S6c, SGd, 16777999",
      "2026-09-30 12:05:18",
    ]);
    expect(rowCells("10.0.0.6")).toEqual([
      "—",
      "—",
      "10.0.0.6",
      "connecting",
      "no",
      "—",
      "2026-09-30 12:05:18",
    ]);
  });

  it("marks only the peers of the S6c route to this SMSC's realm as HSSs", async () => {
    serve(() =>
      json(200, {
        result: {
          ...status,
          routes: [
            {
              realm: "epc.other.org",
              default: false,
              application: "s6c",
              peers: ["hss.epc.example.org"],
            },
            {
              realm: "EPC.example.org",
              default: false,
              application: "s6c",
              peers: [],
            },
          ],
        },
      }),
    );

    renderWithClient(<Diameter />);

    await screen.findByText("10.0.0.5");
    expect(rowCells("10.0.0.5")[4]).toBe("no");
  });

  it("shows an empty table without peers", async () => {
    serve(() =>
      json(200, {
        result: {
          ...status,
          peers: [],
          routes: [
            {
              realm: "epc.example.org",
              default: false,
              application: "s6c",
              peers: [],
            },
          ],
        },
      }),
    );

    renderWithClient(<Diameter />);

    expect(await screen.findByText("No connected peers.")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Connected Peers (0)" }),
    ).toBeInTheDocument();
  });

  it("keeps the last status and warns when a refresh fails", async () => {
    serve(() => json(200, { result: status }));

    const { client } = renderWithClient(<Diameter />);
    await screen.findByText("10.0.0.5");

    serve(() => json(503, { error: "Service Unavailable" }));
    await client.refetchQueries({ queryKey: ["diameter"] });

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Showing the last loaded Diameter status. Refresh failed: Service Unavailable",
    );
    expect(screen.getByText("10.0.0.5")).toBeInTheDocument();
  });

  it("reports a failure to load", async () => {
    serve(() => json(500, { error: "Failed to load" }));

    renderWithClient(<Diameter />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Failed to load Diameter status");
    expect(alert).toHaveTextContent("Failed to load");
    expect(
      within(alert).getByRole("button", { name: "Retry" }),
    ).toBeInTheDocument();
  });

  it("retries a failed load, and shows its details", async () => {
    let calls = 0;
    serve(() => {
      calls++;
      return json(500, { error: "Failed to load" });
    });

    renderWithClient(<Diameter />);

    const alert = await screen.findByRole("alert");
    fireEvent.click(
      within(alert).getByRole("button", { name: "Show details" }),
    );
    expect(alert).toHaveTextContent("HTTP 500");
    expect(alert).toHaveTextContent("GET /api/v1/diameter");

    const before = calls;
    fireEvent.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(calls).toBe(before + 1));
  });

  it("lists the configured peers, their routes and state", async () => {
    serve(() =>
      json(200, {
        result: {
          ...status,
          routes: [
            {
              realm: "epc.example.org",
              default: false,
              application: "s6c",
              peers: ["dea1.ipx.example.net"],
            },
          ],
        },
      }),
    );

    renderWithClient(<Diameter />);

    expect(
      await screen.findByRole("heading", {
        level: 2,
        name: "Configured Peers (1)",
      }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(rowCells("dea1.ipx.example.net", configuredGrid())).toEqual([
        "dea1.ipx.example.net",
        "192.0.2.10:3868[2001:db8::10]:3868",
        "SGd → DefaultS6c → epc.example.org (priority 1)",
        "open",
        "yes",
        "",
      ]),
    );
  });

  it("shows a configured peer that is down with its error", async () => {
    serve(
      () => json(200, { result: status }),
      () =>
        json(200, {
          result: {
            items: [
              {
                ...dea,
                status: {
                  state: "down",
                  since: "2026-09-30T12:05:18.843Z",
                  error: "connection refused",
                },
              },
            ],
          },
        }),
    );

    renderWithClient(<Diameter />);

    const chip = await within(
      await screen.findByRole("grid", {
        name: "Configured Peers",
      }),
    ).findByText("down");
    fireEvent.mouseOver(chip);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "connection refused",
    );
  });

  it("adds a peer with a default route", async () => {
    const calls = serve(
      () => json(200, { result: status }),
      (_url, init) =>
        init?.method === "POST"
          ? json(201, { result: { ...dea, id: 2 } })
          : json(200, { result: { items: [] } }),
    );

    renderWithClient(<Diameter />);

    fireEvent.click(await screen.findByRole("button", { name: "Add Peer" }));
    const dialog = await screen.findByRole("dialog");

    fireEvent.change(within(dialog).getByLabelText("Host"), {
      target: { value: "dea2.ipx.example.net" },
    });
    fireEvent.change(within(dialog).getByLabelText(/Addresses/), {
      target: { value: "192.0.2.20, 198.51.100.20" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add Route" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/diameter/peers",
          body: {
            host: "dea2.ipx.example.net",
            addresses: ["192.0.2.20", "198.51.100.20"],
            port: 3868,
            routes: [{ default: true, application: "sgd", priority: 0 }],
          },
        },
      ]),
    );
    expect(await screen.findByText("Peer added successfully.")).toBeVisible();
  });

  it("refuses a peer without a valid address", async () => {
    serve(
      () => json(200, { result: status }),
      () => json(200, { result: { items: [] } }),
    );

    renderWithClient(<Diameter />);

    fireEvent.click(await screen.findByRole("button", { name: "Add Peer" }));
    const dialog = await screen.findByRole("dialog");
    const add = within(dialog).getByRole("button", { name: "Add" });

    expect(add).toBeDisabled();

    fireEvent.change(within(dialog).getByLabelText(/Addresses/), {
      target: { value: "dea1.ipx.example.net" },
    });
    expect(
      within(dialog).getByText("dea1.ipx.example.net is not an IP address."),
    ).toBeVisible();
    expect(add).toBeDisabled();
  });

  it("shows the server's reason when it refuses a peer", async () => {
    serve(
      () => json(200, { result: status }),
      (_url, init) =>
        init?.method === "POST"
          ? json(409, {
              error: "conflict: peer 1 has the host dea1.ipx.example.net",
            })
          : json(200, { result: { items: [] } }),
    );

    renderWithClient(<Diameter />);

    fireEvent.click(await screen.findByRole("button", { name: "Add Peer" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/Addresses/), {
      target: { value: "192.0.2.20" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(
      await within(dialog).findByText(
        /Could not add the peer: conflict: peer 1 has the host/,
      ),
    ).toBeVisible();
  });

  it("edits a peer's routes", async () => {
    const calls = serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />);

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit dea1.ipx.example.net" }),
    );
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByLabelText("Host")).toHaveValue(
      "dea1.ipx.example.net",
    );
    expect(within(dialog).getByLabelText(/Addresses/)).toHaveValue(
      "192.0.2.10, 2001:db8::10",
    );

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove route 2" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/diameter/peers/1",
          body: {
            host: "dea1.ipx.example.net",
            addresses: ["192.0.2.10", "2001:db8::10"],
            port: 3868,
            routes: [{ default: true, application: "sgd", priority: 0 }],
          },
        },
      ]),
    );
  });

  it("offers a default route for SGd only", async () => {
    serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />);

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit dea1.ipx.example.net" }),
    );
    const route = within(await screen.findByRole("dialog")).getByRole("group", {
      name: "Route 2",
    });

    fireEvent.mouseDown(within(route).getByLabelText("Destination"));
    expect(
      within(screen.getByRole("listbox")).getByRole("option", {
        name: "Default",
      }),
    ).toHaveAttribute("aria-disabled", "true");
  });

  it("deletes a peer once confirmed", async () => {
    const calls = serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />);

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Delete dea1.ipx.example.net",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Delete dea1.ipx.example.net?");

    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: "/api/v1/diameter/peers/1" },
      ]),
    );
    expect(await screen.findByText("Peer deleted successfully.")).toBeVisible();
  });

  it("lets a Read Only user see the peers, not change them", async () => {
    serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />, authFor("ro@example.com", RoleID.ReadOnly));

    await within(
      await screen.findByRole("grid", { name: "Configured Peers" }),
    ).findByText("dea1.ipx.example.net");
    expect(
      screen.queryByRole("button", { name: "Add Peer" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit dea1.ipx.example.net" }),
    ).not.toBeInTheDocument();
  });
});
