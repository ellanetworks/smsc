export const isE164 = (value: string): boolean =>
  /^\+[1-9]\d{0,14}$/.test(value);

// isPartialNumber mirrors the API's to and from filters: all or part of an E.164 number, 1 to 15 digits after an
// optional '+'.
export const isPartialNumber = (value: string): boolean =>
  /^\+?\d{1,15}$/.test(value);
