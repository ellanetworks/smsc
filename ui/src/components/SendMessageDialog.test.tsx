import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import SendMessageDialog from "@/components/SendMessageDialog";
import { json, renderWithClient, stubApi } from "@/test/render";
import { message } from "@/test/fixtures";

const servePost = (response: () => Response) => {
  const bodies: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/messages": (_url, init) => {
          bodies.push(JSON.parse(String(init?.body)));
          return response();
        },
      }),
    ),
  );
  return bodies;
};

const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });

const sendButton = () => screen.getByRole("button", { name: "Send" });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SendMessageDialog", () => {
  it("sends the message and reports the created parts", async () => {
    const parts = [message({ id: 5 }), message({ id: 6 })];
    const bodies = servePost(() => json(201, { result: { items: parts } }));
    const onSent = vi.fn();

    renderWithClient(
      <SendMessageDialog open onClose={() => {}} onSent={onSent} />,
    );

    expect(sendButton()).toBeDisabled();

    fill("From", "+15551230001");
    fill("To", "+15551230002");
    fill("Text", "hello");

    expect(
      screen.getByText("5 characters · 1 part · GSM-7"),
    ).toBeInTheDocument();
    expect(sendButton()).toBeEnabled();

    fireEvent.click(sendButton());

    await waitFor(() => expect(onSent).toHaveBeenCalledWith(parts));
    expect(bodies).toEqual([
      { from: "+15551230001", to: "+15551230002", text: "hello" },
    ]);
    expect(screen.getByRole("textbox", { name: "Text" })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: "To" })).toHaveValue(
      "+15551230002",
    );
  });

  it("counts parts the way the server splits them", () => {
    renderWithClient(
      <SendMessageDialog open onClose={() => {}} onSent={() => {}} />,
    );

    fill("Text", "a".repeat(161));
    expect(
      screen.getByText("161 characters · 2 parts · GSM-7"),
    ).toBeInTheDocument();

    fill("Text", "日本");
    expect(
      screen.getByText(
        "2 characters · 1 part · UCS-2 (up to 70 characters per part)",
      ),
    ).toBeInTheDocument();
  });

  it("flags invalid numbers once the field is left", () => {
    renderWithClient(
      <SendMessageDialog open onClose={() => {}} onSent={() => {}} />,
    );

    const to = screen.getByRole("textbox", { name: "To" });
    fireEvent.change(to, { target: { value: "5551230002" } });
    expect(
      screen.queryByText("E.164, e.g. +15551230002"),
    ).not.toBeInTheDocument();

    fireEvent.blur(to);
    expect(screen.getByText("E.164, e.g. +15551230002")).toBeInTheDocument();

    fireEvent.change(to, { target: { value: "" } });
    expect(screen.getByText("Required")).toBeInTheDocument();
  });

  it("refuses text that needs more than 255 parts", () => {
    renderWithClient(
      <SendMessageDialog open onClose={() => {}} onSent={() => {}} />,
    );

    fill("From", "+15551230001");
    fill("To", "+15551230002");
    fill("Text", "a".repeat(255 * 153 + 1));

    expect(
      screen.getByText("Too long: the text needs more than 255 parts."),
    ).toBeInTheDocument();
    expect(sendButton()).toBeDisabled();
  });

  it("shows the server error and keeps the form", async () => {
    servePost(() => json(400, { error: "text is too long" }));
    const onSent = vi.fn();

    renderWithClient(
      <SendMessageDialog open onClose={() => {}} onSent={onSent} />,
    );

    fill("From", "+15551230001");
    fill("To", "+15551230002");
    fill("Text", "hello");
    fireEvent.click(sendButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not send the message: text is too long",
    );
    expect(screen.getByRole("textbox", { name: "Text" })).toHaveValue("hello");
    expect(onSent).not.toHaveBeenCalled();
  });
});
