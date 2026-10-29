import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  Box,
  Button,
  Divider,
  IconButton,
  Paper,
  Skeleton,
  Stack,
  Typography,
} from "@mui/material";
import {
  Close as CloseIcon,
  DragIndicator as DragIndicatorIcon,
} from "@mui/icons-material";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import Fields from "@/components/Fields";
import QueryAlert from "@/components/QueryAlert";
import { useAuth } from "@/contexts/AuthContext";
import { MessageStatusChip, OutcomeChip } from "@/components/chips";
import {
  type AbsentUserDiagnostics,
  type Attempt,
  type BinaryContent,
  type DeliverReport,
  getMessage,
  getMessageContent,
  listMessageAttempts,
  listMessages,
  type Message,
  type StatusReport,
} from "@/queries/messages";
import { formatTimestamp } from "@/utils/dates";
import {
  diagnosticsLabels,
  encodingLabels,
  hex,
  nodeTypeLabels,
  partLabel,
  stepLabels,
  tpStatusLabel,
} from "@/utils/labels";

export const DETAIL_REFRESH_MS = 5000;

// EARLIER_PER_PAGE is how many earlier pending messages to the same number the drawer reads, to find the one it waits
// behind.
const EARLIER_PER_PAGE = 100;

const ATTEMPTS_PER_PAGE = 100;

// The panel is Ella Core's event panel: under the app bar, resizable between PANEL_MIN_WIDTH and PANEL_MAX_VW of the
// window.
const PANEL_DEFAULT_WIDTH = 560;
const PANEL_MIN_WIDTH = 350;
const PANEL_MAX_VW = 0.8;
const TOOLBAR_HEIGHT = 64;

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

const octets = (o: string): ReactNode => (
  <Box component="span" sx={{ fontFamily: "monospace" }}>
    {o === "" ? "—" : o}
  </Box>
);

// tpduRows are the TPDU parameters of a binary message or a deliver report, those that it has.
const tpduRows = (p: BinaryContent | DeliverReport): [string, ReactNode][] => {
  const rows: [string, ReactNode][] = [];
  if (p.protocol_identifier !== undefined) {
    rows.push(["Protocol identifier", hex(p.protocol_identifier)]);
  }
  if (p.data_coding_scheme !== undefined) {
    rows.push(["Data coding scheme", hex(p.data_coding_scheme)]);
  }
  if (p.user_data_header !== undefined) {
    rows.push(["User data header", octets(p.user_data_header)]);
  }
  if (p.user_data !== undefined) rows.push(["User data", octets(p.user_data)]);
  return rows;
};

// DeliverReports shows what the recipient answered the delivery attempts with, oldest first, such as a (U)SIM's answer
// to a (U)SIM data download message.
function DeliverReports({ reports }: { reports: DeliverReport[] }) {
  return (
    <Box component="section" aria-labelledby="deliver-reports-title">
      <Typography
        id="deliver-reports-title"
        variant="subtitle2"
        component="h4"
        sx={{ mt: 2, mb: 1 }}
      >
        Deliver reports ({reports.length})
      </Typography>
      <Stack component="ol" spacing={1} sx={{ listStyle: "none", p: 0, m: 0 }}>
        {reports.map((r) => (
          <Paper
            key={r.attempt_id}
            component="li"
            variant="outlined"
            sx={{ p: 1.5 }}
          >
            <Fields rows={[["TPDU", octets(r.tpdu)], ...tpduRows(r)]} />
          </Paper>
        ))}
      </Stack>
    </Box>
  );
}

// Content shows the content of a message only when asked to: each read is audited. It is fetched once, and forgotten
// with the drawer.
function Content({ message }: { message: Message }) {
  const [shown, setShown] = useState(false);
  const query = useQuery({
    queryKey: ["message-content", message.id],
    queryFn: () => getMessageContent(message.id),
    enabled: shown,
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
  });
  const { data } = query;

  return (
    <Box component="section" aria-labelledby="content-title">
      <Typography
        id="content-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        Content
      </Typography>
      {!shown && (
        <Button variant="outlined" onClick={() => setShown(true)}>
          Show content
        </Button>
      )}
      {shown && <QueryAlert query={query} subject="content" />}
      {shown && !data && !query.error && (
        <Skeleton variant="rounded" height={48} />
      )}
      {data && data.binary && (
        <Paper variant="outlined" sx={{ p: 1.5 }}>
          <Fields rows={tpduRows(data.binary)} />
        </Paper>
      )}
      {data && !data.binary && (
        <Paper
          variant="outlined"
          sx={{
            p: 1.5,
            whiteSpace: "pre-wrap",
            overflowWrap: "anywhere",
            color: data.text === undefined ? "text.secondary" : undefined,
          }}
        >
          {data.text ?? "Binary content is not shown."}
        </Paper>
      )}
      {data?.deliver_reports && (
        <DeliverReports reports={data.deliver_reports} />
      )}
    </Box>
  );
}

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

  const query = useInfiniteQuery({
    queryKey: ["attempts", message.id],
    queryFn: ({ pageParam }) =>
      listMessageAttempts(message.id, {
        page: pageParam,
        perPage: ATTEMPTS_PER_PAGE,
      }),
    initialPageParam: 1,
    getNextPageParam: (last) =>
      last.page * last.per_page < last.total_count ? last.page + 1 : undefined,
    refetchInterval: pending ? DETAIL_REFRESH_MS : false,
  });
  const { data, error, fetchNextPage, hasNextPage, isFetchingNextPage } = query;

  const attempts = data?.pages.flatMap((p) => p.items) ?? [];
  const total = data?.pages[0]?.total_count;

  // A pending message waits for the earlier pending messages to the same number, which are delivered first. The list
  // ends just after this message, since the parts of a message share its time; the to filter matches by part, so the
  // number is compared too.
  const earlier = useQuery({
    queryKey: ["messages", "earlier", message.id],
    queryFn: () =>
      listMessages({
        to: message.to,
        status: "pending",
        end: new Date(new Date(message.created_at).getTime() + 1).toISOString(),
        perPage: EARLIER_PER_PAGE,
      }),
    enabled: pending,
    refetchInterval: pending ? DETAIL_REFRESH_MS : false,
  });
  const behind = pending
    ? earlier.data?.items.find((m) => m.id < message.id && m.to === message.to)
    : undefined;

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
      <QueryAlert query={query} subject="delivery attempts" />
      {behind && (
        <Typography color="text.secondary" sx={{ mb: 1 }}>
          Waits behind message {behind.id} to the same number.
        </Typography>
      )}
      {!data && !error && <Skeleton variant="rounded" height={120} />}
      {data && attempts.length === 0 && (
        <Typography color="text.secondary">
          {pending ? "No delivery attempts yet." : "No delivery attempts."}
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

function StatusReportCard({ report }: { report: StatusReport }) {
  const rows: [string, ReactNode][] = [
    ["Created", formatTimestamp(report.created_at)],
    ["Updated", formatTimestamp(report.updated_at)],
  ];
  if (report.next_attempt_at) {
    rows.push(["Next attempt", formatTimestamp(report.next_attempt_at)]);
  }

  return (
    <Paper component="li" variant="outlined" sx={{ p: 1.5 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: "center", mb: 1 }}>
        <Typography variant="subtitle2" component="h4">
          {tpStatusLabel(report)}
        </Typography>
        <MessageStatusChip status={report.status} />
      </Stack>
      <Fields rows={rows} />
    </Paper>
  );
}

// StatusReports shows whether the phone that sent the message asked for status reports, and those the SMSC returned
// to it (TS 23.040 clause 3.2.9).
function StatusReports({ message }: { message: Message }) {
  const { requested, reports } = message.status_report;

  return (
    <Box component="section" aria-labelledby="status-reports-title">
      <Typography
        id="status-reports-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        Status reports{reports.length > 0 ? ` (${reports.length})` : ""}
      </Typography>
      {reports.length === 0 && (
        <Typography color="text.secondary">
          {requested ? "None yet." : "Not requested."}
        </Typography>
      )}
      {reports.length > 0 && (
        <Stack
          component="ol"
          spacing={1}
          sx={{ listStyle: "none", p: 0, m: 0 }}
        >
          {reports.map((r) => (
            <StatusReportCard key={r.id} report={r} />
          ))}
        </Stack>
      )}
    </Box>
  );
}

// inProgress tells whether the message, or a status report on it, still waits for delivery.
export const inProgress = (m: Message): boolean =>
  m.status === "pending" ||
  m.status_report.reports.some((r) => r.status === "pending");

function MessageDetail({
  initial,
  onClose,
}: {
  initial: Message;
  onClose: () => void;
}) {
  const auth = useAuth();
  const query = useQuery({
    queryKey: ["message", initial.id],
    queryFn: () => getMessage(initial.id),
    placeholderData: initial,
    refetchInterval: (q) =>
      inProgress(q.state.data ?? initial) ? DETAIL_REFRESH_MS : false,
  });
  const { data } = query;

  const message = data ?? initial;

  const rows: [string, ReactNode][] = [
    ["From", message.from],
    ["To", message.to],
  ];
  if (message.submitted_by) rows.push(["Submitted by", message.submitted_by]);
  if (message.system_id) rows.push(["SMPP system ID", message.system_id]);
  rows.push(
    ["Encoding", encodingLabels[message.encoding]],
    ["Part", partLabel(message)],
  );
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
    <>
      <Box
        sx={{
          px: 2,
          py: 1.5,
          borderBottom: 1,
          borderColor: "divider",
          display: "flex",
          alignItems: "center",
          gap: 1,
        }}
      >
        <Box
          sx={{
            flex: 1,
            minWidth: 0,
            display: "flex",
            alignItems: "center",
            gap: 1,
          }}
        >
          <Typography
            id="message-drawer-title"
            variant="h6"
            component="h2"
            noWrap
          >
            Message {message.id}
          </Typography>
          <MessageStatusChip status={message.status} />
        </Box>
        <IconButton aria-label="Close" onClick={onClose} size="small">
          <CloseIcon />
        </IconButton>
      </Box>
      <Stack spacing={2} sx={{ p: 2, overflow: "auto", flex: 1 }}>
        <QueryAlert query={query} subject="message" />
        <Fields rows={rows} />
        {auth.can("message:content") && (
          <>
            <Divider />
            <Content message={message} />
          </>
        )}
        <Divider />
        <Attempts message={message} />
        <Divider />
        <StatusReports message={message} />
      </Stack>
    </>
  );
}

interface MessageDrawerProps {
  message: Message | null;
  onClose: () => void;
}

// MessageDrawer shows a message beside the list, as Ella Core shows a network event: the list stays usable, and
// Escape closes it.
export default function MessageDrawer({
  message,
  onClose,
}: MessageDrawerProps) {
  const open = message !== null;
  const panelRef = useRef<HTMLElement>(null);
  const [width, setWidth] = useState(PANEL_DEFAULT_WIDTH);

  // Opening the panel moves focus into it, and closing it gives focus back, to the row that opened it.
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement;
    panelRef.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) {
        previous.focus();
      }
    };
  }, [open]);
  const [dragging, setDragging] = useState(false);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const onResizeMouseDown = (e: React.MouseEvent) => {
    e.preventDefault();
    setDragging(true);

    const onMouseMove = (ev: MouseEvent) => {
      const max = window.innerWidth * PANEL_MAX_VW;
      setWidth(
        Math.max(
          PANEL_MIN_WIDTH,
          Math.min(max, window.innerWidth - ev.clientX),
        ),
      );
    };
    const onMouseUp = () => {
      setDragging(false);
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseup", onMouseUp);
    };
    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseup", onMouseUp);
  };

  return (
    <Box
      component="aside"
      ref={panelRef}
      tabIndex={-1}
      aria-labelledby={open ? "message-drawer-title" : undefined}
      aria-hidden={!open}
      inert={!open}
      sx={{
        position: "fixed",
        top: TOOLBAR_HEIGHT,
        right: 0,
        bottom: 0,
        width: { xs: "100%", sm: width },
        transform: open ? "translateX(0)" : "translateX(100%)",
        transition: dragging ? "none" : "transform 200ms ease-in-out",
        zIndex: (t) => t.zIndex.appBar - 1,
        bgcolor: "background.paper",
        boxShadow: open ? 8 : "none",
        display: "flex",
        flexDirection: "row",
        outline: "none",
      }}
    >
      <Box
        onMouseDown={onResizeMouseDown}
        sx={{
          width: 12,
          flexShrink: 0,
          cursor: "ew-resize",
          display: { xs: "none", sm: "flex" },
          alignItems: "center",
          justifyContent: "center",
          "&:hover .resizeIcon": { opacity: 1 },
        }}
      >
        <DragIndicatorIcon
          className="resizeIcon"
          sx={{
            fontSize: 20,
            opacity: 0.5,
            transition: "opacity 120ms",
            color: "text.secondary",
          }}
        />
      </Box>
      <Box
        sx={{
          flex: 1,
          minWidth: 0,
          display: "flex",
          flexDirection: "column",
          borderLeft: 1,
          borderColor: "divider",
        }}
      >
        {message && (
          <MessageDetail key={message.id} initial={message} onClose={onClose} />
        )}
      </Box>
    </Box>
  );
}
