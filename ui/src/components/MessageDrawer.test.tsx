import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import MessageDrawer from "@/components/MessageDrawer";
import type { Attempt, Message } from "@/queries/messages";
import { json, renderWithClient, stubApi } from "@/test/render";
import { attempt, message, page } from "@/test/fixtures";

const serve = (m: Message, attemptPages: Attempt[][], total?: number) => {
  const attemptRequests: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        [`/api/v1/messages/${m.id}`]: () => json(200, { result: m }),
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

    const drawer = screen.getByRole("dialog", { name: "Message 1" });
    expect(within(drawer).getByText("hello")).toBeInTheDocument();
    expect(fields(drawer)).toMatchObject({
      Status: "pending",
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
    expect(within(items[1]).getByText("absent user")).toBeInTheDocument();
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

  it("loads more attempts on request", async () => {
    const m = message();
    const first = Array.from({ length: 100 }, (_, i) => attempt({ id: i + 1 }));
    const requests = serve(m, [first, [attempt({ id: 101 })]], 101);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("dialog");
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

  it("shows a binary message without attempts", async () => {
    const m = message({ text: undefined, encoding: "binary" });
    serve(m, [[]]);

    renderWithClient(<MessageDrawer message={m} onClose={() => {}} />);

    const drawer = screen.getByRole("dialog");
    expect(within(drawer).getByText("(binary)")).toBeInTheDocument();
    expect(
      await within(drawer).findByText("No delivery attempts yet."),
    ).toBeInTheDocument();
  });

  it("renders nothing when no message is selected", () => {
    renderWithClient(<MessageDrawer message={null} onClose={() => {}} />);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
