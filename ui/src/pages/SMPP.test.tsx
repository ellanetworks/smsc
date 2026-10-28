import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import SMPP from "@/pages/SMPP";
import { authFor } from "@/contexts/AuthContext";
import { RoleID } from "@/queries/users";
import { json, renderWithClient, stubApi } from "@/test/render";

const ota = {
  system_id: "ota1",
  user: "ota@example.com",
  allowed_networks: ["10.1.0.0/24", "2001:db8::/32"],
  created_at: "2026-09-30T12:00:00.000Z",
  sessions: [
    {
      bind_type: "transceiver",
      remote_address: "10.1.0.5:40000",
      bound_at: "2026-09-30T12:05:18.843Z",
    },
    {
      bind_type: "receiver",
      remote_address: "10.1.0.6:40001",
      bound_at: "2026-09-30T12:05:18.843Z",
    },
  ],
};

const idle = {
  system_id: "bulk sender",
  user: "bulk@example.com",
  allowed_networks: [],
  created_at: "2026-09-30T12:00:00.000Z",
  sessions: [],
};

type Call = { method: string; path: string; body?: unknown };

const serve = (
  esmes: (url: URL, init?: RequestInit) => Response = () =>
    json(200, { result: { items: [ota, idle] } }),
) => {
  const calls: Call[] = [];
  const recorded =
    (route: (url: URL, init?: RequestInit) => Response) =>
    (url: URL, init?: RequestInit) => {
      if (init?.method && init.method !== "GET") {
        calls.push({
          method: init.method,
          path: url.pathname,
          ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
        });
      }
      return route(url, init);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/esmes": recorded(esmes),
        "/api/v1/esmes/ota1": recorded((_url, init) =>
          init?.method === "DELETE"
            ? json(200, { result: { message: "ESME deleted successfully" } })
            : json(200, { result: ota }),
        ),
        "/api/v1/esmes/bulk%20sender": recorded(() =>
          json(200, { result: idle }),
        ),
        "/api/v1/users": () =>
          json(200, {
            result: {
              items: [
                { email: "admin@example.com", role_id: RoleID.Admin },
                { email: "ota@example.com", role_id: RoleID.Submitter },
                { email: "nobody@example.com", role_id: RoleID.Submitter },
                { email: "viewer@example.com", role_id: RoleID.ReadOnly },
              ],
              page: 1,
              per_page: 100,
              total_count: 4,
            },
          }),
      }),
    ),
  );

  return calls;
};

const grid = () => screen.getByRole("grid", { name: "ESMEs" });

const rowCells = (text: string) =>
  within(
    within(grid()).getByText(text).closest(".MuiDataGrid-row") as HTMLElement,
  )
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const openAdd = async () => {
  fireEvent.click(await screen.findByRole("button", { name: "Add ESME" }));
  return screen.findByRole("dialog");
};

// pickUser chooses a user in the Add dialog's User list.
const pickUser = async (dialog: HTMLElement, email: string) => {
  fireEvent.mouseDown(within(dialog).getByLabelText(/User/));
  fireEvent.click(
    await within(screen.getByRole("listbox")).findByRole("option", {
      name: new RegExp(`^${email}`),
    }),
  );
};

const fill = (dialog: HTMLElement, label: string | RegExp, value: string) =>
  fireEvent.change(within(dialog).getByLabelText(label), {
    target: { value },
  });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SMPP", () => {
  it("lists the ESMEs and their sessions", async () => {
    serve();

    renderWithClient(<SMPP />);

    expect(
      await screen.findByRole("heading", { level: 1, name: "SMPP (2)" }),
    ).toBeInTheDocument();
    await within(grid()).findByText("ota1");
    expect(
      within(grid())
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual(["System ID", "User", "Allowed Networks", "Sessions", "Actions"]);
    expect(rowCells("ota1")).toEqual([
      "ota1",
      "ota@example.com",
      "10.1.0.0/242001:db8::/32",
      "2 of 4 boundTransceiver from 10.1.0.5:40000Receiver from 10.1.0.6:40001",
      "",
    ]);
    expect(rowCells("bulk sender")).toEqual([
      "bulk sender",
      "bulk@example.com",
      "None",
      "None",
      "",
    ]);
  });

  it("shows an empty table without ESMEs", async () => {
    serve(() => json(200, { result: { items: [] } }));

    renderWithClient(<SMPP />);

    expect(await screen.findByText("No ESMEs.")).toBeInTheDocument();
  });

  it("adds an ESME with a password", async () => {
    const calls = serve((_url, init) =>
      init?.method === "POST"
        ? json(201, { result: { ...idle, system_id: "ota2" } })
        : json(200, { result: { items: [] } }),
    );

    renderWithClient(<SMPP />);

    const dialog = await openAdd();
    fill(dialog, /System ID/, "ota2");
    await pickUser(dialog, "ota@example.com");
    fill(dialog, /Password/, "secret1");
    fill(dialog, /Allowed networks/, "10.1.0.0/24, 2001:db8::/32");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/esmes",
          body: {
            system_id: "ota2",
            user: "ota@example.com",
            password: "secret1",
            allowed_networks: ["10.1.0.0/24", "2001:db8::/32"],
          },
        },
      ]),
    );
    expect(await screen.findByText("ESME added successfully.")).toBeVisible();
    expect(
      screen.queryByRole("dialog", { name: "ESME Password" }),
    ).not.toBeInTheDocument();
  });

  it("shows a generated password once", async () => {
    const calls = serve((_url, init) =>
      init?.method === "POST"
        ? json(201, {
            result: { ...idle, system_id: "ota2", password: "Ab3dE6gH" },
          })
        : json(200, { result: { items: [] } }),
    );

    renderWithClient(<SMPP />);

    const dialog = await openAdd();
    fill(dialog, /System ID/, "ota2");
    await pickUser(dialog, "ota@example.com");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    const shown = await screen.findByRole("dialog", { name: "ESME Password" });
    expect(within(shown).getByTestId("new-esme-password")).toHaveTextContent(
      "Ab3dE6gH",
    );
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/api/v1/esmes",
        body: {
          system_id: "ota2",
          user: "ota@example.com",
          allowed_networks: [],
        },
      },
    ]);

    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    fireEvent.click(
      within(shown).getByRole("button", { name: "Copy password" }),
    );
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("Ab3dE6gH"));

    fireEvent.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(screen.queryByText("Ab3dE6gH")).not.toBeInTheDocument(),
    );

    // Opening the dialog again does not show it.
    const again = await openAdd();
    expect(within(again).queryByText("Ab3dE6gH")).not.toBeInTheDocument();
    expect(screen.queryByText("Ab3dE6gH")).not.toBeInTheDocument();
  });

  it("validates what it sends", async () => {
    serve(() => json(200, { result: { items: [] } }));

    renderWithClient(<SMPP />);

    const dialog = await openAdd();
    const add = within(dialog).getByRole("button", { name: "Add" });
    expect(add).toBeDisabled();

    fill(dialog, /System ID/, "a-system-id-too-long");
    expect(
      within(dialog).getByText("1 to 15 printable ASCII characters."),
    ).toBeVisible();
    fill(dialog, /System ID/, "ota2");

    expect(add).toBeDisabled();
    await pickUser(dialog, "ota@example.com");
    expect(add).toBeEnabled();

    fill(dialog, /Password/, "toolongpw");
    expect(
      within(dialog).getByText("1 to 8 printable ASCII characters."),
    ).toBeVisible();
    expect(add).toBeDisabled();
    fill(dialog, /Password/, "pässwd");
    expect(add).toBeDisabled();
    fill(dialog, /Password/, "");
    expect(add).toBeEnabled();

    fill(dialog, /Allowed networks/, "10.1.0.0/24, 10.1.0.5");
    expect(
      within(dialog).getByText(
        "10.1.0.5 is not a network prefix, such as 10.1.0.0/24.",
      ),
    ).toBeVisible();
    expect(add).toBeDisabled();
    fill(dialog, /Allowed networks/, "10.1.0.0/33");
    expect(add).toBeDisabled();
    fill(dialog, /Allowed networks/, "2001:db8:::/32");
    expect(add).toBeDisabled();
    fill(dialog, /Allowed networks/, "::/0, 0.0.0.0/0, ::ffff:10.0.0.0/104");
    expect(add).toBeEnabled();
  });

  it("lists the users, a Read Only one disabled", async () => {
    serve(() => json(200, { result: { items: [] } }));
    renderWithClient(<SMPP />);

    const dialog = await openAdd();
    fireEvent.mouseDown(within(dialog).getByLabelText(/User/));
    const options = await within(screen.getByRole("listbox")).findAllByRole(
      "option",
    );
    expect(options.map((o) => o.textContent)).toEqual([
      "admin@example.com · Admin",
      "ota@example.com · Submitter",
      "nobody@example.com · Submitter",
      "viewer@example.com · Read Only",
    ]);
    expect(options[3]).toHaveAttribute("aria-disabled", "true");
  });

  it("shows the server's reason when it refuses an ESME", async () => {
    serve((_url, init) =>
      init?.method === "POST"
        ? json(400, { error: "user nobody@example.com does not exist" })
        : json(200, { result: { items: [] } }),
    );

    renderWithClient(<SMPP />);

    const dialog = await openAdd();
    fill(dialog, /System ID/, "ota2");
    await pickUser(dialog, "nobody@example.com");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(
      await within(dialog).findByText(
        "Could not add the ESME: user nobody@example.com does not exist",
      ),
    ).toBeVisible();
  });

  it("edits an ESME, keeping its password when left blank", async () => {
    const calls = serve();

    renderWithClient(<SMPP />);

    fireEvent.click(await screen.findByRole("button", { name: "Edit ota1" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByLabelText(/System ID/)).toBeDisabled();
    // The owner cannot change: its select is disabled.
    expect(within(dialog).getByLabelText(/User/)).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(within(dialog).getByLabelText(/Password/)).toHaveValue("");
    expect(within(dialog).getByLabelText(/Allowed networks/)).toHaveValue(
      "10.1.0.0/24, 2001:db8::/32",
    );

    fill(dialog, /Allowed networks/, "10.1.0.0/24");
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/esmes/ota1",
          body: {
            allowed_networks: ["10.1.0.0/24"],
          },
        },
      ]),
    );
    expect(await screen.findByText("ESME updated successfully.")).toBeVisible();
  });

  it("changes the password of an ESME whose system ID is escaped", async () => {
    const calls = serve();

    renderWithClient(<SMPP />);

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit bulk sender" }),
    );
    const dialog = await screen.findByRole("dialog");
    fill(dialog, /Password/, "newpass");
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/esmes/bulk%20sender",
          body: {
            password: "newpass",
            allowed_networks: [],
          },
        },
      ]),
    );
  });

  it("deletes an ESME once confirmed", async () => {
    const calls = serve();

    renderWithClient(<SMPP />);

    fireEvent.click(await screen.findByRole("button", { name: "Delete ota1" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Delete ota1?");

    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: "/api/v1/esmes/ota1" }]),
    );
    expect(await screen.findByText("ESME deleted successfully.")).toBeVisible();
  });

  it("lets a Read Only user see the ESMEs, not change them", async () => {
    serve();

    renderWithClient(<SMPP />, authFor("ro@example.com", RoleID.ReadOnly));

    await within(await screen.findByRole("grid", { name: "ESMEs" })).findByText(
      "ota1",
    );
    expect(
      screen.queryByRole("button", { name: "Add ESME" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit ota1" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Delete ota1" }),
    ).not.toBeInTheDocument();
    expect(
      within(grid())
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).not.toContain("Actions");
  });

  it("reports a failure to load", async () => {
    serve(() => json(500, { error: "Failed to load" }));

    renderWithClient(<SMPP />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Failed to load ESMEs");
  });
});
