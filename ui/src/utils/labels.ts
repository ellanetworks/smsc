import type {
  AbsentUserDiagnostics,
  AttemptStep,
  Message,
  MessageEncoding,
  NodeType,
  StatusReport,
} from "@/queries/messages";

export const encodingLabels: Record<MessageEncoding, string> = {
  gsm7: "GSM-7",
  ucs2: "UCS-2",
  binary: "Binary",
};

export const stepLabels: Record<AttemptStep, string> = {
  routing: "Routing",
  delivery: "Delivery",
};

export const nodeTypeLabels: Record<NodeType, string> = {
  mme: "MME",
  sgsn: "SGSN",
  ip_sm_gw: "IP-SM-GW",
  smsf_3gpp: "SMSF (3GPP)",
  smsf_non_3gpp: "SMSF (non-3GPP)",
};

export const diagnosticsLabels: Record<keyof AbsentUserDiagnostics, string> = {
  mme: "MME",
  msc: "MSC",
  sgsn: "SGSN",
  ip_sm_gw: "IP-SM-GW",
  smsf_3gpp: "SMSF (3GPP)",
  smsf_non_3gpp: "SMSF (non-3GPP)",
};

export const partLabel = (m: Message): string =>
  m.concatenation ? `${m.concatenation.part} of ${m.concatenation.total}` : "—";

// hex shows an octet as two hexadecimal digits, such as "0x7f".
export const hex = (n: number): string =>
  `0x${n.toString(16).padStart(2, "0")}`;

// tpStatusLabel names a TP-Status (TS 23.040 clause 9.2.3.15) with its value, such as "no response (0x22)".
export const tpStatusLabel = (r: StatusReport): string =>
  `${r.tp_status_name.replaceAll("_", " ")} (${hex(r.tp_status)})`;
