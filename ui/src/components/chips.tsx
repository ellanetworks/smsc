import { Chip, type ChipProps } from "@mui/material";
import type { DiameterPeerState } from "@/queries/diameter";
import type { MessageStatus } from "@/queries/messages";
import { capitalized } from "@/utils/labels";

export const messageStatusColors: Record<MessageStatus, ChipProps["color"]> = {
  pending: "info",
  delivered: "success",
  delegated: "secondary",
  failed: "error",
  expired: "warning",
  replaced: "default",
};

const messageStatusLabels: Partial<Record<MessageStatus, string>> = {
  delegated: "At application",
};

export function MessageStatusChip({ status }: { status: MessageStatus }) {
  return (
    <Chip
      label={messageStatusLabels[status] ?? capitalized(status)}
      color={messageStatusColors[status]}
      size="small"
    />
  );
}

const outcomeColor = (outcome: string): ChipProps["color"] => {
  switch (outcome) {
    case "success":
      return "success";
    case "timeout":
    case "unreachable":
    case "no_hss":
    case "absent_user":
    case "user_busy_for_mt_sms":
      return "warning";
    case "delegated_to_esme":
    case "esme_acknowledged":
      return "info";
    default:
      return "error";
  }
};

const outcomeLabels: Record<string, string> = {
  no_hss: "No HSS connected",
  diameter_error: "Diameter error",
  user_busy_for_mt_sms: "User busy for MT SMS",
  sm_delivery_failure: "SM delivery failure",
  mwd_list_full: "MWD list full",
  delegated_to_esme: "Queued for the ESME",
  esme_acknowledged: "Accepted by the ESME",
  esme_rejected: "Rejected by the ESME",
  esme_expired: "Validity period expired",
  esme_deleted: "ESME deleted",
  esme_undecodable: "Not convertible to SMPP",
};

export function OutcomeChip({ outcome }: { outcome: string }) {
  return (
    <Chip
      label={
        outcomeLabels[outcome] ?? capitalized(outcome.replaceAll("_", " "))
      }
      color={outcomeColor(outcome)}
      size="small"
    />
  );
}

export const diameterStateColor = (
  state: DiameterPeerState,
): ChipProps["color"] => {
  switch (state) {
    case "open":
      return "success";
    case "down":
      return "error";
    default:
      return "warning";
  }
};

export function DiameterStateChip({ state }: { state: DiameterPeerState }) {
  return (
    <Chip
      label={capitalized(state)}
      color={diameterStateColor(state)}
      size="small"
    />
  );
}
