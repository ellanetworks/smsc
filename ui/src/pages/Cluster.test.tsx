import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Cluster from "@/pages/Cluster";
import { authFor } from "@/contexts/AuthContext";
import type { ClusterStatus } from "@/queries/cluster";
import { RoleID } from "@/queries/users";
import { clusterMember, clusterStatus } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = (status: ClusterStatus) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/cluster": () => json(200, { result: status }),
      }),
    ),
  );

const grid = () => screen.getByRole("grid", { name: "Nodes" });

const row = (text: string) =>
  within(grid()).getByText(text).closest(".MuiDataGrid-row") as HTMLElement;

const rowCells = (text: string) =>
  within(row(text))
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const settingRows = () =>
  Array.from(
    screen
      .getByRole("table", { name: "Cluster state" })
      .querySelectorAll(":scope > tbody > tr"),
  ).map((r) => r.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Cluster", () => {
  it("shows the cluster state and every member", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    expect(
      await screen.findByRole("heading", { level: 2, name: "Nodes (3)" }),
    ).toBeInTheDocument();
    expect(settingRows()).toEqual([
      "Cluster ID9f1e2d3c-2222-4b5a-8c7d-6e5f4a3b2c1d",
      "HealthHealthyTolerates 1 voter failure",
      "Leadersmsc",
      "Term3",
      "Applied Index42 applied · 42 committed",
    ]);
    expect(
      within(grid())
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual([
      "Node",
      "Cluster Address",
      "Suffrage",
      "Drain",
      "Health",
      "Applied Index",
      "Last Contact",
      "Version",
      "Joined",
      "Actions",
    ]);
    expect(rowCells("10.0.0.11:7000").slice(0, 8)).toEqual([
      "smsc0b5c6a0eLeaderThis node",
      "10.0.0.11:7000",
      "Voter",
      "Active",
      "Healthy",
      "42",
      "0s",
      "v0.1.0",
    ]);
    expect(rowCells("10.0.0.13:7000").slice(0, 7)).toEqual([
      "smsc32d7e8c2a",
      "10.0.0.13:7000",
      "Nonvoter",
      "Drained",
      "UnhealthyDiverged",
      "30",
      "9.5s",
    ]);
  });

  it("says when no leader answers", async () => {
    serve(
      clusterStatus({
        leader_id: undefined,
        healthy: undefined,
        failure_tolerance: undefined,
        members: [clusterMember({ healthy: undefined })],
      }),
    );
    renderWithClient(<Cluster />);

    expect(await screen.findByText("No leader")).toBeInTheDocument();
  });

  it("explains how to enable high availability on a standalone node", async () => {
    serve(clusterStatus({ clustered: false, members: [clusterMember()] }));
    renderWithClient(<Cluster />);

    expect(
      await screen.findByText("High availability is not enabled"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("grid")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add Node" }),
    ).not.toBeInTheDocument();
  });

  it("lets an admin add a node and act on members", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    fireEvent.click(await screen.findByRole("button", { name: "Add Node" }));
    expect(
      screen.getByRole("dialog", { name: "Add a Node to the Cluster" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    fireEvent.click(
      within(row("10.0.0.12:7000")).getByRole("button", {
        name: "Node actions",
      }),
    );
    expect(
      screen.getAllByRole("menuitem").map((item) => item.textContent),
    ).toEqual([
      "Edit Diameter label",
      "Drain this node",
      "Remove from cluster",
    ]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Drain this node" }));
    expect(
      await screen.findByRole("dialog", { name: "Drain node smsc2?" }),
    ).toBeInTheDocument();
  });

  it("offers to resume a drained node", async () => {
    serve(clusterStatus());
    renderWithClient(<Cluster />);

    await screen.findByRole("grid", { name: "Nodes" });
    fireEvent.click(
      within(row("10.0.0.13:7000")).getByRole("button", {
        name: "Node actions",
      }),
    );
    fireEvent.click(screen.getByRole("menuitem", { name: "Resume this node" }));
    expect(
      await screen.findByRole("dialog", { name: "Resume node smsc3?" }),
    ).toBeInTheDocument();
  });

  it("shows a read-only user the cluster without actions", async () => {
    serve(clusterStatus());
    renderWithClient(
      <Cluster />,
      authFor("viewer@example.com", RoleID.ReadOnly),
    );

    await screen.findByRole("grid", { name: "Nodes" });
    expect(
      screen.queryByRole("button", { name: "Add Node" }),
    ).not.toBeInTheDocument();
    expect(
      within(grid()).queryByRole("columnheader", { name: "Actions" }),
    ).not.toBeInTheDocument();
  });
});
