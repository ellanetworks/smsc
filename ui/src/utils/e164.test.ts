import { describe, expect, it } from "vitest";
import { isE164 } from "@/utils/e164";

describe("isE164", () => {
  it.each(["+1", "+15551230002", "+123456789012345"])("accepts %s", (v) => {
    expect(isE164(v)).toBe(true);
  });

  it.each([
    "",
    "+",
    "15551230002",
    "+05551230002",
    "+1234567890123456",
    "+1555 123",
  ])("rejects %j", (v) => {
    expect(isE164(v)).toBe(false);
  });
});
