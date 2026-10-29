import { Chip, type ChipProps } from "@mui/material";
import type { MessageStatus } from "@/queries/messages";
import { capitalized } from "@/utils/labels";

const statusColors: Record<MessageStatus, ChipProps["color"]> = {
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
      color={statusColors[status]}
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

// outcomeLabels spells the outcomes whose acronyms the default label, the outcome with spaces, would hide.
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
