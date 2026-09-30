import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import App from "@/App";
import { json, renderWithClient, stubApi } from "@/test/render";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders the product name and the Diameter status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/diameter": () =>
            json(200, {
              result: {
                host: "smsc.example.org",
                realm: "example.org",
                hss_available: false,
                peers: [],
              },
            }),
        }),
      ),
    );

    renderWithClient(<App />);

    expect(
      screen.getByRole("heading", { name: "Ella SMSC" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByRole("region", { name: "Diameter" }),
    ).toBeInTheDocument();
  });
});
