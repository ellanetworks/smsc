import { isE164 } from "@/utils/e164";

export const isOriginator = (value: string): boolean =>
  value.startsWith("+") ? isE164(value) : isOriginatorName(value);

export const isOriginatorName = (value: string): boolean =>
  value.length <= 11 &&
  /^[A-Za-z0-9]+( [A-Za-z0-9]+)*$/.test(value) &&
  !/^\d+$/.test(value);
