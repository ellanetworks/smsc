import { Box, Chip, type ChipProps, Skeleton, Typography } from "@mui/material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import {
  type DiameterPeer,
  type DiameterPeerState,
  type DiameterStatus,
  getDiameterStatus,
} from "@/queries/diameter";
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
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

const gridSx = {
  width: "100%",
  border: 1,
  borderColor: "divider",
  bgcolor: "background.paper",
  "& .MuiDataGrid-cell": {
    borderBottom: "1px solid",
    borderColor: "divider",
  },
  "& .MuiDataGrid-columnHeaders": {
    borderBottom: "1px solid",
    borderColor: "divider",
  },
};

const hssPeers = (status: DiameterStatus): string[] =>
  status.routes.find((route) => route.application === "s6c")?.peers ?? [];

type CoreRow = DiameterPeer & { hss: boolean };

const columns: GridColDef<CoreRow>[] = [
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

export default function Cores() {
  const { data, error, isPending } = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterStatus,
    refetchInterval: DIAMETER_REFRESH_MS,
  });
  const hss = data ? hssPeers(data) : [];

  return (
    <Box component="section" aria-labelledby="cores-title">
      <PageHeader
        id="cores-title"
        title="Cores"
        description="Diameter connections between this SMSC and Ella Core."
      />
      <QueryAlert
        error={error}
        hasData={data !== undefined}
        subject="Diameter status"
      />
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
            help: "Your network's Diameter realm, shared by this SMSC and its cores. Set by the Operator ID (MCC/MNC).",
            value: data && <DomainName name={data.realm} />,
          },
        ]}
      />
      <Typography
        id="cores-table-title"
        variant="h6"
        component="h2"
        sx={{ mt: 4, mb: 2 }}
      >
        {data ? `Connected Cores (${data.peers.length})` : "Connected Cores"}
      </Typography>
      {isPending && <Skeleton variant="rounded" height={160} />}
      {data && (
        <DataGrid<CoreRow>
          aria-label="Cores"
          rows={data.peers.map((peer) => ({
            ...peer,
            hss: hss.includes(peer.host),
          }))}
          columns={columns}
          getRowId={(peer) => `${peer.host}|${peer.address}`}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          localeText={{ noRowsLabel: "No cores." }}
          autoHeight
          sx={gridSx}
        />
      )}
    </Box>
  );
}
