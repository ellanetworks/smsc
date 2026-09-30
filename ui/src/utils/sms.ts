export type TextEncoding = "gsm7" | "ucs2";

export interface Segmentation {
  encoding: TextEncoding;
  partSizes: number[];
  tooLong: boolean;
}

const MAX_SEPTETS = 160;
const MAX_USER_DATA_OCTETS = 140;
const CONCAT_HEADER_OCTETS = 6;
const CONCAT_HEADER_SEPTETS = Math.floor((CONCAT_HEADER_OCTETS * 8 + 6) / 7);
const MAX_PARTS = 255;

const GSM7_BASIC = new Set(
  "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
    "¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà",
);

const GSM7_EXTENSION = new Set("\f^{}\\[~]|€");

const gsm7Sizes = (text: string): number[] | null => {
  const sizes: number[] = [];
  for (const c of text) {
    if (GSM7_BASIC.has(c)) sizes.push(1);
    else if (GSM7_EXTENSION.has(c)) sizes.push(2);
    else return null;
  }
  return sizes;
};

const ucs2Sizes = (text: string): number[] => {
  const sizes: number[] = [];
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    const pair = code >= 0xd800 && code <= 0xdfff && i + 1 < text.length;
    sizes.push(pair ? 2 : 1);
    if (pair) i++;
  }
  return sizes;
};

const split = (sizes: number[], limit: number): number[] => {
  const chunks: number[] = [];
  let current = 0;
  for (const size of sizes) {
    if (current + size > limit) {
      chunks.push(current);
      current = 0;
    }
    current += size;
  }
  chunks.push(current);
  return chunks;
};

const segment = (
  encoding: TextEncoding,
  sizes: number[],
  single: number,
  concatenated: number,
): Segmentation => {
  const total = sizes.reduce((sum, s) => sum + s, 0);
  if (total <= single) {
    return { encoding, partSizes: [total], tooLong: false };
  }

  const partSizes = split(sizes, concatenated);
  return { encoding, partSizes, tooLong: partSizes.length > MAX_PARTS };
};

export const segmentText = (text: string): Segmentation => {
  const gsm7 = gsm7Sizes(text);
  if (gsm7) {
    return segment(
      "gsm7",
      gsm7,
      MAX_SEPTETS,
      MAX_SEPTETS - CONCAT_HEADER_SEPTETS,
    );
  }

  return segment(
    "ucs2",
    ucs2Sizes(text),
    MAX_USER_DATA_OCTETS / 2,
    (MAX_USER_DATA_OCTETS - CONCAT_HEADER_OCTETS) / 2,
  );
};
