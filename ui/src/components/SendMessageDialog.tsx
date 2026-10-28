import { useState, type FormEvent } from "react";
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import DialogError from "@/components/DialogError";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createMessage, type Message } from "@/queries/messages";
import { isE164 } from "@/utils/e164";
import { isOriginator } from "@/utils/originator";
import { encodingLabels } from "@/utils/labels";
import { segmentText } from "@/utils/sms";

interface SendMessageDialogProps {
  open: boolean;
  onClose: () => void;
  onSent: (parts: Message[]) => void;
}

const pluralize = (n: number, word: string) =>
  `${n} ${word}${n === 1 ? "" : "s"}`;

const fieldError = (
  value: string,
  touched: boolean,
  isValid: (value: string) => boolean,
  hint: string,
) => {
  if (!touched) return undefined;
  if (value === "") return "Required";
  if (!isValid(value)) return hint;
  return undefined;
};

function TextSummary({ text }: { text: string }) {
  const { encoding, partSizes, tooLong } = segmentText(text);
  const characters = [...text].length;

  if (tooLong) {
    return (
      <Typography variant="body2" color="error">
        Too long: the text needs more than 255 parts.
      </Typography>
    );
  }

  return (
    <Typography variant="body2" color="text.secondary">
      {pluralize(characters, "character")} ·{" "}
      {pluralize(partSizes.length, "part")} · {encodingLabels[encoding]}
      {encoding === "ucs2" && text !== "" && " (up to 70 characters per part)"}
    </Typography>
  );
}

export default function SendMessageDialog({
  open,
  onClose,
  onSent,
}: SendMessageDialogProps) {
  const queryClient = useQueryClient();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [text, setText] = useState("");
  const [touched, setTouched] = useState({ from: false, to: false });

  const mutation = useMutation({
    mutationFn: createMessage,
    onSuccess: (parts) => {
      void queryClient.invalidateQueries({ queryKey: ["messages"] });
      setText("");
      setTouched({ from: false, to: false });
      onSent(parts);
    },
  });

  // The value is kept as typed, since a name may hold spaces, and trimmed of those around it to be used.
  const sender = from.trim();
  const fromError = fieldError(
    sender,
    touched.from,
    isOriginator,
    "E.164, e.g. +15551230001, or a name of up to 11 letters and digits, with single spaces",
  );
  const toError = fieldError(
    to,
    touched.to,
    isE164,
    "E.164, e.g. +15551230002",
  );
  const valid =
    isOriginator(sender) &&
    isE164(to) &&
    text !== "" &&
    !segmentText(text).tooLong;

  const close = () => {
    mutation.reset();
    onClose();
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid || mutation.isPending) return;
    mutation.mutate({ from: sender, to, text });
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      fullWidth
      maxWidth="sm"
      slotProps={{ paper: { component: "form", onSubmit: submit } }}
    >
      <DialogTitle>Send</DialogTitle>
      <DialogContent dividers>
        <Stack spacing={2}>
          <DialogError
            error={mutation.error}
            prefix="Could not send the message"
          />
          <TextField
            label="From"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            onBlur={() => setTouched((t) => ({ ...t, from: true }))}
            error={fromError !== undefined}
            helperText={fromError}
            placeholder="+15551230001"
            required
          />
          <TextField
            label="To"
            value={to}
            onChange={(e) => setTo(e.target.value.trim())}
            onBlur={() => setTouched((t) => ({ ...t, to: true }))}
            error={toError !== undefined}
            helperText={toError}
            placeholder="+15551230002"
            required
          />
          <TextField
            label="Text"
            value={text}
            onChange={(e) => setText(e.target.value)}
            multiline
            minRows={4}
            required
          />
          <TextSummary text={text} />
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Cancel</Button>
        <Button
          type="submit"
          variant="contained"
          color="success"
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending ? "Sending…" : "Send"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
