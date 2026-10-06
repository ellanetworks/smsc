import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type DeliverySettings, updateDelivery } from "@/queries/delivery";
import { formatDuration, parseDuration } from "@/utils/durations";

const DURATION_HINT = "e.g. 30s, 5m, 1h or 7d";

const items = (value: string) =>
  value
    .split(",")
    .map((item) => item.trim())
    .filter((item) => item !== "");

export default function EditDeliveryDialog({
  delivery,
  onClose,
}: {
  delivery: DeliverySettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [validity, setValidity] = useState(
    formatDuration(delivery.default_validity_seconds),
  );
  const [retries, setRetries] = useState(
    delivery.retry_intervals_seconds.map(formatDuration).join(", "),
  );

  const mutation = useMutation({
    mutationFn: updateDelivery,
    onSuccess: (updated) => {
      queryClient.setQueryData(["delivery"], updated);
      onClose();
    },
  });

  const validityError =
    parseDuration(validity) === undefined ? DURATION_HINT : undefined;
  const retriesError = items(retries).some(
    (item) => parseDuration(item) === undefined,
  )
    ? `Comma-separated, ${DURATION_HINT}`
    : undefined;

  return (
    <EditDialog
      title="Edit Delivery"
      valid={validityError === undefined && retriesError === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          default_validity_seconds: parseDuration(validity)!,
          retry_intervals_seconds: items(retries).map((item) =>
            parseDuration(item)!,
          ),
        })
      }
      onClose={onClose}
    >
      <TextField
        label="Default Validity"
        value={validity}
        onChange={(e) => setValidity(e.target.value)}
        error={validityError !== undefined}
        helperText={validityError}
        required
      />
      <TextField
        label="Retry Intervals"
        value={retries}
        onChange={(e) => setRetries(e.target.value)}
        error={retriesError !== undefined}
        helperText={retriesError ?? "Empty disables retries."}
        placeholder="1m, 5m, 15m, 1h"
      />
    </EditDialog>
  );
}
