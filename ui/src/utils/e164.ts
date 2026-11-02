export const isE164 = (value: string): boolean =>
  /^\+[1-9]\d{0,14}$/.test(value);

export const isPartialNumber = (value: string): boolean =>
  /^\+?\d{1,15}$/.test(value);
