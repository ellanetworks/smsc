import {
  apiFetch,
  withQuery,
  type Page,
  type PageParams,
} from "@/queries/utils";

export type MessageStatus = "pending" | "delivered" | "failed" | "expired";

export type MessageEncoding = "gsm7" | "ucs2" | "binary";

export interface Concatenation {
  reference: number;
  part: number;
  total: number;
}

export interface Message {
  id: number;
  from: string;
  to: string;
  text?: string;
  encoding: MessageEncoding;
  concatenation?: Concatenation;
  status: MessageStatus;
  created_at: string;
  updated_at: string;
  expires_at: string;
  next_attempt_at?: string;
}

export type AttemptStep = "routing" | "delivery";

export type NodeType = "mme" | "sgsn" | "smsf_3gpp" | "smsf_non_3gpp";

export interface AbsentUserDiagnostics {
  mme?: string;
  msc?: string;
  sgsn?: string;
  smsf_3gpp?: string;
  smsf_non_3gpp?: string;
}

export interface Attempt {
  id: number;
  started_at: string;
  completed_at: string;
  step: AttemptStep;
  node?: string;
  node_type?: NodeType;
  outcome: string;
  result_code?: number;
  vendor_id?: number;
  failure_cause?: string;
  tp_failure_cause?: string;
  absent_user_diagnostics?: AbsentUserDiagnostics;
}

export interface CreateMessageParams {
  from: string;
  to: string;
  text: string;
}

export interface ListMessagesParams extends PageParams {
  to?: string;
  from?: string;
  status?: MessageStatus;
}

export const createMessage = async (
  params: CreateMessageParams,
): Promise<Message[]> => {
  const { items } = await apiFetch<{ items: Message[] }>("/api/v1/messages", {
    method: "POST",
    body: params,
  });
  return items;
};

export const listMessages = (
  params: ListMessagesParams = {},
): Promise<Page<Message>> =>
  apiFetch<Page<Message>>(
    withQuery("/api/v1/messages", {
      page: params.page,
      per_page: params.perPage,
      to: params.to,
      from: params.from,
      status: params.status,
    }),
  );

export const getMessage = (id: number): Promise<Message> =>
  apiFetch<Message>(`/api/v1/messages/${id}`);

export const listMessageAttempts = (
  id: number,
  params: PageParams = {},
): Promise<Page<Attempt>> =>
  apiFetch<Page<Attempt>>(
    withQuery(`/api/v1/messages/${id}/attempts`, {
      page: params.page,
      per_page: params.perPage,
    }),
  );
