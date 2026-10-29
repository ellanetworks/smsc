import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { authFor } from "@/contexts/AuthContext";
import MessageDrawer, { inProgress } from "@/components/MessageDrawer";
import type { Attempt, Message, MessageContent } from "@/queries/messages";
import { RoleID } from "@/queries/users";
import { json, renderWithClient, stubApi } from "@/test/render";
import { attempt, message, page } from "@/test/fixtures";

const serve = (
  m: Message,
  attemptPages: Attempt[][],
  total?: number,
  content: MessageContent = { text: "hello" },
) => {
  const attemptRequests: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        [`/api/v1/messages/${m.id}`]: () => json(200, { result: m }),
        [`/api/v1/messages/${m.id}/content`]: () =>
          json(200, { result: content }),
        [`/api/v1/messages/${m.id}/attempts`]: (url) => {
          attemptRequests.push(url);
          const n = Number(url.searchParams.get("page"));
          return json(200, {
            result: page(attemptPages[n - 1] ?? [], {
              page: n,
              per_page: 100,
              total_count:
                total ?? attemptPages.reduce((s, p) => s + p.length, 0),
            }),
          });
        },
      }),
    ),
  );
  return attemptRequests;
};

const fields = (container: HTMLElement) =>
  Object.fromEntries(
    Array.from(container.querySelectorAll("dt")).map((dt) => [
      dt.textContent,
      dt.nextElementSibling?.textContent,
    ]),
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MessageDrawer", () => {
  it("shows the message and its delivery attempts", async () => {
    const m = message({
      status: "pending",
      concatenation: { reference: 9, part: 1, total: 2 },
      next_attempt_at: "2026-09-30T12:05:00.000Z",
    });
    serve(m, [
      [
        attempt({ id: 1, step: "routing", node: "mme.example.org" }),
        attempt({
          id: 2,
          step: "delivery",
          node: "mme.example.org",
          node_type: "mme",
          outcome: "absent_user",
          result_code: 5550,
          vendor_id: 10415,
          completed_at: "2026-09-30T12:00:02.500Z",
          absent_user_diagnostics: { mme: "ue_not_reachable" },
        }),
      ],
    ]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary", { name: "Message 1" });
    expect(within(drawer).getByText("Pending")).toBeInTheDocument();
    expect(fields(drawer)).toMatchObject({
      Part: "1 of 2",
      Reference: "9",
      Encoding: "GSM-7",
      "Next attempt": "2026-09-30 12:05:00",
    });

    const items = await within(drawer).findAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(
      within(items[1]).getByRole("heading", { name: "2. Delivery" }),
    ).toBeInTheDocument();
    expect(within(items[1]).getByText("Absent user")).toBeInTheDocument();
    expect(fields(items[1])).toEqual({
      Started: "2026-09-30 12:00:00",
      Duration: "2.5 s",
      Node: "mme.example.org (MME)",
      "Result code": "5550 (vendor 10415)",
      "Absent (MME)": "ue_not_reachable",
    });
    expect(fields(items[0])).toMatchObject({
      Duration: "250 ms",
      Node: "mme.example.org",
    });
  });

  it("shows the SMPP system ID of a message submitted over SMPP", async () => {
    const m = message({ submitted_by: "ota@example.com", system_id: "ota1" });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary", { name: "Message 1" });
    expect(fields(drawer)).toMatchObject({
      "Submitted by": "ota@example.com",
      "SMPP system ID": "ota1",
    });
    await within(drawer).findByRole("heading", {
      name: /^Delivery attempts/,
    });
  });

  it("shows no SMPP system ID for a message not submitted over SMPP", async () => {
    const m = message({ submitted_by: "api@example.com" });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary", { name: "Message 1" });
    expect(fields(drawer)).toMatchObject({ "Submitted by": "api@example.com" });
    expect(fields(drawer)).not.toHaveProperty("SMPP system ID");
    await within(drawer).findByRole("heading", {
      name: /^Delivery attempts/,
    });
  });

  it("loads more attempts on request", async () => {
    const m = message();
    const first = Array.from({ length: 100 }, (_, i) => attempt({ id: i + 1 }));
    const requests = serve(m, [first, [attempt({ id: 101 })]], 101);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary");
    expect(
      await within(drawer).findByRole("heading", {
        name: "Delivery attempts (101)",
      }),
    ).toBeInTheDocument();
    expect(await within(drawer).findAllByRole("listitem")).toHaveLength(100);

    fireEvent.click(within(drawer).getByRole("button", { name: "Load more" }));

    expect(
      await within(drawer).findByRole("heading", { name: "101. Routing" }),
    ).toBeInTheDocument();
    expect(
      within(drawer).queryByRole("button", { name: "Load more" }),
    ).not.toBeInTheDocument();
    expect(requests.map((r) => r.searchParams.get("page"))).toEqual(["1", "2"]);
  });

  it("shows the content only on request, once", async () => {
    const m = message({ status: "pending" });
    serve(m, [[]]);
    const reads = () =>
      vi
        .mocked(fetch)
        .mock.calls.filter(([input]) => String(input).endsWith("/content"))
        .length;

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary");
    expect(
      await within(drawer).findByText("No delivery attempts yet."),
    ).toBeInTheDocument();
    expect(within(drawer).queryByText("hello")).not.toBeInTheDocument();
    expect(reads()).toBe(0);

    fireEvent.click(
      within(drawer).getByRole("button", { name: "Show content" }),
    );

    expect(await within(drawer).findByText("hello")).toBeInTheDocument();
    expect(
      within(drawer).queryByRole("button", { name: "Show content" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(reads()).toBe(1));
  });

  it("does not offer the content to a Read Only user", () => {
    const m = message();
    serve(m, [[]]);

    renderWithClient(
      <MessageDrawer message={m} onClose={() => {}} />,
      authFor("ro@example.com", RoleID.ReadOnly),
    );

    expect(
      screen.queryByRole("heading", { name: "Content" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Show content" }),
    ).not.toBeInTheDocument();
  });

  it("shows a binary message without attempts", async () => {
    const m = message({ encoding: "binary" });
    serve(m, [[]], undefined, {});

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary");
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Show content" }),
    );
    expect(
      await within(drawer).findByText("Binary content is not shown."),
    ).toBeInTheDocument();
    expect(
      await within(drawer).findByText("No delivery attempts."),
    ).toBeInTheDocument();
  });

  it("shows the TPDU parameters of a binary message, and the deliver reports", async () => {
    const m = message({ encoding: "binary" });
    serve(m, [[]], undefined, {
      binary: {
        protocol_identifier: 0x7f,
        data_coding_scheme: 0xf6,
        user_data_header: "027000",
        user_data: "0011223344",
      },
      deliver_reports: [
        {
          attempt_id: 3,
          tpdu: "00077ff602cafe",
          protocol_identifier: 0x7f,
          data_coding_scheme: 0xf6,
          user_data: "cafe",
        },
        { attempt_id: 5, tpdu: "01" },
      ],
    });

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("complementary");
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Show content" }),
    );

    const content = await within(drawer).findByRole("region", {
      name: "Content",
    });
    const reports = await within(content).findByRole("region", {
      name: "Deliver reports (2)",
    });
    const [first, second] = within(reports).getAllByRole("listitem");
    expect(fields(first)).toEqual({
      TPDU: "00077ff602cafe",
      "Protocol identifier": "0x7f",
      "Data coding scheme": "0xf6",
      "User data": "cafe",
    });
    expect(fields(second)).toEqual({ TPDU: "01" });

    const binary = content.querySelector("dl")!;
    expect(fields(binary as HTMLElement)).toEqual({
      "Protocol identifier": "0x7f",
      "Data coding scheme": "0xf6",
      "User data header": "027000",
      "User data": "0011223344",
    });
    expect(
      within(content).queryByText("Binary content is not shown."),
    ).not.toBeInTheDocument();
  });

  it("shows the status reports on the message", () => {
    const m = message({
      status: "failed",
      status_report: {
        requested: true,
        reports: [
          {
            id: 2,
            tp_status: 0x62,
            tp_status_name: "no_response",
            status: "pending",
            created_at: "2026-09-30T12:30:00.000Z",
            updated_at: "2026-09-30T12:30:00.000Z",
            expires_at: "2026-10-01T12:30:00.000Z",
            next_attempt_at: "2026-09-30T12:35:00.000Z",
          },
        ],
      },
    });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const section = within(screen.getByRole("complementary")).getByRole(
      "region",
      {
        name: "Status reports (1)",
      },
    );
    const reports = within(section).getAllByRole("listitem");
    expect(reports).toHaveLength(1);
    expect(
      within(reports[0]).getByRole("heading", { name: "no response (0x62)" }),
    ).toBeInTheDocument();
    expect(within(reports[0]).getByText("Pending")).toBeInTheDocument();
    expect(fields(reports[0])).toEqual({
      Created: "2026-09-30 12:30:00",
      Updated: "2026-09-30 12:30:00",
      "Next attempt": "2026-09-30 12:35:00",
    });
  });

  it.each([
    [false, "Not requested."],
    [true, "None yet."],
  ])("says when there is no report (requested: %s)", (requested, text) => {
    const m = message({ status_report: { requested, reports: [] } });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const section = within(screen.getByRole("complementary")).getByRole(
      "region",
      {
        name: "Status reports",
      },
    );
    expect(within(section).getByText(text)).toBeInTheDocument();
    expect(within(section).queryByRole("listitem")).not.toBeInTheDocument();
  });

  it("tells which earlier message to the same number a pending one waits behind", async () => {
    const m = message({ id: 7, status: "pending", to: "+15551230002" });
    const lists: URL[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          [`/api/v1/messages/${m.id}/attempts`]: () =>
            json(200, { result: page([]) }),
          "/api/v1/messages": (url) => {
            lists.push(url);
            return json(200, {
              result: page([
                m,
                // A longer number that the to filter, which matches by part, also returns.
                message({ id: 6, status: "pending", to: "+155512300021" }),
                message({ id: 5, status: "pending", to: "+15551230002" }),
                message({ id: 2, status: "pending", to: "+15551230002" }),
              ]),
            });
          },
        }),
      ),
    );

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    expect(
      await within(screen.getByRole("complementary")).findByText(
        "Waits behind message 5 to the same number.",
      ),
    ).toBeInTheDocument();
    expect(lists[0].searchParams.get("to")).toBe("+15551230002");
    expect(lists[0].searchParams.get("status")).toBe("pending");
  });

  it("does not wait for attempts on an expired message", async () => {
    const m = message({ status: "expired" });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    expect(
      await within(screen.getByRole("complementary")).findByText(
        "No delivery attempts.",
      ),
    ).toBeInTheDocument();
  });

  it("spells out the acronyms of an outcome", async () => {
    const m = message();
    serve(m, [[attempt({ id: 1, step: "routing", outcome: "no_hss" })]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    expect(
      await within(screen.getByRole("complementary")).findByText(
        "No HSS connected",
      ),
    ).toBeInTheDocument();
  });

  it("shows a replaced message", () => {
    const m = message({ status: "replaced" });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    expect(
      within(screen.getByRole("complementary")).getByText("Replaced"),
    ).toBeInTheDocument();
  });

  it("takes focus when it opens, and gives it back when it closes", () => {
    const m = message();
    serve(m, [[]]);
    function Harness() {
      const [open, setOpen] = useState<Message | null>(null);
      return (
        <>
          <button onClick={() => setOpen(m)}>Open</button>
          <MessageDrawer message={open} onClose={() => setOpen(null)} />
        </>
      );
    }

    renderWithClient(<Harness />);
    const opener = screen.getByRole("button", { name: "Open" });
    opener.focus();
    fireEvent.click(opener);

    expect(screen.getByRole("complementary")).toHaveFocus();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(opener).toHaveFocus();
  });

  it("closes on Escape", () => {
    const m = message();
    serve(m, [[]]);
    const onClose = vi.fn();

    renderWithClient(<MessageDrawer message={m} onClose={onClose} />);
    fireEvent.keyDown(window, { key: "Escape" });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("renders nothing when no message is selected", () => {
    renderWithClient(<MessageDrawer message={null} onClose={() => {}} />);

    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
  });

  it("refreshes while the message or a report on it is pending", () => {
    const report = (status: "pending" | "delivered") => ({
      id: 2,
      tp_status: 0,
      tp_status_name: "received",
      status,
      created_at: "2026-09-30T12:01:00.000Z",
      updated_at: "2026-09-30T12:01:00.000Z",
      expires_at: "2026-10-01T12:01:00.000Z",
    });
    const reports = (status: "pending" | "delivered") => ({
      requested: true,
      reports: [report(status)],
    });

    expect(inProgress(message({ status: "pending" }))).toBe(true);
    expect(inProgress(message())).toBe(false);
    expect(inProgress(message({ status_report: reports("pending") }))).toBe(
      true,
    );
    expect(inProgress(message({ status_report: reports("delivered") }))).toBe(
      false,
    );
  });
});
