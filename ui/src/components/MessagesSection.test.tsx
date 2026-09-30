import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import MessagesSection from "@/components/MessagesSection";
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

const rowCells = (text: string) =>
  within(screen.getByText(text).closest(".MuiDataGrid-row") as HTMLElement)
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MessagesSection", () => {
  it("lists messages with their part and status", async () => {
    serveMessages([
      message({
        id: 7,
        text: "part two",
        status: "pending",
        concatenation: { reference: 3, part: 2, total: 3 },
        next_attempt_at: "2026-09-30T12:05:00.000Z",
      }),
      message({ id: 8, text: undefined, encoding: "binary" }),
    ]);

    renderWithClient(<MessagesSection />);

    await screen.findByText("part two");
    expect(rowCells("part two")).toEqual([
      "2026-09-30 12:00:00",
      "+15551230001",
      "+15551230002",
      "part two",
      "2 of 3",
      "pending",
    ]);
    expect(rowCells("(binary)")).toContain("—");
    for (const name of ["ID", "Next attempt", "Encoding"]) {
      expect(
        screen.queryByRole("columnheader", { name }),
      ).not.toBeInTheDocument();
    }
  });

  it("requests the first page by default", async () => {
    const requests = serveMessages();

    renderWithClient(<MessagesSection />);

    await screen.findByText("hello");
    expect(lastQuery(requests)).toEqual({ page: "1", per_page: "25" });
  });

  it("filters by recipient once the number is valid", async () => {
    const requests = serveMessages();

    renderWithClient(<MessagesSection />);
    await screen.findByText("hello");

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

  it("filters by status", async () => {
    const requests = serveMessages();

    renderWithClient(<MessagesSection />);
    await screen.findByText("hello");

    fireEvent.mouseDown(screen.getByRole("combobox", { name: "Status" }));
    fireEvent.click(await screen.findByRole("option", { name: "failed" }));

    await waitFor(() =>
      expect(lastQuery(requests)).toMatchObject({ status: "failed" }),
    );
  });

  it("opens the message detail when a row is clicked", async () => {
    serveMessages();

    renderWithClient(<MessagesSection />);
    fireEvent.click(await screen.findByText("hello"));

    const drawer = await screen.findByRole("dialog", { name: "Message 1" });
    expect(
      await within(drawer).findByRole("heading", {
        name: "Delivery attempts (1)",
      }),
    ).toBeInTheDocument();

    fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("sends a message and offers to view it", async () => {
    const created = [
      message({ id: 1, status: "pending" }),
      message({ id: 2, status: "pending" }),
    ];
    const listed: URL[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/messages": (url, init) => {
            if (init?.method === "POST") {
              return json(201, { result: { items: created } });
            }
            listed.push(url);
            return json(200, { result: page([message()]) });
          },
          "/api/v1/messages/1": () => json(200, { result: created[0] }),
          "/api/v1/messages/1/attempts": () => json(200, { result: page([]) }),
        }),
      ),
    );

    renderWithClient(<MessagesSection />);
    await screen.findByText("hello");
    const listedBefore = listed.length;

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
    await waitFor(() => expect(listed.length).toBeGreaterThan(listedBefore));

    fireEvent.click(screen.getByRole("button", { name: "View" }));
    expect(
      await screen.findByRole("dialog", { name: "Message 1" }),
    ).toBeInTheDocument();
  });

  it("shows an empty list", async () => {
    serveMessages([]);

    renderWithClient(<MessagesSection />);

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

    renderWithClient(<MessagesSection />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not load messages: Failed to list messages",
    );
  });
});
