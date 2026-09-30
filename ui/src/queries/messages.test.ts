import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createMessage,
  listMessageAttempts,
  listMessages,
} from "@/queries/messages";

const stubFetch = (result: unknown) => {
  const fetchMock = vi.fn(
    async (_url: string, _init?: RequestInit) =>
      new Response(JSON.stringify({ result }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listMessages", () => {
  it("maps filters and pagination to query parameters", async () => {
    const fetchMock = stubFetch({
      items: [],
      page: 3,
      per_page: 50,
      total_count: 0,
    });

    await listMessages({
      page: 3,
      perPage: 50,
      to: "+15551230002",
      status: "failed",
    });

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/v1/messages?page=3&per_page=50&to=%2B15551230002&status=failed",
    );
  });
});

describe("listMessageAttempts", () => {
  it("requests the attempts of a message", async () => {
    const fetchMock = stubFetch({
      items: [],
      page: 2,
      per_page: 100,
      total_count: 0,
    });

    await listMessageAttempts(7, { page: 2, perPage: 100 });

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/v1/messages/7/attempts?page=2&per_page=100",
    );
  });
});

describe("createMessage", () => {
  it("posts the message and returns the created parts", async () => {
    const parts = [{ id: 1 }, { id: 2 }];
    const fetchMock = stubFetch({ items: parts });

    const created = await createMessage({
      from: "+15551230001",
      to: "+15551230002",
      text: "hello",
    });

    expect(created).toEqual(parts);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({
        from: "+15551230001",
        to: "+15551230002",
        text: "hello",
      }),
    });
  });
});
