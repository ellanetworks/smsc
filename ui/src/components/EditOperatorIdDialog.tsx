import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type OperatorSettings, updateOperator } from "@/queries/operator";

export default function EditOperatorIdDialog({
  operator,
  onClose,
}: {
  operator: OperatorSettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [mcc, setMcc] = useState(operator.mcc);
  const [mnc, setMnc] = useState(operator.mnc);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      void queryClient.invalidateQueries({ queryKey: ["diameter"] });
      onClose();
    },
  });

  const mccError = /^\d{3}$/.test(mcc) ? undefined : "3 digits";
  const mncError = /^\d{2,3}$/.test(mnc) ? undefined : "2 or 3 digits";

  return (
    <EditDialog
      title="Edit Operator ID"
      valid={mccError === undefined && mncError === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...operator, mcc, mnc })}
      onClose={onClose}
    >
      <TextField
        label="MCC"
        value={mcc}
        onChange={(e) => setMcc(e.target.value.trim())}
        error={mccError !== undefined}
        helperText={mccError}
        placeholder="001"
        required
      />
      <TextField
        label="MNC"
        value={mnc}
        onChange={(e) => setMnc(e.target.value.trim())}
        error={mncError !== undefined}
        helperText={mncError}
        placeholder="01"
        required
      />
    </EditDialog>
  );
}
