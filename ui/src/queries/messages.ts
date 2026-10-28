import {
  apiFetch,
  withQuery,
  type Page,
  type PageParams,
} from "@/queries/utils";

export type MessageStatus =
  "pending" | "delivered" | "failed" | "expired" | "replaced";

// A status report is delivered as a message is, but is never replaced.
export type StatusReportDelivery = Exclude<MessageStatus, "replaced">;

export interface StatusReport {
  id: number;
  tp_status: number;
  tp_status_name: string;
  status: StatusReportDelivery;
  created_at: string;
  updated_at: string;
  expires_at: string;
  next_attempt_at?: string;
}

export interface StatusReports {
  requested: boolean;
  reports: StatusReport[];
}

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
  encoding: MessageEncoding;
  concatenation?: Concatenation;
  status: MessageStatus;
  created_at: string;
  updated_at: string;
  expires_at: string;
  next_attempt_at?: string;
  status_report: StatusReports;
  submitted_by?: string;
  // system_id is the ESME that submitted the message over SMPP.
  system_id?: string;
}

// Octets are in hexadecimal, two digits each.
export type Octets = string;

// BinaryContent is the TPDU parameters of a binary message (TS 23.040 clause 9.2.3.9).
export interface BinaryContent {
  protocol_identifier: number;
  data_coding_scheme: number;
  // user_data_header is the TP-UDH, its length octet included. It is absent when there is none.
  user_data_header?: Octets;
  user_data: Octets;
}

// DeliverReport is an SMS-DELIVER-REPORT that the recipient answered a delivery attempt with (TS 23.040 clause
// 9.2.2.1a). Only tpdu is present when it does not decode.
export interface DeliverReport {
  attempt_id: number;
  tpdu: Octets;
  protocol_identifier?: number;
  data_coding_scheme?: number;
  user_data_header?: Octets;
  user_data?: Octets;
}

// MessageContent is the content of a message, which the other responses leave out. Reading it is audited.
export interface MessageContent {
  // text is absent for a binary message.
  text?: string;
  binary?: BinaryContent;
  deliver_reports?: DeliverReport[];
}

export type AttemptStep = "routing" | "delivery";

export type NodeType =
  "mme" | "sgsn" | "ip_sm_gw" | "smsf_3gpp" | "smsf_non_3gpp";

export interface AbsentUserDiagnostics {
  mme?: string;
  msc?: string;
  sgsn?: string;
  ip_sm_gw?: string;
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
  // start and end bound when the messages were created: start included, end excluded.
  start?: string;
  end?: string;
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
      start: params.start,
      end: params.end,
    }),
  );

export const getMessage = (id: number): Promise<Message> =>
  apiFetch<Message>(`/api/v1/messages/${id}`);

export const getMessageContent = (id: number): Promise<MessageContent> =>
  apiFetch<MessageContent>(`/api/v1/messages/${id}/content`);

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
