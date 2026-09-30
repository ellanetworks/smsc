import type { Attempt, Message } from "@/queries/messages";

export const message = (overrides: Partial<Message> = {}): Message => ({
  id: 1,
  from: "+15551230001",
  to: "+15551230002",
  text: "hello",
  encoding: "gsm7",
  status: "delivered",
  created_at: "2026-09-30T12:00:00.000Z",
  updated_at: "2026-09-30T12:00:01.000Z",
  expires_at: "2026-10-07T12:00:00.000Z",
  ...overrides,
});

export const attempt = (overrides: Partial<Attempt> = {}): Attempt => ({
  id: 1,
  started_at: "2026-09-30T12:00:00.000Z",
  completed_at: "2026-09-30T12:00:00.250Z",
  step: "routing",
  outcome: "success",
  result_code: 2001,
  ...overrides,
});

export const page = <T>(
  items: T[],
  extra: { page?: number; per_page?: number; total_count?: number } = {},
) => ({
  items,
  page: extra.page ?? 1,
  per_page: extra.per_page ?? 25,
  total_count: extra.total_count ?? items.length,
});
