import { useCallback, useMemo, useState } from "react";
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
import EditRetentionDialog from "@/components/EditRetentionDialog";
import MessageDrawer from "@/components/MessageDrawer";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SendMessageDialog from "@/components/SendMessageDialog";
import TimeRangePicker, {
  AUDIT_RANGES,
  EMPTY_TIME_RANGE,
  timeRangeFilter,
  timeRangeParams,
  type TimeRangeValue,
} from "@/components/TimeRangePicker";
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
import { getRetention } from "@/queries/retention";
import { RoleID } from "@/queries/users";
import { GRID_SX } from "@/utils/layout";
import { formatTimestamp } from "@/utils/dates";
import { formatDuration } from "@/utils/durations";
import { isPartialNumber } from "@/utils/e164";
import { isOriginatorName } from "@/utils/originator";
import { capitalized, encodingLabels, partLabel } from "@/utils/labels";

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

const NUMBER_HINT = "All or part of a number, e.g. +1555123 or 0002";

const SENDER_HINT =
  "All or part of a number, e.g. +1555123, or a name of up to 11 letters and digits, with single spaces";

// A filter is applied once valid; a lone "+", the start of a number, is neither applied nor an error.
const numberStarted = (value: string) => value === "+";

const formatOptionalTimestamp = (value?: string) =>
  value ? formatTimestamp(value) : "—";

// DATE_WIDTH fits a date of 19 characters, YYYY-MM-DD HH:MM:SS: 160px in Source Code Pro at 14px, with the 10px
// padding each side of a cell, and some room.
const DATE_WIDTH = 190;

const columns: GridColDef<Message>[] = [
  {
    field: "created_at",
    headerName: "Created",
    width: DATE_WIDTH,
    valueFormatter: formatOptionalTimestamp,
  },
  // The text columns share the spare width, so that none is cut while there is room.
  // At 140px, they fit an E.164 number of 15 digits or a name of 11 characters, and the columns fit 1280px wide.
  { field: "from", headerName: "From", flex: 1, minWidth: 140 },
  { field: "to", headerName: "To", flex: 1, minWidth: 140 },
  {
    field: "part",
    headerName: "Part",
    width: 80,
    valueGetter: (_value, row) => partLabel(row),
  },
  {
    field: "status",
    headerName: "Status",
    width: 110,
    renderCell: ({ row }) => <MessageStatusChip status={row.status} />,
  },
  {
    field: "updated_at",
    headerName: "Updated",
    width: DATE_WIDTH,
    valueFormatter: formatOptionalTimestamp,
  },
  {
    field: "expires_at",
    headerName: "Expires",
    width: DATE_WIDTH,
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
    flex: 1.5,
    minWidth: 220,
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
  const closeDrawer = useCallback(() => setSelected(null), []);
  const [editingDelivery, setEditingDelivery] = useState(false);
  const delivery = useQuery({
    queryKey: ["delivery"],
    queryFn: getDelivery,
    enabled: auth.can("delivery:read"),
  });
  const [sending, setSending] = useState(false);
  const retention = useQuery({
    queryKey: ["retention"],
    queryFn: getRetention,
    enabled: auth.can("retention:read"),
  });
  const [editingRetention, setEditingRetention] = useState(false);
  const [timeRange, setTimeRange] = useState<TimeRangeValue>(EMPTY_TIME_RANGE);
  const timeFilter = useMemo(() => timeRangeFilter(timeRange), [timeRange]);

  // Numbers match by part, and names in any case.
  const toInvalid = to !== "" && !numberStarted(to) && !isPartialNumber(to);
  // The value is kept as typed, since a name may hold spaces, and trimmed of those around it to be used.
  const sender = from.trim();
  const fromInvalid =
    sender !== "" &&
    !numberStarted(sender) &&
    !isPartialNumber(sender) &&
    !isOriginatorName(sender);

  const debouncedTo = useDebouncedValue(
    toInvalid || numberStarted(to) ? "" : to,
    FILTER_DEBOUNCE_MS,
  );
  const debouncedFrom = useDebouncedValue(
    fromInvalid || numberStarted(sender) ? "" : sender,
    FILTER_DEBOUNCE_MS,
  );
  const filters = {
    to: debouncedTo,
    from: debouncedFrom,
    status: status || undefined,
  };

  // A relative range resolves on each request, so that the refresh keeps "Last 1 hour" moving.
  const messages = useQuery({
    queryKey: ["messages", filters, timeFilter, pagination],
    queryFn: () =>
      listMessages({
        ...filters,
        ...timeRangeParams(timeFilter, { ranges: AUDIT_RANGES }),
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
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={2}
        useFlexGap
        sx={{ mb: 2, flexWrap: "wrap" }}
      >
        <TimeRangePicker
          value={timeRange}
          onChange={(next) => {
            setTimeRange(next);
            resetPage();
          }}
          ranges={AUDIT_RANGES}
        />
        <TextField
          label="To"
          size="small"
          value={to}
          onChange={(e) => {
            setTo(e.target.value.trim());
            resetPage();
          }}
          error={toInvalid}
          helperText={toInvalid ? NUMBER_HINT : undefined}
          placeholder="+15551230002"
        />
        <TextField
          label="From"
          size="small"
          value={from}
          onChange={(e) => {
            setFrom(e.target.value);
            resetPage();
          }}
          error={fromInvalid}
          helperText={fromInvalid ? SENDER_HINT : undefined}
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
              {capitalized(s)}
            </MenuItem>
          ))}
        </TextField>
        {(auth.can("delivery:read") || auth.can("retention:read")) && (
          <Box
            data-testid="delivery-settings"
            sx={{
              display: "flex",
              alignItems: "center",
              flexWrap: "wrap",
              gap: 1,
              ml: { sm: "auto" },
            }}
          >
            {auth.can("delivery:read") && (
              <>
                <Typography variant="body2" color="textSecondary">
                  <Tooltip
                    title="How long delivery of a message without its own validity period is attempted."
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
              </>
            )}
            {auth.can("retention:read") && (
              <>
                <Typography variant="body2" color="textSecondary">
                  <Tooltip
                    title="How long finished messages are kept before they are deleted."
                    arrow
                  >
                    <span>Retention</span>
                  </Tooltip>
                  :{" "}
                  <strong>
                    {retention.data
                      ? `${retention.data.days} ${retention.data.days === 1 ? "day" : "days"}`
                      : "…"}
                  </strong>
                </Typography>
                {auth.can("retention:update") && (
                  <IconButton
                    aria-label="Edit retention"
                    size="small"
                    onClick={() => setEditingRetention(true)}
                    disabled={!retention.data}
                  >
                    <EditIcon fontSize="small" color="primary" />
                  </IconButton>
                )}
              </>
            )}
          </Box>
        )}
      </Stack>
      <QueryAlert query={messages} subject="messages" />
      <DataGrid<Message>
        aria-label="Messages"
        density="compact"
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
        hideFooterSelectedRowCount
        rowSelectionModel={{
          type: "include",
          ids: new Set(selected ? [selected.id] : []),
        }}
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: "No messages." }}
        sx={{
          ...GRID_SX,
          "& .MuiDataGrid-row": { cursor: "pointer" },
          "& .MuiDataGrid-row.Mui-selected": {
            backgroundColor: (t) => t.palette.action.selected,
            "&:hover": { backgroundColor: (t) => t.palette.action.selected },
            "& .MuiDataGrid-cell": { fontWeight: 500 },
          },
        }}
      />
      <MessageDrawer message={selected} onClose={closeDrawer} />
      {editingDelivery && delivery.data && (
        <EditDeliveryDialog
          delivery={delivery.data}
          onClose={() => setEditingDelivery(false)}
        />
      )}
      {editingRetention && retention.data && (
        <EditRetentionDialog
          retention={retention.data}
          onClose={() => setEditingRetention(false)}
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
