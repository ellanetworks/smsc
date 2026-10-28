import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Diameter from "@/pages/Diameter";
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

const serve = (diameter: () => Response) =>
  vi.stubGlobal("fetch", vi.fn(stubApi({ "/api/v1/diameter": diameter })));

const rowCells = (text: string) =>
  within(screen.getByText(text).closest(".MuiDataGrid-row") as HTMLElement)
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
  it("shows this SMSC, the HSS settings and its peers", async () => {
    serve(() => json(200, { result: status }));

    renderWithClient(<Diameter />);

    expect(
      await screen.findByRole("heading", {
        level: 2,
        name: "Peers (2)",
      }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(settingRows()).toEqual([
        "Hostsmsc.example.org",
        "Realmepc.example.org",
      ]),
    );
    expect(
      within(screen.getByRole("grid", { name: "Peers" }))
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

    expect(await screen.findByText("No peers.")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Peers (0)" }),
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
});
