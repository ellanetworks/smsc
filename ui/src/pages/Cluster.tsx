import { useMemo, useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Chip,
  Link,
  Skeleton,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  OpenInNew as OpenInNewIcon,
} from "@mui/icons-material";
import {
  DataGrid,
  GridActionsCellItem,
  type GridColDef,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import {
  AddNodeDialog,
  DrainNodeDialog,
  RemoveNodeDialog,
  RenameNodeDialog,
  ResumeNodeDialog,
} from "@/components/ClusterDialogs";
import ClusterStateCard from "@/components/ClusterStateCard";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { useAuth } from "@/contexts/AuthContext";
import {
  getCluster,
  shortNodeId,
  type ClusterMember,
  type DrainState,
} from "@/queries/cluster";
import { CLUSTER_DOCS_URL } from "@/utils/docs";
import { formatTimestamp } from "@/utils/dates";
import { GRID_SX } from "@/utils/layout";

export const CLUSTER_REFRESH_MS = 5000;

type Action = "drain" | "resume" | "remove" | "rename";

function DrainStateChip({ state }: { state: DrainState }) {
  if (state === "draining") {
    return (
      <Chip label="Draining" size="small" color="warning" variant="outlined" />
    );
  }
  if (state === "drained") {
    return (
      <Chip label="Drained" size="small" color="error" variant="outlined" />
    );
  }
  return <Chip label="Active" size="small" variant="outlined" />;
}

function SuffrageChip({ suffrage }: { suffrage: ClusterMember["suffrage"] }) {
  const voter = suffrage === "voter";
  return (
    <Chip
      label={suffrage === "nonvoter" ? "Nonvoter" : voter ? "Voter" : "None"}
      size="small"
      color={voter ? "primary" : "warning"}
      variant={voter ? "filled" : "outlined"}
    />
  );
}

function HealthCell({ member }: { member: ClusterMember }) {
  return (
    <Stack direction="row" spacing={0.5} sx={{ alignItems: "center" }}>
      {member.healthy === undefined ? (
        <Tooltip title="The leader has not reported on this node yet." arrow>
          <Chip label="—" size="small" variant="outlined" />
        </Tooltip>
      ) : (
        <Chip
          label={member.healthy ? "Healthy" : "Unhealthy"}
          size="small"
          color={member.healthy ? "success" : "error"}
        />
      )}
      {member.diverged && (
        <Tooltip title="This node's database differs from the leader's." arrow>
          <Chip label="Diverged" size="small" color="error" />
        </Tooltip>
      )}
    </Stack>
  );
}

const columns = (
  selfId: string | undefined,
  manageable: boolean,
  act: (action: Action, member: ClusterMember) => void,
): GridColDef<ClusterMember>[] => [
  {
    field: "diameter_label",
    headerName: "Node",
    flex: 1.4,
    minWidth: 240,
    renderCell: ({ row }) => (
      <Stack
        direction="row"
        spacing={1}
        sx={{ alignItems: "center", height: "100%", minWidth: 0 }}
      >
        <Typography
          variant="body2"
          noWrap
          title={`${row.diameter_label} (${row.node_id})`}
          sx={{ fontWeight: row.leader ? 700 : 400, minWidth: 0 }}
        >
          {row.diameter_label || shortNodeId(row.node_id)}
          <Typography
            component="span"
            variant="caption"
            sx={{
              ml: 1,
              color: "text.secondary",
              fontFamily: "monospace",
              fontWeight: 400,
            }}
          >
            {shortNodeId(row.node_id)}
          </Typography>
        </Typography>
        {row.leader && (
          <Chip
            label="Leader"
            color="primary"
            size="small"
            sx={{ flexShrink: 0 }}
          />
        )}
        {row.node_id === selfId && (
          <Chip
            label="This node"
            size="small"
            variant="outlined"
            sx={{ flexShrink: 0 }}
          />
        )}
      </Stack>
    ),
  },
  {
    field: "address",
    headerName: "Cluster Address",
    flex: 0.8,
    minWidth: 160,
    valueGetter: (_value, row) => row.address || "—",
  },
  {
    field: "suffrage",
    headerName: "Suffrage",
    width: 110,
    renderCell: ({ row }) => <SuffrageChip suffrage={row.suffrage} />,
  },
  {
    field: "drain_state",
    headerName: "Drain",
    width: 105,
    renderCell: ({ row }) => <DrainStateChip state={row.drain_state} />,
  },
  {
    field: "healthy",
    headerName: "Health",
    width: 190,
    renderCell: ({ row }) => <HealthCell member={row} />,
  },
  {
    field: "applied_index",
    headerName: "Applied Index",
    width: 120,
    valueGetter: (_value, row) => row.applied_index ?? "—",
  },
  {
    field: "last_contact",
    headerName: "Last Contact",
    width: 120,
    valueGetter: (_value, row) => row.last_contact || "—",
  },
  {
    field: "version",
    headerName: "Version",
    width: 110,
    valueGetter: (_value, row) => row.version || "—",
  },
  {
    field: "joined_at",
    headerName: "Joined",
    width: 170,
    valueGetter: (_value, row) => formatTimestamp(row.joined_at),
  },
  ...(manageable
    ? [
        {
          field: "actions",
          headerName: "Actions",
          type: "actions",
          width: 80,
          getActions: ({ row }) => [
            <GridActionsCellItem
              key="rename"
              showInMenu
              label="Edit Diameter label"
              onClick={() => act("rename", row)}
            />,
            row.drain_state === "active" ? (
              <GridActionsCellItem
                key="drain"
                showInMenu
                label="Drain this node"
                onClick={() => act("drain", row)}
              />
            ) : (
              <GridActionsCellItem
                key="resume"
                showInMenu
                label="Resume this node"
                onClick={() => act("resume", row)}
              />
            ),
            <GridActionsCellItem
              key="remove"
              showInMenu
              label="Remove from cluster"
              onClick={() => act("remove", row)}
            />,
          ],
        } satisfies GridColDef<ClusterMember>,
      ]
    : []),
];

export default function Cluster() {
  const auth = useAuth();
  const [adding, setAdding] = useState(false);
  const [target, setTarget] = useState<{
    action: Action;
    member: ClusterMember;
  } | null>(null);

  const cluster = useQuery({
    queryKey: ["cluster"],
    queryFn: getCluster,
    refetchInterval: CLUSTER_REFRESH_MS,
  });
  const status = cluster.data;
  const members = useMemo(() => status?.members ?? [], [status]);
  const manageable = auth.can("cluster:manage") && status?.clustered === true;

  const close = () => setTarget(null);

  return (
    <Box component="section" aria-labelledby="cluster-title">
      <PageHeader
        id="cluster-title"
        title="Cluster"
        description="High-availability cluster members and health."
        action={
          manageable && (
            <Button
              variant="contained"
              color="success"
              startIcon={<AddIcon />}
              onClick={() => setAdding(true)}
              sx={{ flexShrink: 0 }}
            >
              Add Node
            </Button>
          )
        }
      />
      <QueryAlert query={cluster} subject="cluster status" />
      {cluster.isPending && <Skeleton variant="rounded" height={240} />}
      {status && !status.clustered && (
        <Alert severity="info">
          <AlertTitle>High availability is not enabled</AlertTitle>
          This node is running in standalone mode. Add a cluster block to its
          configuration file and restart it to enable clustering.{" "}
          <Link
            href={CLUSTER_DOCS_URL}
            target="_blank"
            rel="noreferrer"
            underline="hover"
            sx={{ display: "inline-flex", alignItems: "center" }}
          >
            Learn more
            <OpenInNewIcon sx={{ fontSize: 16, ml: 0.5 }} />
          </Link>
        </Alert>
      )}
      {status?.clustered && (
        <>
          <ClusterStateCard status={status} />
          <Typography
            id="cluster-nodes-title"
            variant="h6"
            component="h2"
            sx={{ mt: 4, mb: 2 }}
          >
            {`Nodes (${members.length})`}
          </Typography>
          <DataGrid<ClusterMember>
            aria-label="Nodes"
            rows={members}
            columns={columns(status.node_id, manageable, (action, member) =>
              setTarget({ action, member }),
            )}
            getRowId={(m) => m.node_id}
            disableColumnSorting
            disableColumnFilter
            disableColumnMenu
            disableRowSelectionOnClick
            disableVirtualization
            hideFooter
            localeText={{
              noRowsLabel: "No members.",
              actionsCellMore: "Node actions",
            }}
            autoHeight
            sx={GRID_SX}
          />
        </>
      )}
      {adding && <AddNodeDialog onClose={() => setAdding(false)} />}
      {target?.action === "rename" && (
        <RenameNodeDialog member={target.member} onClose={close} />
      )}
      {target?.action === "drain" && (
        <DrainNodeDialog
          member={target.member}
          isSelf={target.member.node_id === status?.node_id}
          isLastActive={
            !members.some(
              (m) =>
                m.node_id !== target.member.node_id &&
                m.drain_state === "active",
            )
          }
          onClose={close}
        />
      )}
      {target?.action === "resume" && (
        <ResumeNodeDialog member={target.member} onClose={close} />
      )}
      {target?.action === "remove" && (
        <RemoveNodeDialog
          member={target.member}
          isSelf={target.member.node_id === status?.node_id}
          onClose={close}
        />
      )}
    </Box>
  );
}
