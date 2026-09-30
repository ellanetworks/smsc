import type {
  AbsentUserDiagnostics,
  AttemptStep,
  Message,
  MessageEncoding,
  NodeType,
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
  smsf_3gpp: "SMSF (3GPP)",
  smsf_non_3gpp: "SMSF (non-3GPP)",
};

export const diagnosticsLabels: Record<keyof AbsentUserDiagnostics, string> = {
  mme: "MME",
  msc: "MSC",
  sgsn: "SGSN",
  smsf_3gpp: "SMSF (3GPP)",
  smsf_non_3gpp: "SMSF (non-3GPP)",
};

export const partLabel = (m: Message): string =>
  m.concatenation ? `${m.concatenation.part} of ${m.concatenation.total}` : "—";

export const messageText = (m: Message): string => m.text ?? "(binary)";
