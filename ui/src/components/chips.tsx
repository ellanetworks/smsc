import { Chip, type ChipProps } from "@mui/material";
import type { MessageStatus } from "@/queries/messages";

const statusColors: Record<MessageStatus, ChipProps["color"]> = {
  pending: "info",
  delivered: "success",
  failed: "error",
  expired: "warning",
};

export function MessageStatusChip({ status }: { status: MessageStatus }) {
  return <Chip label={status} color={statusColors[status]} size="small" />;
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

export function OutcomeChip({ outcome }: { outcome: string }) {
  return (
    <Chip
      label={outcome.replaceAll("_", " ")}
      color={outcomeColor(outcome)}
      size="small"
    />
  );
}
