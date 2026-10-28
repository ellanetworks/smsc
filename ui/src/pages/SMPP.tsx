import { useState } from "react";
import {
  Box,
  Button,
  IconButton,
  Skeleton,
  Stack,
  Tooltip,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import ESMEDialog, { ESMEPasswordDialog } from "@/components/ESMEDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import {
  type BindType,
  type CreatedESME,
  deleteESME,
  type ESME,
  listESMEs,
  MAX_SESSIONS,
} from "@/queries/smpp";
import { formatTimestamp } from "@/utils/dates";
import { GRID_SX } from "@/utils/layout";

export const SMPP_REFRESH_MS = 5000;

const bindLabels: Record<BindType, string> = {
  transmitter: "Transmitter",
  receiver: "Receiver",
  transceiver: "Transceiver",
};

const columns = (
  editable: boolean,
  onEdit: (esme: ESME) => void,
  onDelete: (esme: ESME) => void,
): GridColDef<ESME>[] => [
  {
    field: "system_id",
    headerName: "System ID",
    flex: 0.7,
    minWidth: 130,
  },
  {
    field: "user",
    headerName: "User",
    flex: 1,
    minWidth: 180,
  },
  {
    field: "allowed_networks",
    headerName: "Allowed Networks",
    flex: 1,
    minWidth: 160,
    renderCell: ({ row }) =>
      row.allowed_networks.length === 0 ? (
        "None"
      ) : (
        <Stack>
          {row.allowed_networks.map((n) => (
            <span key={n}>{n}</span>
          ))}
        </Stack>
      ),
  },
  {
    field: "sessions",
    headerName: "Sessions",
    flex: 1.2,
    minWidth: 220,
    renderCell: ({ row }) =>
      row.sessions.length === 0 ? (
        "None"
      ) : (
        <Stack>
          <span>
            {row.sessions.length} of {MAX_SESSIONS} bound
          </span>
          {row.sessions.map((s) => (
            <Tooltip
              key={`${s.remote_address}|${s.bound_at}|${s.bind_type}`}
              title={`Bound ${formatTimestamp(s.bound_at)}`}
              arrow
            >
              <span>
                {bindLabels[s.bind_type] ?? s.bind_type} from {s.remote_address}
              </span>
            </Tooltip>
          ))}
        </Stack>
      ),
  },
  ...(editable
    ? [
        {
          field: "actions",
          headerName: "Actions",
          width: 100,
          align: "right",
          headerAlign: "right",
          renderCell: ({ row }) => (
            <>
              <Tooltip title="Edit ESME" arrow>
                <IconButton
                  size="small"
                  aria-label={`Edit ${row.system_id}`}
                  onClick={() => onEdit(row)}
                >
                  <EditIcon fontSize="small" color="primary" />
                </IconButton>
              </Tooltip>
              <Tooltip title="Delete ESME" arrow>
                <IconButton
                  size="small"
                  aria-label={`Delete ${row.system_id}`}
                  onClick={() => onDelete(row)}
                >
                  <DeleteIcon fontSize="small" color="error" />
                </IconButton>
              </Tooltip>
            </>
          ),
        } satisfies GridColDef<ESME>,
      ]
    : []),
];

export default function SMPP() {
  const auth = useAuth();
  const editable = auth.can("esme:update");
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [editing, setEditing] = useState<ESME | "new" | null>(null);
  const [deleting, setDeleting] = useState<ESME | null>(null);
  // created is a new ESME whose password the server generated: it is shown once.
  const [created, setCreated] = useState<
    (CreatedESME & { password: string }) | null
  >(null);

  const esmes = useQuery({
    queryKey: ["esmes"],
    queryFn: listESMEs,
    refetchInterval: SMPP_REFRESH_MS,
  });

  const remove = useMutation({
    mutationFn: (esme: ESME) => deleteESME(esme.system_id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["esmes"] });
      showSnackbar("ESME deleted successfully.", "success");
      setDeleting(null);
    },
    onError: (error) => {
      setDeleting(null);
      showSnackbar(`Failed to delete ESME: ${errorMessage(error)}`, "error");
    },
  });

  return (
    <Box component="section" aria-labelledby="smpp-title">
      <PageHeader
        id="smpp-title"
        title="SMPP"
        count={esmes.data?.length}
        description="The ESMEs that bind to this SMSC over SMPP 3.4 to submit messages, and their sessions."
        action={
          editable && (
            <Button
              variant="contained"
              color="success"
              startIcon={<AddIcon />}
              onClick={() => setEditing("new")}
              sx={{ flexShrink: 0 }}
            >
              Add ESME
            </Button>
          )
        }
      />
      <QueryAlert query={esmes} subject="ESMEs" />
      {esmes.isPending && <Skeleton variant="rounded" height={120} />}
      {esmes.data && (
        <DataGrid<ESME>
          aria-label="ESMEs"
          rows={esmes.data}
          columns={columns(editable, setEditing, setDeleting)}
          getRowId={(esme) => esme.system_id}
          getRowHeight={() => "auto"}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          localeText={{ noRowsLabel: "No ESMEs." }}
          autoHeight
          sx={{
            ...GRID_SX,
            "& .MuiDataGrid-cell": {
              ...GRID_SX["& .MuiDataGrid-cell"],
              py: 1,
            },
          }}
        />
      )}
      {editing !== null && (
        <ESMEDialog
          esme={editing === "new" ? undefined : editing}
          onCreated={(esme) => {
            const { password } = esme;
            if (password) setCreated({ ...esme, password });
          }}
          onClose={() => setEditing(null)}
        />
      )}
      {created && (
        <ESMEPasswordDialog esme={created} onClose={() => setCreated(null)} />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete ESME"
          description={`Delete ${deleting.system_id}? Its open sessions are closed, and it can no longer bind.`}
          confirmLabel="Delete"
          pending={remove.isPending}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </Box>
  );
}
