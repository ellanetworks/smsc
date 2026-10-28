import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { authFor } from "@/contexts/AuthContext";
import Messages from "@/pages/Messages";
import { RoleID } from "@/queries/users";
import { json, renderWithClient, stubApi } from "@/test/render";
import { attempt, message, page } from "@/test/fixtures";

const serveMessages = (items = [message()], totalCount?: number) => {
  const requests: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/messages": (url) => {
          requests.push(url);
          return json(200, {
            result: page(items, { total_count: totalCount }),
          });
        },
        "/api/v1/messages/1": () => json(200, { result: items[0] }),
        "/api/v1/messages/1/attempts": () =>
          json(200, { result: page([attempt()]) }),
      }),
    ),
  );
  return requests;
};

const lastQuery = (requests: URL[]) =>
  Object.fromEntries(requests[requests.length - 1].searchParams);

// listed waits for the default message's row.
const listed = () => screen.findByRole("gridcell", { name: "+15551230002" });

const rowCells = (to: string) =>
  within(screen.getByText(to).closest(".MuiDataGrid-row") as HTMLElement)
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Messages", () => {
  it("lists messages with their part and status", async () => {
    serveMessages([
      message({
        id: 7,
        to: "+15551230007",
        status: "pending",
        concatenation: { reference: 3, part: 2, total: 3 },
        next_attempt_at: "2026-09-30T12:05:00.000Z",
        submitted_by: "ota@example.com",
      }),
      message({ id: 8, to: "+15551230008", encoding: "binary" }),
    ]);

    renderWithClient(<Messages />);

    await screen.findByText("+15551230007");
    expect(rowCells("+15551230007")).toEqual([
      "2026-09-30 12:00:00",
      "+15551230001",
      "+15551230007",
      "2 of 3",
      "pending",
      "ota@example.com",
    ]);
    expect(rowCells("+15551230008")).toContain("—");
    // The content is not listed: reading it is audited.
    for (const name of ["ID", "Next attempt", "Encoding", "Text"]) {
      expect(
        screen.queryByRole("columnheader", { name }),
      ).not.toBeInTheDocument();
    }
  });

  it("requests the first page by default", async () => {
    const requests = serveMessages();

    renderWithClient(<Messages />);

    await listed();
    expect(lastQuery(requests)).toEqual({ page: "1", per_page: "25" });
  });

  it("filters by recipient once the number is valid", async () => {
    const requests = serveMessages();

    renderWithClient(<Messages />);
    await listed();

    const input = screen.getByRole("textbox", { name: "To" });
    fireEvent.change(input, { target: { value: "+1555" } });
    fireEvent.change(input, { target: { value: "+1555x" } });

    expect(screen.getByText("E.164, e.g. +15551230002")).toBeInTheDocument();

    fireEvent.change(input, { target: { value: "+15551230002" } });

    await waitFor(() =>
      expect(lastQuery(requests)).toEqual({
        page: "1",
        per_page: "25",
        to: "+15551230002",
      }),
    );
    expect(requests.some((r) => r.searchParams.get("to") === "+1555x")).toBe(
      false,
    );
  });

  it("filters by time range", async () => {
    const requests = serveMessages();

    renderWithClient(<Messages />);
    await listed();

    fireEvent.click(
      screen.getByRole("button", { name: "Time range: Any time" }),
    );
    const before = Date.now();
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Last 1 hour" }),
    );

    await waitFor(() => expect(lastQuery(requests).start).toBeDefined());
    const query = lastQuery(requests);
    const start = Date.parse(query.start);
    expect(start).toBeGreaterThanOrEqual(before - 3_600_000);
    expect(start).toBeLessThanOrEqual(Date.now() - 3_600_000);
    expect(query.end).toBeUndefined();
    expect(query.page).toBe("1");

    fireEvent.click(
      screen.getByRole("button", { name: "Time range: Last 1 hour" }),
    );
    const custom = screen
      .getByText("Custom range")
      .closest(".MuiPopover-paper") as HTMLElement;
    fireEvent.change(within(custom).getByLabelText("From"), {
      target: { value: "2026-09-30T10:00" },
    });
    fireEvent.change(within(custom).getByLabelText("To"), {
      target: { value: "2026-09-30T11:30" },
    });

    await waitFor(() =>
      expect(lastQuery(requests)).toMatchObject({
        start: new Date("2026-09-30T10:00").toISOString(),
        end: new Date("2026-09-30T11:30").toISOString(),
      }),
    );

    fireEvent.click(screen.getByRole("menuitem", { name: "Any time" }));
    await waitFor(() =>
      expect(lastQuery(requests)).toEqual({ page: "1", per_page: "25" }),
    );
  });

  it("filters by status", async () => {
    const requests = serveMessages();

    renderWithClient(<Messages />);
    await listed();

    fireEvent.mouseDown(screen.getByRole("combobox", { name: "Status" }));
    fireEvent.click(await screen.findByRole("option", { name: "failed" }));

    await waitFor(() =>
      expect(lastQuery(requests)).toMatchObject({ status: "failed" }),
    );
  });

  it("opens the message detail when a row is clicked", async () => {
    serveMessages();

    renderWithClient(<Messages />);
    fireEvent.click(await listed());

    const drawer = await screen.findByRole("complementary", {
      name: "Message 1",
    });
    expect(
      await within(drawer).findByRole("heading", {
        name: "Delivery attempts (1)",
      }),
    ).toBeInTheDocument();

    fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("complementary")).not.toBeInTheDocument(),
    );
  });

  it("sends a message and offers to view it", async () => {
    const created = [
      message({ id: 1, status: "pending" }),
      message({ id: 2, status: "pending" }),
    ];
    const lists: URL[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/messages": (url, init) => {
            if (init?.method === "POST") {
              return json(201, { result: { items: created } });
            }
            lists.push(url);
            return json(200, { result: page([message()]) });
          },
          "/api/v1/messages/1": () => json(200, { result: created[0] }),
          "/api/v1/messages/1/attempts": () => json(200, { result: page([]) }),
        }),
      ),
    );

    renderWithClient(<Messages />);
    await listed();
    const listedBefore = lists.length;

    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    const dialog = await screen.findByRole("dialog", { name: "Send" });
    for (const [label, value] of [
      ["From", "+15551230001"],
      ["To", "+15551230002"],
      ["Text", "hello"],
    ]) {
      fireEvent.change(within(dialog).getByRole("textbox", { name: label }), {
        target: { value },
      });
    }
    fireEvent.click(within(dialog).getByRole("button", { name: "Send" }));

    expect(
      await screen.findByText("Message queued as 2 parts."),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Send" })).toBeNull(),
    );
    await waitFor(() => expect(lists.length).toBeGreaterThan(listedBefore));

    fireEvent.click(screen.getByRole("button", { name: "View" }));
    expect(
      await screen.findByRole("complementary", { name: "Message 1" }),
    ).toBeInTheDocument();
  });

  it("shows an empty list", async () => {
    serveMessages([]);

    renderWithClient(<Messages />);

    expect(await screen.findByText("No messages.")).toBeInTheDocument();
  });

  it("reports a failure to load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/messages": () =>
            json(500, { error: "Failed to list messages" }),
        }),
      ),
    );

    renderWithClient(<Messages />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Failed to load messages");
    expect(alert).toHaveTextContent("Failed to list messages");
    expect(
      within(alert).getByRole("button", { name: "Retry" }),
    ).toBeInTheDocument();
  });
});

describe("Messages delivery settings", () => {
  const serveDelivery = () => {
    const bodies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/messages": () => json(200, { result: page([]) }),
          "/api/v1/messages/retention": (_url, init) => {
            if (init?.method !== "PUT") {
              return json(200, { result: { days: 30 } });
            }
            const body = JSON.parse(String(init.body));
            bodies.push(body);
            return json(200, { result: body });
          },
          "/api/v1/delivery": (_url, init) => {
            if (init?.method !== "PUT") {
              return json(200, {
                result: {
                  default_validity_seconds: 604800,
                  retry_intervals_seconds: [60, 300, 900, 3600],
                },
              });
            }
            const body = JSON.parse(String(init.body));
            bodies.push(body);
            return json(200, { result: body });
          },
        }),
      ),
    );
    return bodies;
  };

  it("shows and updates the validity and retries", async () => {
    const bodies = serveDelivery();

    renderWithClient(<Messages />);

    const line = screen.getByTestId("delivery-settings");
    await waitFor(() =>
      expect(line).toHaveTextContent("Validity: 7d · Retries: 1m, 5m, 15m, 1h"),
    );

    fireEvent.click(screen.getByRole("button", { name: "Edit delivery" }));
    fireEvent.change(
      screen.getByRole("textbox", { name: "Default Validity" }),
      {
        target: { value: "1 week" },
      },
    );
    expect(screen.getByText("e.g. 30s, 5m, 1h or 7d")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fireEvent.change(
      screen.getByRole("textbox", { name: "Default Validity" }),
      {
        target: { value: "2d" },
      },
    );
    fireEvent.change(screen.getByRole("textbox", { name: "Retry Intervals" }), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(line).toHaveTextContent("Validity: 2d · Retries: none"),
    );
    expect(bodies).toEqual([
      { default_validity_seconds: 172800, retry_intervals_seconds: [] },
    ]);
  });
});

describe("Messages retention", () => {
  it("shows and updates the retention", async () => {
    const bodies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/messages": () => json(200, { result: page([]) }),
          "/api/v1/delivery": () =>
            json(200, {
              result: {
                default_validity_seconds: 604800,
                retry_intervals_seconds: [],
              },
            }),
          "/api/v1/messages/retention": (_url, init) => {
            if (init?.method !== "PUT") {
              return json(200, { result: { days: 30 } });
            }
            const body = JSON.parse(String(init.body));
            bodies.push(body);
            return json(200, { result: body });
          },
        }),
      ),
    );

    renderWithClient(<Messages />);

    const line = screen.getByTestId("delivery-settings");
    await waitFor(() => expect(line).toHaveTextContent("Retention: 30 days"));

    fireEvent.click(screen.getByRole("button", { name: "Edit retention" }));
    const days = screen.getByRole("spinbutton", { name: "Days" });
    fireEvent.change(days, { target: { value: "0" } });
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fireEvent.change(days, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(line).toHaveTextContent("Retention: 1 day"));
    expect(bodies).toEqual([{ days: 1 }]);
  });
});

describe("Messages by role", () => {
  it("lets a Read Only user read, but not send or edit", async () => {
    serveMessages();

    renderWithClient(<Messages />, authFor("ro@example.com", RoleID.ReadOnly));

    await listed();
    expect(
      screen.queryByRole("button", { name: "Send" }),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("delivery-settings")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit delivery" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit retention" }),
    ).not.toBeInTheDocument();
  });

  it("shows a Submitter its messages, without delivery settings or submitters", async () => {
    serveMessages();

    renderWithClient(
      <Messages />,
      authFor("ota@example.com", RoleID.Submitter),
    );

    await listed();
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
    expect(screen.queryByTestId("delivery-settings")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: "Submitted by" }),
    ).not.toBeInTheDocument();
    const paths = vi
      .mocked(fetch)
      .mock.calls.map(
        ([input]) => new URL(String(input), "http://smsc.test").pathname,
      );
    expect(paths).not.toContain("/api/v1/delivery");
    expect(paths).not.toContain("/api/v1/messages/retention");
  });
});
