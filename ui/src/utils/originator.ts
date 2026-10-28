import { isE164 } from "@/utils/e164";

// isOriginator mirrors the API's from: an E.164 number when it starts with '+', and otherwise a name of 1 to 11 ASCII
// letters and digits, with single spaces between words, not all digits, since an all-digit value is a number.
export const isOriginator = (value: string): boolean =>
  value.startsWith("+")
    ? isE164(value)
    : value.length <= 11 &&
      /^[A-Za-z0-9]+( [A-Za-z0-9]+)*$/.test(value) &&
      !/^\d+$/.test(value);
