import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  apiFetch,
  setAccessToken,
  setAccessTokenRenewer,
  withQuery,
} from "@/queries/utils";

const stubFetch = (status: number, body: string, statusText = "") => {
  const fetchMock = vi.fn(
    async () => new Response(body, { status, statusText }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
};

afterEach(() => {
  vi.unstubAllGlobals();
  setAccessToken(null);
  setAccessTokenRenewer(null);
});

describe("apiFetch", () => {
  it("unwraps the result", async () => {
    stubFetch(200, JSON.stringify({ result: { id: 1 } }));

    await expect(apiFetch("/api/v1/messages/1")).resolves.toEqual({ id: 1 });
  });

  it("sends a JSON body", async () => {
    const fetchMock = stubFetch(201, JSON.stringify({ result: {} }));

    await apiFetch("/api/v1/messages", { method: "POST", body: { a: 1 } });

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: '{"a":1}',
    });
  });

  it("surfaces the server error message", async () => {
    stubFetch(400, JSON.stringify({ error: "text is required" }));

    const err = await apiFetch("/api/v1/messages").catch((e) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      kind: "http",
      status: 400,
      message: "text is required",
    });
  });

  it("falls back to the status text when the error body is not JSON", async () => {
    stubFetch(404, "404 page not found", "Not Found");

    await expect(apiFetch("/nope")).rejects.toMatchObject({
      kind: "http",
      status: 404,
      message: "Not Found",
    });
  });

  it("rejects a successful response that is not JSON", async () => {
    stubFetch(200, "<html></html>");

    await expect(apiFetch("/api/v1/diameter")).rejects.toMatchObject({
      kind: "http",
      status: 200,
    });
  });

  it("reports network failures", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    await expect(apiFetch("/api/v1/diameter")).rejects.toMatchObject({
      kind: "network",
      status: undefined,
    });
  });
});

describe("withQuery", () => {
  it("drops empty values", () => {
    expect(
      withQuery("/api/v1/messages", {
        page: 2,
        to: "",
        from: undefined,
        status: "pending",
      }),
    ).toBe("/api/v1/messages?page=2&status=pending");
  });

  it("returns the bare path without parameters", () => {
    expect(withQuery("/api/v1/messages", {})).toBe("/api/v1/messages");
  });

  it("encodes the plus sign of E.164 numbers", () => {
    expect(withQuery("/api/v1/messages", { to: "+15551230002" })).toBe(
      "/api/v1/messages?to=%2B15551230002",
    );
  });
});

describe("apiFetch authentication", () => {
  it("sends the access token once there is one", async () => {
    const fetchMock = stubFetch(200, JSON.stringify({ result: {} }));

    await apiFetch("/api/v1/status");
    setAccessToken("token");
    await apiFetch("/api/v1/messages");

    expect(fetchMock.mock.calls[0]).toEqual([
      "/api/v1/status",
      { method: "GET" },
    ]);
    expect(fetchMock.mock.calls[1]).toEqual([
      "/api/v1/messages",
      { method: "GET", headers: { Authorization: "Bearer token" } },
    ]);
  });

  it("renews a refused access token, and retries the request once", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: "Invalid token" }), {
          status: 401,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ result: { id: 1 } }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);
    setAccessToken("expired");
    setAccessTokenRenewer(async () => {
      setAccessToken("renewed");
      return true;
    });

    await expect(apiFetch("/api/v1/messages/1")).resolves.toEqual({ id: 1 });
    expect(fetchMock.mock.calls[1][1]).toMatchObject({
      headers: { Authorization: "Bearer renewed" },
    });
  });

  it("fails when the access token cannot be renewed", async () => {
    stubFetch(401, JSON.stringify({ error: "Invalid token" }));
    const renew = vi.fn(async () => false);
    setAccessTokenRenewer(renew);
    setAccessToken("expired");

    await expect(apiFetch("/api/v1/messages")).rejects.toMatchObject({
      kind: "auth",
      status: 401,
    });
    expect(renew).toHaveBeenCalledOnce();
  });

  it("does not renew anything on a failed login", async () => {
    stubFetch(
      401,
      JSON.stringify({ error: "The email or password is incorrect." }),
    );
    const renew = vi.fn(async () => true);
    setAccessTokenRenewer(renew);
    setAccessToken("token");

    await expect(
      apiFetch("/api/v1/auth/login", { method: "POST", anonymous: true }),
    ).rejects.toMatchObject({
      kind: "http",
      message: "The email or password is incorrect.",
    });
    expect(renew).not.toHaveBeenCalled();
  });

  it("reports a forbidden request", async () => {
    stubFetch(403, JSON.stringify({ error: "Forbidden" }));
    setAccessToken("token");

    await expect(apiFetch("/api/v1/users")).rejects.toMatchObject({
      kind: "forbidden",
      status: 403,
    });
  });

  it("resolves a response without content", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 204 })),
    );

    await expect(apiFetch("/api/v1/auth/logout")).resolves.toBeUndefined();
  });
});
