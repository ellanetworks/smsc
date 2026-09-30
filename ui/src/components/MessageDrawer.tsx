import type { ReactNode } from "react";
import {
  Box,
  Button,
  Divider,
  Drawer,
  IconButton,
  Paper,
  Skeleton,
  Stack,
  Typography,
} from "@mui/material";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import Fields from "@/components/Fields";
import QueryAlert from "@/components/QueryAlert";
import { MessageStatusChip, OutcomeChip } from "@/components/chips";
import {
  type AbsentUserDiagnostics,
  type Attempt,
  getMessage,
  listMessageAttempts,
  type Message,
} from "@/queries/messages";
import { formatTimestamp } from "@/utils/dates";
import {
  diagnosticsLabels,
  encodingLabels,
  messageText,
  nodeTypeLabels,
  partLabel,
  stepLabels,
} from "@/utils/labels";

export const DETAIL_REFRESH_MS = 5000;

const ATTEMPTS_PER_PAGE = 100;

const durationLabel = (a: Attempt): string => {
  const ms = Date.parse(a.completed_at) - Date.parse(a.started_at);
  if (Number.isNaN(ms)) return "—";
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
};

const resultCodeLabel = (a: Attempt): string | undefined => {
  if (a.result_code === undefined) return undefined;
  return a.vendor_id === undefined
    ? String(a.result_code)
    : `${a.result_code} (vendor ${a.vendor_id})`;
};

const diagnosticsRows = (d?: AbsentUserDiagnostics): [string, ReactNode][] =>
  d
    ? (Object.keys(diagnosticsLabels) as (keyof AbsentUserDiagnostics)[])
        .filter((k) => d[k])
        .map((k) => [`Absent (${diagnosticsLabels[k]})`, d[k]])
    : [];

function AttemptCard({ attempt, index }: { attempt: Attempt; index: number }) {
  const rows: [string, ReactNode][] = [
    ["Started", formatTimestamp(attempt.started_at)],
    ["Duration", durationLabel(attempt)],
  ];

  if (attempt.node) {
    rows.push([
      "Node",
      attempt.node_type
        ? `${attempt.node} (${nodeTypeLabels[attempt.node_type]})`
        : attempt.node,
    ]);
  }

  const resultCode = resultCodeLabel(attempt);
  if (resultCode) rows.push(["Result code", resultCode]);
  if (attempt.failure_cause)
    rows.push(["Failure cause", attempt.failure_cause]);
  if (attempt.tp_failure_cause) {
    rows.push(["TP failure cause", attempt.tp_failure_cause]);
  }
  rows.push(...diagnosticsRows(attempt.absent_user_diagnostics));

  return (
    <Paper component="li" variant="outlined" sx={{ p: 1.5 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: "center", mb: 1 }}>
        <Typography variant="subtitle2" component="h4">
          {index + 1}. {stepLabels[attempt.step]}
        </Typography>
        <OutcomeChip outcome={attempt.outcome} />
      </Stack>
      <Fields rows={rows} />
    </Paper>
  );
}

function Attempts({ message }: { message: Message }) {
  const pending = message.status === "pending";

  const { data, error, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useInfiniteQuery({
      queryKey: ["attempts", message.id],
      queryFn: ({ pageParam }) =>
        listMessageAttempts(message.id, {
          page: pageParam,
          perPage: ATTEMPTS_PER_PAGE,
        }),
      initialPageParam: 1,
      getNextPageParam: (last) =>
        last.page * last.per_page < last.total_count
          ? last.page + 1
          : undefined,
      refetchInterval: pending ? DETAIL_REFRESH_MS : false,
    });

  const attempts = data?.pages.flatMap((p) => p.items) ?? [];
  const total = data?.pages[0]?.total_count;

  return (
    <Box component="section" aria-labelledby="attempts-title">
      <Typography
        id="attempts-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        Delivery attempts{total !== undefined ? ` (${total})` : ""}
      </Typography>
      <QueryAlert
        error={error}
        hasData={data !== undefined}
        subject="delivery attempts"
      />
      {!data && !error && <Skeleton variant="rounded" height={120} />}
      {data && attempts.length === 0 && (
        <Typography color="text.secondary">
          No delivery attempts yet.
        </Typography>
      )}
      {attempts.length > 0 && (
        <Stack
          component="ol"
          spacing={1}
          sx={{ listStyle: "none", p: 0, m: 0 }}
        >
          {attempts.map((a, i) => (
            <AttemptCard key={a.id} attempt={a} index={i} />
          ))}
        </Stack>
      )}
      {hasNextPage && (
        <Button
          onClick={() => void fetchNextPage()}
          disabled={isFetchingNextPage}
          sx={{ mt: 1 }}
        >
          {isFetchingNextPage ? "Loading…" : "Load more"}
        </Button>
      )}
    </Box>
  );
}

function MessageDetail({
  initial,
  onClose,
}: {
  initial: Message;
  onClose: () => void;
}) {
  const { data, error } = useQuery({
    queryKey: ["message", initial.id],
    queryFn: () => getMessage(initial.id),
    placeholderData: initial,
    refetchInterval: (q) =>
      (q.state.data ?? initial).status === "pending"
        ? DETAIL_REFRESH_MS
        : false,
  });

  const message = data ?? initial;

  const rows: [string, ReactNode][] = [
    ["Status", <MessageStatusChip key="status" status={message.status} />],
    ["From", message.from],
    ["To", message.to],
    ["Encoding", encodingLabels[message.encoding]],
    ["Part", partLabel(message)],
  ];
  if (message.concatenation) {
    rows.push(["Reference", message.concatenation.reference]);
  }
  rows.push(
    ["Created", formatTimestamp(message.created_at)],
    ["Updated", formatTimestamp(message.updated_at)],
    ["Expires", formatTimestamp(message.expires_at)],
  );
  if (message.next_attempt_at) {
    rows.push(["Next attempt", formatTimestamp(message.next_attempt_at)]);
  }

  return (
    <Stack spacing={2} sx={{ p: 2 }}>
      <Stack direction="row" sx={{ alignItems: "center" }}>
        <Typography
          id="message-drawer-title"
          variant="h6"
          component="h2"
          sx={{ flexGrow: 1 }}
        >
          Message {message.id}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <Box
            component="span"
            aria-hidden
            sx={{ fontSize: 20, lineHeight: 1 }}
          >
            ×
          </Box>
        </IconButton>
      </Stack>
      <QueryAlert error={error} hasData subject="message" />
      <Fields rows={rows} />
      <Paper
        variant="outlined"
        sx={{
          p: 1.5,
          whiteSpace: "pre-wrap",
          overflowWrap: "anywhere",
          color: message.text === undefined ? "text.secondary" : undefined,
        }}
      >
        {messageText(message)}
      </Paper>
      <Divider />
      <Attempts message={message} />
    </Stack>
  );
}

interface MessageDrawerProps {
  message: Message | null;
  onClose: () => void;
}

export default function MessageDrawer({
  message,
  onClose,
}: MessageDrawerProps) {
  return (
    <Drawer
      anchor="right"
      open={message !== null}
      onClose={onClose}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 520 } },
          "aria-labelledby": "message-drawer-title",
        },
      }}
    >
      {message && (
        <MessageDetail key={message.id} initial={message} onClose={onClose} />
      )}
    </Drawer>
  );
}
