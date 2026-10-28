import { useState } from "react";
import {
  Box,
  Button,
  Chip,
  type ChipProps,
  IconButton,
  Skeleton,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { GRID_SX } from "@/utils/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deletePeer,
  type DiameterPeer,
  type DiameterPeerState,
  type DiameterStatus,
  getDiameterStatus,
  listPeers,
  type Peer,
  type PeerRoute,
} from "@/queries/diameter";
import ConfirmDialog from "@/components/ConfirmDialog";
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import PeerDialog from "@/components/PeerDialog";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { formatTimestamp } from "@/utils/dates";

export const DIAMETER_REFRESH_MS = 5000;

const stateColor = (state: DiameterPeerState): ChipProps["color"] => {
  switch (state) {
    case "open":
      return "success";
    case "down":
      return "error";
    default:
      return "warning";
  }
};

const applicationLabels: Record<string, string> = {
  s6c: "S6c",
  sgd: "SGd",
};

const applicationLabel = (name: string) => applicationLabels[name] ?? name;

// The HSS route is the S6c route to this SMSC's own realm: a configured peer may
// also route S6c to another realm.
const hssPeers = (status: DiameterStatus): string[] =>
  status.routes.find(
    (route) =>
      route.application === "s6c" &&
      route.realm.toLowerCase() === status.realm.toLowerCase(),
  )?.peers ?? [];

// endpoint is an address and port, an IPv6 address in brackets.
const endpoint = (address: string, port: number) =>
  address.includes(":") ? `[${address}]:${port}` : `${address}:${port}`;

const routeLabel = (r: PeerRoute) =>
  `${applicationLabel(r.application)} → ${r.default ? "Default" : r.realm}` +
  (r.priority > 0 ? ` (priority ${r.priority})` : "");

function PeerStateChip({ peer }: { peer: Peer }) {
  const status = peer.status;
  const state = status?.state ?? "connecting";
  const tooltip =
    status?.state === "open"
      ? `Since ${formatTimestamp(status.since)}`
      : (status?.error ?? "");

  return (
    <Tooltip title={tooltip} arrow>
      <Chip label={state} color={stateColor(state)} size="small" />
    </Tooltip>
  );
}

type ConnectedRow = DiameterPeer & { hss: boolean };

const connectedColumns: GridColDef<ConnectedRow>[] = [
  {
    field: "host",
    headerName: "Host",
    flex: 1,
    minWidth: 180,
    renderCell: ({ row }) => (row.host ? <DomainName name={row.host} /> : "—"),
  },
  {
    field: "realm",
    headerName: "Realm",
    flex: 1,
    minWidth: 180,
    renderCell: ({ row }) =>
      row.realm ? <DomainName name={row.realm} /> : "—",
  },
  {
    field: "address",
    headerName: "Address",
    flex: 0.6,
    minWidth: 120,
    valueGetter: (_value, row) => row.address || "—",
  },
  {
    field: "state",
    headerName: "State",
    flex: 0.4,
    minWidth: 110,
    renderCell: ({ row }) => (
      <Chip label={row.state} color={stateColor(row.state)} size="small" />
    ),
  },
  {
    field: "hss",
    headerName: "HSS",
    flex: 0.3,
    minWidth: 80,
    valueGetter: (_value, row) => (row.hss ? "yes" : "no"),
  },
  {
    field: "applications",
    headerName: "Applications",
    flex: 0.5,
    minWidth: 120,
    valueGetter: (_value, row) =>
      row.applications.map(applicationLabel).join(", ") || "—",
  },
  {
    field: "since",
    headerName: "Since",
    flex: 0.6,
    minWidth: 170,
    valueGetter: (_value, row) => formatTimestamp(row.since),
  },
];

type ConfiguredRow = Peer & { hss: boolean };

const configuredColumns = (
  editable: boolean,
  onEdit: (peer: Peer) => void,
  onDelete: (peer: Peer) => void,
): GridColDef<ConfiguredRow>[] => [
  {
    field: "host",
    headerName: "Host",
    flex: 1,
    minWidth: 180,
    renderCell: ({ row }) => {
      const host = row.host || row.status?.host;
      return host ? <DomainName name={host} /> : "—";
    },
  },
  {
    field: "addresses",
    headerName: "Addresses",
    flex: 0.8,
    minWidth: 150,
    renderCell: ({ row }) => (
      <Stack>
        {row.addresses.map((a) => (
          <span key={a}>{endpoint(a, row.port)}</span>
        ))}
      </Stack>
    ),
  },
  {
    field: "routes",
    headerName: "Routes",
    flex: 1.2,
    minWidth: 220,
    renderCell: ({ row }) =>
      row.routes.length === 0 ? (
        "—"
      ) : (
        <Stack>
          {row.routes.map((r) => (
            <span key={routeLabel(r)}>{routeLabel(r)}</span>
          ))}
        </Stack>
      ),
  },
  {
    field: "state",
    headerName: "State",
    flex: 0.4,
    minWidth: 110,
    renderCell: ({ row }) => <PeerStateChip peer={row} />,
  },
  {
    field: "hss",
    headerName: "HSS",
    flex: 0.3,
    minWidth: 80,
    valueGetter: (_value, row) => (row.hss ? "yes" : "no"),
  },
  ...(editable
    ? [
        {
          field: "actions",
          headerName: "Actions",
          width: 100,
          align: "right",
          headerAlign: "right",
          renderCell: ({ row }) => {
            const name = row.host || row.addresses.join(", ");
            return (
              <>
                <Tooltip title="Edit peer" arrow>
                  <IconButton
                    size="small"
                    aria-label={`Edit ${name}`}
                    onClick={() => onEdit(row)}
                  >
                    <EditIcon fontSize="small" color="primary" />
                  </IconButton>
                </Tooltip>
                <Tooltip title="Delete peer" arrow>
                  <IconButton
                    size="small"
                    aria-label={`Delete ${name}`}
                    onClick={() => onDelete(row)}
                  >
                    <DeleteIcon fontSize="small" color="error" />
                  </IconButton>
                </Tooltip>
              </>
            );
          },
        } satisfies GridColDef<ConfiguredRow>,
      ]
    : []),
];

const sectionTitle = (title: string, count: number | undefined) =>
  count === undefined ? title : `${title} (${count})`;

export default function Diameter() {
  const auth = useAuth();
  const editable = auth.can("diameter:update");
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [editing, setEditing] = useState<Peer | "new" | null>(null);
  const [deleting, setDeleting] = useState<Peer | null>(null);

  const diameter = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterStatus,
    refetchInterval: DIAMETER_REFRESH_MS,
  });
  const configured = useQuery({
    queryKey: ["diameter", "peers"],
    queryFn: listPeers,
    refetchInterval: DIAMETER_REFRESH_MS,
  });
  const { data, isPending } = diameter;
  const hss = data ? hssPeers(data) : [];
  const connected = data?.peers.filter((p) => !p.configured);

  const remove = useMutation({
    mutationFn: (peer: Peer) => deletePeer(peer.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter"] });
      showSnackbar("Peer deleted successfully.", "success");
      setDeleting(null);
    },
    onError: (error) => {
      setDeleting(null);
      showSnackbar(`Failed to delete peer: ${errorMessage(error)}`, "error");
    },
  });

  return (
    <Box component="section" aria-labelledby="diameter-title">
      <PageHeader
        id="diameter-title"
        title="Diameter"
        description="This SMSC's Diameter identity, and its peers."
      />
      <QueryAlert query={diameter} subject="Diameter status" />
      <QueryAlert query={configured} subject="configured peers" />
      <SettingsTable
        label="Diameter settings"
        loading={data === undefined}
        rows={[
          {
            label: "Host",
            help: "This SMSC's Diameter name. Set by the Operator ID (MCC/MNC).",
            value: data && <DomainName name={data.host} />,
          },
          {
            label: "Realm",
            help: "Your network's Diameter realm, shared by this SMSC and its peers. Set by the Operator ID (MCC/MNC).",
            value: data && <DomainName name={data.realm} />,
          },
        ]}
      />
      <Stack
        direction="row"
        sx={{
          mt: 4,
          mb: 2,
          alignItems: "center",
          justifyContent: "space-between",
          gap: 2,
        }}
      >
        <Box>
          <Typography id="configured-peers-title" variant="h6" component="h2">
            {sectionTitle("Configured Peers", configured.data?.length)}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            The Diameter agents this SMSC connects to, such as an edge agent or
            a DRA in front of the HSS.
          </Typography>
        </Box>
        {editable && (
          <Button
            variant="contained"
            color="success"
            startIcon={<AddIcon />}
            onClick={() => setEditing("new")}
            sx={{ flexShrink: 0 }}
          >
            Add Peer
          </Button>
        )}
      </Stack>
      {configured.isPending && <Skeleton variant="rounded" height={120} />}
      {configured.data && (
        <DataGrid<ConfiguredRow>
          aria-label="Configured Peers"
          rows={configured.data.map((peer) => ({
            ...peer,
            hss:
              peer.status?.host !== undefined && hss.includes(peer.status.host),
          }))}
          columns={configuredColumns(editable, setEditing, setDeleting)}
          getRowHeight={() => "auto"}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          localeText={{ noRowsLabel: "No configured peers." }}
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
      <Box sx={{ mt: 4, mb: 2 }}>
        <Typography id="peers-title" variant="h6" component="h2">
          {sectionTitle("Connected Peers", connected?.length)}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          The HSSs, MMEs, SMSFs and IP-SM-GWs that connected to this SMSC.
        </Typography>
      </Box>
      {isPending && <Skeleton variant="rounded" height={160} />}
      {connected && (
        <DataGrid<ConnectedRow>
          aria-label="Connected Peers"
          rows={connected.map((peer) => ({
            ...peer,
            hss: hss.includes(peer.host),
          }))}
          columns={connectedColumns}
          getRowId={(peer) => `${peer.host}|${peer.address}`}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          localeText={{ noRowsLabel: "No connected peers." }}
          autoHeight
          sx={GRID_SX}
        />
      )}
      {editing !== null && (
        <PeerDialog
          peer={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete Peer"
          description={`Delete ${deleting.host || deleting.addresses.join(", ")}? This SMSC disconnects from it, and stops sending it the requests of its routes.`}
          confirmLabel="Delete"
          pending={remove.isPending}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </Box>
  );
}
