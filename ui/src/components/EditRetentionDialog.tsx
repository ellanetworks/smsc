import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { type RetentionSettings, updateRetention } from "@/queries/retention";

export default function EditRetentionDialog({
  retention,
  onClose,
}: {
  retention: RetentionSettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [days, setDays] = useState(String(retention.days));

  const mutation = useMutation({
    mutationFn: updateRetention,
    onSuccess: (updated) => {
      queryClient.setQueryData(["retention"], updated);
      showSnackbar("Retention updated successfully.", "success");
      onClose();
    },
  });

  const valid = /^[1-9][0-9]{0,5}$/.test(days);

  return (
    <EditDialog
      title="Edit Retention"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ days: Number(days) })}
      onClose={onClose}
    >
      <TextField
        label="Days"
        type="number"
        value={days}
        onChange={(e) => setDays(e.target.value)}
        error={!valid}
        helperText={
          valid
            ? "Finished messages older than this are deleted, with their status reports and delivery attempts."
            : "A whole number of days, at least 1."
        }
        slotProps={{ htmlInput: { min: 1, step: 1 } }}
        required
      />
    </EditDialog>
  );
}
