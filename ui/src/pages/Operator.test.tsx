import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import Operator from "@/pages/Operator";
import type { OperatorSettings } from "@/queries/operator";
import { json, renderWithClient, stubApi } from "@/test/render";

const operator: OperatorSettings = {
  mcc: "001",
  mnc: "01",
  service_centre_address: "+15550000000",
  numbering: {
    country_code: "1",
    national_prefix: "",
    international_prefix: "",
  },
};

const serve = (put: (body: OperatorSettings) => Response) => {
  const bodies: OperatorSettings[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/operator": (_url, init) => {
          if (init?.method !== "PUT") return json(200, { result: operator });
          const body = JSON.parse(String(init.body)) as OperatorSettings;
          bodies.push(body);
          return put(body);
        },
      }),
    ),
  );
  return bodies;
};

const rows = () =>
  Array.from(
    screen
      .getByRole("table", { name: "Operator settings" })
      .querySelectorAll(":scope > tbody > tr"),
  ).map((row) => row.querySelector("td")?.nextElementSibling?.textContent);

const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Operator", () => {
  it("shows the operator settings", async () => {
    serve(() => json(500, {}));

    renderWithClient(<Operator />);

    await screen.findByText("+15550000000");
    expect(rows()).toEqual([
      "001 / 01",
      "+15550000000",
      "Country Code1National PrefixN/AInternational PrefixN/A",
    ]);
  });

  it("explains each setting on hover", async () => {
    serve(() => json(500, {}));

    renderWithClient(<Operator />);
    await screen.findByText("+15550000000");

    fireEvent.mouseOver(screen.getByText("Numbering"));
    expect(
      await screen.findByRole("tooltip", {
        name: "Used to turn the national numbers phones send into international ones.",
      }),
    ).toBeInTheDocument();
  });

  it("updates the service centre address", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await screen.findByText("+15550000000");
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Service Centre Address" }),
    );

    fill("Service Centre Address", "15550000000");
    expect(screen.getByText("E.164, e.g. +15550000000")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill("Service Centre Address", "+33600000000");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(bodies).toEqual([
      { ...operator, service_centre_address: "+33600000000" },
    ]);
    expect(rows()[1]).toBe("+33600000000");
  });

  it("updates the operator ID", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await screen.findByText("+15550000000");
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Operator ID (MCC/MNC)" }),
    );

    fill("MNC", "1");
    expect(screen.getByText("2 or 3 digits")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill("MCC", "208");
    fill("MNC", "10");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(bodies).toEqual([{ ...operator, mcc: "208", mnc: "10" }]);
    expect(rows()[0]).toBe("208 / 10");
  });

  it("updates the numbering plan", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await screen.findByText("+15550000000");
    fireEvent.click(screen.getByRole("button", { name: "Edit Numbering" }));

    fill("Country Code", "33");
    fill("National Prefix", "0");
    fill("Country Code", "033");
    expect(
      screen.getByText("1 to 3 digits, not starting with 0"),
    ).toBeInTheDocument();
    fill("Country Code", "33");
    fill("International Prefix", "+");
    expect(screen.getByText("Up to 4 digits")).toBeInTheDocument();
    fill("International Prefix", "0");
    expect(
      screen.getByText("Must differ from the national prefix"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();
    fill("International Prefix", "00");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({
      mcc: "001",
      mnc: "01",
      service_centre_address: "+15550000000",
      numbering: {
        country_code: "33",
        national_prefix: "0",
        international_prefix: "00",
      },
    });
  });

  it("shows the server error and keeps the form", async () => {
    serve(() =>
      json(400, { error: "numbering.country_code must be 1 to 3 digits" }),
    );

    renderWithClient(<Operator />);
    await screen.findByText("+15550000000");
    fireEvent.click(screen.getByRole("button", { name: "Edit Numbering" }));
    fill("Country Code", "33");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not update: numbering.country_code must be 1 to 3 digits",
    );
    expect(screen.getByRole("textbox", { name: "Country Code" })).toHaveValue(
      "33",
    );
  });
});
