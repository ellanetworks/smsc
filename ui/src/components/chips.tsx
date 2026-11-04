import { Chip, type ChipProps } from "@mui/material";
import type { DiameterPeerState } from "@/queries/diameter";
import type { MessageStatus } from "@/queries/messages";
import { capitalized } from "@/utils/labels";

export const messageStatusColors: Record<MessageStatus, ChipProps["color"]> = {
  pending: "info",
  delivered: "success",
  failed: "error",
  expired: "warning",
  replaced: "default",
};

export function MessageStatusChip({ status }: { status: MessageStatus }) {
  return (
    <Chip
      label={capitalized(status)}
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
