import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type OperatorSettings, updateOperator } from "@/queries/operator";
import { isE164 } from "@/utils/e164";

export default function EditServiceCentreDialog({
  operator,
  onClose,
}: {
  operator: OperatorSettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [address, setAddress] = useState(operator.service_centre_address);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      onClose();
    },
  });

  const error = isE164(address) ? undefined : "E.164, e.g. +15550000000";

  return (
    <EditDialog
      title="Edit Service Centre Address"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({ ...operator, service_centre_address: address })
      }
      onClose={onClose}
    >
      <TextField
        label="Service Centre Address"
        value={address}
        onChange={(e) => setAddress(e.target.value.trim())}
        error={error !== undefined}
        helperText={error}
        placeholder="+15550000000"
        required
      />
    </EditDialog>
  );
}
