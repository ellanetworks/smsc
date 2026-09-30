import { describe, expect, it } from "vitest";
import { segmentText } from "@/utils/sms";

describe("segmentText", () => {
  it.each([
    ["160 septets fit one message", "a".repeat(160), "gsm7", [160]],
    ["161 septets need two", "a".repeat(161), "gsm7", [153, 8]],
    ["extension counts two septets", "a".repeat(159) + "€", "gsm7", [153, 8]],
    [
      "escape pair is not split",
      "a".repeat(152) + "€bbbbbbb",
      "gsm7",
      [152, 9],
    ],
    ["70 UCS-2 characters fit one", "日".repeat(70), "ucs2", [70]],
    ["71 UCS-2 characters need two", "日".repeat(71), "ucs2", [67, 4]],
    [
      "surrogate pair is not split",
      "日".repeat(66) + "😀" + "日".repeat(5),
      "ucs2",
      [66, 7],
    ],
    [
      "one non-GSM character forces UCS-2",
      "a".repeat(100) + "日",
      "ucs2",
      [67, 34],
    ],
    ["empty text is one empty part", "", "gsm7", [0]],
  ])("%s", (_name, text, encoding, partSizes) => {
    expect(segmentText(text)).toEqual({ encoding, partSizes, tooLong: false });
  });

  it("flags text that needs more than 255 parts", () => {
    expect(segmentText("a".repeat(255 * 153)).tooLong).toBe(false);
    expect(segmentText("a".repeat(255 * 153 + 1)).tooLong).toBe(true);
  });

  it("does not treat the escape character as GSM-7", () => {
    expect(segmentText("\x1b").encoding).toBe("ucs2");
  });
});
