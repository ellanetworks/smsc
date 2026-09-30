import { useState } from "react";
import {
  Alert,
  Button,
  Card,
  CardContent,
  CardHeader,
  MenuItem,
  Snackbar,
  Stack,
  TextField,
} from "@mui/material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import MessageDrawer from "@/components/MessageDrawer";
import QueryAlert from "@/components/QueryAlert";
import SendMessageDialog from "@/components/SendMessageDialog";
import { MessageStatusChip } from "@/components/chips";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import {
  listMessages,
  type Message,
  type MessageStatus,
} from "@/queries/messages";
import { formatTimestamp } from "@/utils/dates";
import { isE164 } from "@/utils/e164";
import { encodingLabels, messageText, partLabel } from "@/utils/labels";

export const MESSAGES_REFRESH_MS = 5000;

const FILTER_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const STATUSES: MessageStatus[] = ["pending", "delivered", "failed", "expired"];

const E164_HINT = "E.164, e.g. +15551230002";

const formatOptionalTimestamp = (value?: string) =>
  value ? formatTimestamp(value) : "—";

const columns: GridColDef<Message>[] = [
  {
    field: "created_at",
    headerName: "Created",
    width: 170,
    valueFormatter: formatOptionalTimestamp,
  },
  { field: "from", headerName: "From", width: 160 },
  { field: "to", headerName: "To", width: 160 },
  {
    field: "text",
    headerName: "Text",
    flex: 1,
    minWidth: 200,
    valueGetter: (_value, row) => messageText(row),
  },
  {
    field: "part",
    headerName: "Part",
    width: 90,
    valueGetter: (_value, row) => partLabel(row),
  },
  {
    field: "status",
    headerName: "Status",
    width: 120,
    renderCell: ({ row }) => <MessageStatusChip status={row.status} />,
  },
  {
    field: "updated_at",
    headerName: "Updated",
    width: 170,
    valueFormatter: formatOptionalTimestamp,
  },
  {
    field: "expires_at",
    headerName: "Expires",
    width: 170,
    valueFormatter: formatOptionalTimestamp,
  },
  {
    field: "encoding",
    headerName: "Encoding",
    width: 110,
    valueGetter: (_value, row) => encodingLabels[row.encoding],
  },
];

const hiddenByDefault = {
  updated_at: false,
  expires_at: false,
  encoding: false,
};

export default function MessagesSection() {
  const [to, setTo] = useState("");
  const [from, setFrom] = useState("");
  const [status, setStatus] = useState<MessageStatus | "">("");
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });
  const [selected, setSelected] = useState<Message | null>(null);
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState<Message[]>([]);
  const [sentOpen, setSentOpen] = useState(false);

  const toInvalid = to !== "" && !isE164(to);
  const fromInvalid = from !== "" && !isE164(from);

  const debouncedTo = useDebouncedValue(
    toInvalid ? "" : to,
    FILTER_DEBOUNCE_MS,
  );
  const debouncedFrom = useDebouncedValue(
    fromInvalid ? "" : from,
    FILTER_DEBOUNCE_MS,
  );
  const filters = {
    to: debouncedTo,
    from: debouncedFrom,
    status: status || undefined,
  };

  const { data, error, isFetching } = useQuery({
    queryKey: ["messages", filters, pagination],
    queryFn: () =>
      listMessages({
        ...filters,
        page: pagination.page + 1,
        perPage: pagination.pageSize,
      }),
    placeholderData: (prev) => prev,
    refetchInterval: MESSAGES_REFRESH_MS,
  });

  const resetPage = () => setPagination((p) => ({ ...p, page: 0 }));

  return (
    <Card
      component="section"
      aria-labelledby="messages-title"
      variant="outlined"
    >
      <CardHeader
        title="Messages"
        slotProps={{ title: { id: "messages-title", component: "h2" } }}
        action={
          <Button variant="contained" onClick={() => setSending(true)}>
            Send
          </Button>
        }
      />
      <CardContent>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={2}
          sx={{ mb: 2 }}
        >
          <TextField
            label="To"
            size="small"
            value={to}
            onChange={(e) => {
              setTo(e.target.value.trim());
              resetPage();
            }}
            error={toInvalid}
            helperText={toInvalid ? E164_HINT : undefined}
            placeholder="+15551230002"
          />
          <TextField
            label="From"
            size="small"
            value={from}
            onChange={(e) => {
              setFrom(e.target.value.trim());
              resetPage();
            }}
            error={fromInvalid}
            helperText={fromInvalid ? E164_HINT : undefined}
            placeholder="+15551230001"
          />
          <TextField
            select
            label="Status"
            size="small"
            value={status}
            onChange={(e) => {
              setStatus(e.target.value as MessageStatus | "");
              resetPage();
            }}
            sx={{ minWidth: 160 }}
          >
            <MenuItem value="">All</MenuItem>
            {STATUSES.map((s) => (
              <MenuItem key={s} value={s}>
                {s}
              </MenuItem>
            ))}
          </TextField>
        </Stack>
        <QueryAlert
          error={error}
          hasData={data !== undefined}
          subject="messages"
        />
        <DataGrid<Message>
          aria-label="Messages"
          rows={data?.items ?? []}
          columns={columns}
          rowCount={data?.total_count ?? 0}
          loading={data === undefined && isFetching}
          paginationMode="server"
          paginationModel={pagination}
          onPaginationModelChange={setPagination}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          initialState={{ columns: { columnVisibilityModel: hiddenByDefault } }}
          onRowClick={({ row }) => setSelected(row)}
          onCellKeyDown={({ row }, event) => {
            if (event.key === "Enter") setSelected(row);
          }}
          disableColumnSorting
          disableColumnFilter
          disableRowSelectionOnClick
          disableVirtualization
          autoHeight
          localeText={{ noRowsLabel: "No messages." }}
          sx={{ "& .MuiDataGrid-row": { cursor: "pointer" } }}
        />
      </CardContent>
      <MessageDrawer message={selected} onClose={() => setSelected(null)} />
      <SendMessageDialog
        open={sending}
        onClose={() => setSending(false)}
        onSent={(parts) => {
          setSending(false);
          setSent(parts);
          setSentOpen(true);
        }}
      />
      <Snackbar
        open={sentOpen}
        autoHideDuration={8000}
        onClose={(_event, reason) => {
          if (reason !== "clickaway") setSentOpen(false);
        }}
      >
        <Alert
          severity="success"
          variant="filled"
          action={
            <Button
              color="inherit"
              size="small"
              onClick={() => {
                setSelected(sent[0] ?? null);
                setSentOpen(false);
              }}
            >
              View
            </Button>
          }
        >
          {sent.length > 1
            ? `Message queued as ${sent.length} parts.`
            : "Message queued."}
        </Alert>
      </Snackbar>
    </Card>
  );
}
