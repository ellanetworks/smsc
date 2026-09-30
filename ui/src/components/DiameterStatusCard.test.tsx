import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import DiameterStatusCard from "@/components/DiameterStatusCard";
import { json, renderWithClient, stubApi } from "@/test/render";

const status = {
  host: "smsc.example.org",
  realm: "example.org",
  hss_available: true,
  peers: [
    {
      host: "hss.epc.example.org",
      realm: "epc.example.org",
      address: "10.0.0.5",
      state: "open",
      applications: ["s6c", "sgd", "16777999"],
      since: "2026-09-30T12:05:18.843Z",
    },
  ],
};

const serve = (httpStatus: number, body: unknown) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(stubApi({ "/api/v1/diameter": () => json(httpStatus, body) })),
  );

const fields = () =>
  Object.fromEntries(
    Array.from(document.querySelectorAll("dt")).map((dt) => [
      dt.textContent,
      dt.nextElementSibling?.textContent,
    ]),
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DiameterStatusCard", () => {
  it("shows the local identity, HSS reachability and peers", async () => {
    serve(200, { result: status });

    renderWithClient(<DiameterStatusCard />);

    await screen.findByText("smsc.example.org");
    expect(fields()).toEqual({
      Host: "smsc.example.org",
      Realm: "example.org",
      HSS: "reachable",
    });

    const rows = within(
      screen.getByRole("table", { name: "Peers" }),
    ).getAllByRole("row");
    expect(rows).toHaveLength(2);

    const cells = within(rows[1])
      .getAllByRole("cell")
      .map((c) => c.textContent);
    expect(cells).toEqual([
      "hss.epc.example.org",
      "epc.example.org",
      "10.0.0.5",
      "open",
      "S6c, SGd, 16777999",
      "2026-09-30 12:05:18",
    ]);
  });

  it("shows when the HSS is unreachable and there are no peers", async () => {
    serve(200, { result: { ...status, hss_available: false, peers: [] } });

    renderWithClient(<DiameterStatusCard />);

    await screen.findByText("unreachable");
    expect(fields()).toMatchObject({ HSS: "unreachable" });
    expect(screen.getByText("No peers.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows peers whose identity is not yet known", async () => {
    serve(200, {
      result: {
        ...status,
        peers: [
          {
            host: "",
            realm: "",
            address: "10.0.0.6",
            state: "connecting",
            applications: [],
            since: "2026-09-30T12:05:18.843Z",
          },
        ],
      },
    });

    renderWithClient(<DiameterStatusCard />);

    const table = await screen.findByRole("table", { name: "Peers" });
    const cells = within(within(table).getAllByRole("row")[1])
      .getAllByRole("cell")
      .map((c) => c.textContent);
    expect(cells).toEqual([
      "—",
      "—",
      "10.0.0.6",
      "connecting",
      "—",
      "2026-09-30 12:05:18",
    ]);
  });

  it("keeps the last status and warns when a refresh fails", async () => {
    serve(200, { result: status });

    const { client } = renderWithClient(<DiameterStatusCard />);
    await screen.findByText("reachable");

    serve(503, { error: "Service Unavailable" });
    await client.refetchQueries({ queryKey: ["diameter"] });

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Showing the last loaded Diameter status. Refresh failed: Service Unavailable",
    );
    expect(fields()).toMatchObject({ HSS: "reachable" });
  });

  it("reports a failure to load", async () => {
    serve(500, { error: "Failed to load" });

    renderWithClient(<DiameterStatusCard />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not load Diameter status: Failed to load",
    );
  });
});
