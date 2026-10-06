import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type OperatorSettings, updateOperator } from "@/queries/operator";

export default function EditNumberingDialog({
  operator,
  onClose,
}: {
  operator: OperatorSettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [numbering, setNumbering] = useState(operator.numbering);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      onClose();
    },
  });

  const errors = {
    country_code: /^\d{1,3}$/.test(numbering.country_code)
      ? undefined
      : "1 to 3 digits",
    national_prefix: /^\d*$/.test(numbering.national_prefix)
      ? undefined
      : "Digits only",
    international_prefix: /^\d*$/.test(numbering.international_prefix)
      ? undefined
      : "Digits only",
  };

  const field = (name: keyof typeof numbering) => ({
    value: numbering[name],
    onChange: (e: { target: { value: string } }) =>
      setNumbering((n) => ({ ...n, [name]: e.target.value.trim() })),
    error: errors[name] !== undefined,
    helperText: errors[name],
  });

  return (
    <EditDialog
      title="Edit Numbering"
      valid={Object.values(errors).every((e) => e === undefined)}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...operator, numbering })}
      onClose={onClose}
    >
      <TextField label="Country Code" {...field("country_code")} required />
      <TextField label="National Prefix" {...field("national_prefix")} />
      <TextField
        label="International Prefix"
        {...field("international_prefix")}
      />
    </EditDialog>
  );
}
