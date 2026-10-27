import { useState } from "react";
import {
  Box,
  Button,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { Edit as EditIcon } from "@mui/icons-material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import EditDeliveryDialog from "@/components/EditDeliveryDialog";
import MessageDrawer from "@/components/MessageDrawer";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SendMessageDialog from "@/components/SendMessageDialog";
import { MessageStatusChip } from "@/components/chips";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import {
  listMessages,
  type Message,
  type MessageStatus,
} from "@/queries/messages";
import { useAuth } from "@/contexts/AuthContext";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { getDelivery } from "@/queries/delivery";
import { RoleID } from "@/queries/users";
import { GRID_SX } from "@/utils/layout";
import { formatTimestamp } from "@/utils/dates";
import { formatDuration } from "@/utils/durations";
import { isE164 } from "@/utils/e164";
import { encodingLabels, messageText, partLabel } from "@/utils/labels";

export const MESSAGES_REFRESH_MS = 5000;

const FILTER_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const STATUSES: MessageStatus[] = [
  "pending",
  "delivered",
  "failed",
  "expired",
  "replaced",
];

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
  {
    field: "submitted_by",
    headerName: "Submitted by",
    width: 200,
    valueGetter: (_value, row) => row.submitted_by ?? "",
  },
];

const hiddenByDefault = {
  updated_at: false,
  expires_at: false,
  encoding: false,
};

export default function Messages() {
  const auth = useAuth();
  const { showSnackbar } = useSnackbar();
  const [to, setTo] = useState("");
  const [from, setFrom] = useState("");
  const [status, setStatus] = useState<MessageStatus | "">("");
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });
  const [selected, setSelected] = useState<Message | null>(null);
  const [editingDelivery, setEditingDelivery] = useState(false);
  const delivery = useQuery({
    queryKey: ["delivery"],
    queryFn: getDelivery,
    enabled: auth.can("delivery:read"),
  });
  const [sending, setSending] = useState(false);

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

  const messages = useQuery({
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
  const { data, isFetching } = messages;

  const resetPage = () => setPagination((p) => ({ ...p, page: 0 }));

  return (
    <Box component="section" aria-labelledby="messages-title">
      <PageHeader
        id="messages-title"
        title="Messages"
        action={
          auth.can("message:create") && (
            <Button
              variant="contained"
              color="success"
              onClick={() => setSending(true)}
            >
              Send
            </Button>
          )
        }
      />
      <Stack direction={{ xs: "column", sm: "row" }} spacing={2} sx={{ mb: 2 }}>
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
        {auth.can("delivery:read") && (
          <Box
            data-testid="delivery-settings"
            sx={{
              display: "flex",
              alignItems: "center",
              gap: 1,
              ml: { sm: "auto" },
            }}
          >
            <Typography variant="body2" color="textSecondary">
              <Tooltip
                title="How long a message without its own validity period is kept for delivery."
                arrow
              >
                <span>Validity</span>
              </Tooltip>
              :{" "}
              <strong>
                {delivery.data
                  ? formatDuration(delivery.data.default_validity_seconds)
                  : "…"}
              </strong>
              {" · "}
              <Tooltip
                title="Wait before each new delivery attempt. The last one repeats."
                arrow
              >
                <span>Retries</span>
              </Tooltip>
              :{" "}
              <strong>
                {delivery.data
                  ? delivery.data.retry_intervals_seconds
                      .map(formatDuration)
                      .join(", ") || "none"
                  : "…"}
              </strong>
            </Typography>
            {auth.can("delivery:update") && (
              <IconButton
                aria-label="Edit delivery"
                size="small"
                onClick={() => setEditingDelivery(true)}
                disabled={!delivery.data}
              >
                <EditIcon fontSize="small" color="primary" />
              </IconButton>
            )}
          </Box>
        )}
      </Stack>
      <QueryAlert query={messages} subject="messages" />
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
        initialState={{
          columns: {
            columnVisibilityModel: {
              ...hiddenByDefault,
              // A Submitter sees only its own messages.
              submitted_by: auth.roleId !== RoleID.Submitter,
            },
          },
        }}
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
        sx={{ ...GRID_SX, "& .MuiDataGrid-row": { cursor: "pointer" } }}
      />
      <MessageDrawer message={selected} onClose={() => setSelected(null)} />
      {editingDelivery && delivery.data && (
        <EditDeliveryDialog
          delivery={delivery.data}
          onClose={() => setEditingDelivery(false)}
        />
      )}
      <SendMessageDialog
        open={sending}
        onClose={() => setSending(false)}
        onSent={(parts) => {
          setSending(false);
          showSnackbar(
            parts.length > 1
              ? `Message queued as ${parts.length} parts.`
              : "Message queued.",
            "success",
            { label: "View", onClick: () => setSelected(parts[0] ?? null) },
          );
        }}
      />
    </Box>
  );
}
