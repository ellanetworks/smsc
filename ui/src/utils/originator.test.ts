import { describe, expect, it } from "vitest";
import { isOriginator } from "@/utils/originator";

describe("isOriginator", () => {
  it.each([
    "+1",
    "+15551230001",
    "+123456789012345",
    "EllaSMSC",
    "ACME Ltd",
    "A",
    "Ella 2024",
    "ElevenChars",
  ])("accepts %j", (v) => {
    expect(isOriginator(v)).toBe(true);
  });

  it.each([
    "",
    "+",
    "15551230001",
    "+05551230002",
    "+1234567890123456",
    "+1555 123",
    "EllaNetworks",
    "Ella_Net",
    "Café",
    "+EllaSMSC",
    "   ",
    " Ella",
    "Ella ",
    "ACME  Ltd",
  ])("rejects %j", (v) => {
    expect(isOriginator(v)).toBe(false);
  });
});
