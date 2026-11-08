import type { ReactNode } from "react";
import {
  Box,
  Card,
  type CardProps,
  CardActionArea,
  CardContent,
  CardHeader,
  Chip,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import Grid from "@mui/material/Grid";
import { useQuery } from "@tanstack/react-query";
import { Link as RouterLink, useNavigate } from "react-router-dom";
import PageHeader from "@/components/PageHeader";
import { MessageStatusChip, messageStatusColors } from "@/components/chips";
import { useAuth } from "@/contexts/AuthContext";
import { getCluster, type ClusterStatus } from "@/queries/cluster";
import {
  type DiameterPeer,
  getDiameterStatus,
  hssPeers,
  listPeers,
} from "@/queries/diameter";
import { getMessageCounts, type MessageStatus } from "@/queries/messages";
import { getMetrics, sumOf } from "@/queries/metrics";
import { getOIDCLogin } from "@/queries/oidc";
import { getOperator } from "@/queries/operator";
import { type ESME, listESMEs } from "@/queries/smpp";
import { formatDateTime } from "@/utils/dates";

export const DASHBOARD_REFRESH_MS = 5000;

export const COUNTS_REFRESH_MS = 30_000;

const DAY_MS = 24 * 60 * 60 * 1000;

const ALWAYS_SHOWN: MessageStatus[] = [
  "delivered",
  "pending",
  "failed",
  "expired",
];

const nf = new Intl.NumberFormat();

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"];

const sizeFormat = new Intl.NumberFormat(undefined, {
  maximumFractionDigits: 2,
});

const formatBytes = (bytes: number): string => {
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < BYTE_UNITS.length - 1) {
    n /= 1024;
    i++;
  }
  return `${sizeFormat.format(n)} ${BYTE_UNITS[i]}`;
};

const lastDayLink = (status?: MessageStatus) =>
  `/messages?${status ? `status=${status}&` : ""}range=24h`;

const headerSx = {
  backgroundColor: "backgroundSubtle",
  borderTopLeftRadius: 8,
  borderTopRightRadius: 8,
  "& .MuiCardHeader-title": { fontWeight: 600, fontSize: "1rem" },
};

const titleLinkSx = {
  textDecoration: "none",
  color: "inherit",
  "&:hover": { textDecoration: "underline" },
};

const headSx = { fontWeight: 600, whiteSpace: "nowrap" };

interface PanelProps extends Omit<CardProps, "title"> {
  title: string;
  to?: string;
  loading?: boolean;
  error?: boolean;
  minHeight?: number;
  alignTop?: boolean;
  children?: ReactNode;
}

function Panel({
  title,
  to,
  loading,
  error,
  minHeight = 160,
  alignTop,
  children,
  ...cardProps
}: PanelProps) {
  return (
    <Card
      {...cardProps}
      sx={{
        height: "100%",
        display: "flex",
        flexDirection: "column",
        borderRadius: 3,
        boxShadow: 2,
      }}
    >
      <CardHeader
        title={
          to ? (
            <Box component={RouterLink} to={to} sx={titleLinkSx}>
              {title}
            </Box>
          ) : (
            title
          )
        }
        slotProps={{ title: { component: "h3" } }}
        sx={headerSx}
      />
      <CardContent
        sx={{
          flexGrow: 1,
          display: "flex",
          alignItems: alignTop ? "flex-start" : "center",
          justifyContent: alignTop ? "flex-start" : "center",
          minHeight,
        }}
      >
        {loading ? (
          <Skeleton
            variant="rounded"
            width={alignTop ? "100%" : 120}
            height={alignTop ? minHeight - 32 : 40}
          />
        ) : error ? (
          <Typography variant="body2" color="error">
            Failed to load data.
          </Typography>
        ) : (
          children
        )}
      </CardContent>
    </Card>
  );
}

interface KpiCardProps {
  title: string;
  hint: string;
  value?: ReactNode;
  caption?: ReactNode;
  loading?: boolean;
  error?: boolean;
  to?: string;
}

function KpiCard({ title, hint, value, caption, to, ...state }: KpiCardProps) {
  const content = (
    <>
      <Box sx={{ textAlign: "center" }}>
        {typeof value === "string" ? (
          <Typography variant="h4">{value}</Typography>
        ) : (
          value
        )}
        {caption && (
          <Typography variant="body2" color="textSecondary" sx={{ mt: 1 }}>
            {caption}
          </Typography>
        )}
      </Box>
    </>
  );

  if (to) {
    return (
      <Tooltip title={hint} arrow describeChild>
        <CardActionArea
          component={RouterLink}
          to={to}
          sx={{ height: "100%", borderRadius: 3 }}
        >
          <Panel title={title} {...state}>
            {content}
          </Panel>
        </CardActionArea>
      </Tooltip>
    );
  }

  return (
    <Tooltip title={hint} arrow describeChild>
      <Panel title={title} tabIndex={0} {...state}>
        {content}
      </Panel>
    </Tooltip>
  );
}

function Section({
  title,
  first,
  children,
}: {
  title: string;
  first?: boolean;
  children: ReactNode;
}) {
  return (
    <Box component="section" aria-label={title}>
      <Typography variant="h5" component="h2" sx={{ mt: first ? 0 : 4, mb: 2 }}>
        {title}
      </Typography>
      <Grid container spacing={4} sx={{ alignItems: "stretch" }}>
        {children}
      </Grid>
    </Box>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box>
      <Typography variant="body2" color="textSecondary">
        {label}
      </Typography>
      <Typography variant="body1" sx={{ overflowWrap: "anywhere" }}>
        {children}
      </Typography>
    </Box>
  );
}

const QUARTER = { xs: 12, sm: 6, md: 3 };

const HA_HINTS = {
  off: "High availability is not enabled on this node.",
  healthy: "Healthy voters out of the cluster's total.",
  degraded: "At least one node is unhealthy.",
  noLeader:
    "No node holds leadership. The cluster cannot accept writes until enough voters recover.",
  unknown: "Cluster health could not be read from this node.",
};

function clusterHealth(status?: ClusterStatus): {
  hint: string;
  value: ReactNode;
} {
  if (!status) {
    return { hint: HA_HINTS.unknown, value: <Chip label="Unknown" /> };
  }
  if (!status.clustered) {
    return { hint: HA_HINTS.off, value: <Chip label="Off" /> };
  }
  if (!status.leader_id) {
    return {
      hint: HA_HINTS.noLeader,
      value: <Chip label="No leader" color="error" />,
    };
  }
  if (status.healthy === undefined) {
    return { hint: HA_HINTS.unknown, value: <Chip label="Unknown" /> };
  }
  const voters = (status.members ?? []).filter((m) => m.suffrage === "voter");
  const healthy = voters.filter((m) => m.healthy).length;
  return {
    hint: status.healthy ? HA_HINTS.healthy : HA_HINTS.degraded,
    value: `${healthy}/${voters.length}`,
  };
}

function Ratio({
  value,
  color,
}: {
  value: string;
  color?: "warning.main" | "error.main";
}) {
  return (
    <Typography variant="h4" sx={{ color }}>
      {value}
    </Typography>
  );
}

function peersHealth(
  peers: DiameterPeer[],
  hss: string[],
  nameOf: (peer: DiameterPeer) => string,
): { hint: string; value: ReactNode; caption?: string } {
  if (peers.length === 0) {
    return { hint: "No Diameter peers are configured.", value: "None" };
  }
  const notOpen = peers.filter((p) => p.state !== "open");
  const hssOpen = peers.some((p) => p.state === "open" && hss.includes(p.host));
  const color =
    notOpen.length === 0
      ? undefined
      : notOpen.length === peers.length || (hss.length > 0 && !hssOpen)
        ? "error.main"
        : "warning.main";
  const hssCaption =
    hss.length === 0 ? "No HSS route" : hssOpen ? "HSS open" : "HSS down";
  return {
    hint:
      notOpen.length === 0
        ? "Peers with an open connection out of all peers."
        : `Not open: ${notOpen.map(nameOf).join(", ")}.`,
    value: (
      <Ratio
        value={`${peers.length - notOpen.length}/${peers.length}`}
        color={color}
      />
    ),
    caption: `Open · ${hssCaption}`,
  };
}

function esmesHealth(esmes: ESME[]): {
  hint: string;
  value: ReactNode;
  caption?: string;
} {
  if (esmes.length === 0) {
    return { hint: "No ESMEs are configured.", value: "None" };
  }
  const bound = esmes.filter((e) => e.sessions.length > 0).length;
  const sessions = esmes.reduce((n, e) => n + e.sessions.length, 0);
  return {
    hint: "ESMEs with at least one bound session out of all ESMEs.",
    value: <Ratio value={`${bound}/${esmes.length}`} />,
    caption: `Bound · ${nf.format(sessions)} ${sessions === 1 ? "session" : "sessions"}`,
  };
}

export default function Dashboard() {
  const auth = useAuth();
  const navigate = useNavigate();

  const operator = useQuery({ queryKey: ["operator"], queryFn: getOperator });
  const sso = useQuery({ queryKey: ["oidcLogin"], queryFn: getOIDCLogin });
  const diameter = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterStatus,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const esmes = useQuery({
    queryKey: ["esmes"],
    queryFn: listESMEs,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const metrics = useQuery({
    queryKey: ["metrics"],
    queryFn: getMetrics,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const counts = useQuery({
    queryKey: ["messages", "counts", "lastDay"],
    queryFn: () =>
      getMessageCounts({ start: new Date(Date.now() - DAY_MS).toISOString() }),
    refetchInterval: COUNTS_REFRESH_MS,
  });
  const cluster = useQuery({
    queryKey: ["cluster"],
    queryFn: getCluster,
    refetchInterval: DASHBOARD_REFRESH_MS,
    enabled: auth.can("cluster:read"),
  });
  const configured = useQuery({
    queryKey: ["diameter", "peers"],
    queryFn: listPeers,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });

  const peers = diameter.data?.peers ?? [];
  const hss = diameter.data ? hssPeers(diameter.data) : [];
  const addressesOf = new Map(
    (configured.data ?? []).map((p) => [p.id, p.addresses.join(", ")]),
  );
  const peerName = (peer: DiameterPeer) =>
    peer.host ||
    peer.address ||
    (peer.id !== undefined && addressesOf.get(peer.id)) ||
    "unknown peer";
  const diameterHealth = peersHealth(peers, hss, peerName);
  const smppHealth = esmesHealth(esmes.data ?? []);

  const samples = metrics.data ?? [];
  const due = sumOf(samples, "ellasmsc_messages_pending", { state: "due" });
  const waiting = sumOf(samples, "ellasmsc_messages_pending", {
    state: "waiting",
  });
  const memory = sumOf(samples, "process_resident_memory_bytes");
  const database = sumOf(samples, "ellasmsc_database_storage_bytes");
  const startedAt = sumOf(samples, "process_start_time_seconds");

  const byStatus = counts.data;
  const statuses: MessageStatus[] = byStatus
    ? [...ALWAYS_SHOWN, ...(byStatus.replaced > 0 ? ["replaced" as const] : [])]
    : [];
  const total = byStatus
    ? Object.values(byStatus).reduce((sum, n) => sum + n, 0)
    : 0;

  const failedToLoad = (q: { error: Error | null; data: unknown }) =>
    !!q.error && q.data === undefined;
  const metricCard = {
    loading: metrics.isPending,
    error: failedToLoad(metrics),
  };

  return (
    <Box component="section" aria-labelledby="dashboard-title">
      <PageHeader id="dashboard-title" title="Dashboard" />

      <Section title="Overview" first>
        <Grid size={QUARTER}>
          <Panel
            title="This SMSC"
            to="/operator"
            loading={operator.isPending}
            error={failedToLoad(operator)}
            alignTop
          >
            <Box sx={{ display: "grid", gap: 2 }}>
              <Field label="Operator ID (MCC/MNC)">
                {operator.data && `${operator.data.mcc} / ${operator.data.mnc}`}
              </Field>
              <Field label="Service Centre Address">
                {operator.data?.service_centre_address}
              </Field>
            </Box>
          </Panel>
        </Grid>
        <Grid size={QUARTER}>
          <KpiCard
            title="Diameter Peers"
            hint={diameterHealth.hint}
            loading={diameter.isPending}
            error={failedToLoad(diameter)}
            value={diameterHealth.value}
            caption={diameterHealth.caption}
            to="/diameter"
          />
        </Grid>
        <Grid size={QUARTER}>
          <KpiCard
            title="SMPP"
            hint={smppHealth.hint}
            loading={esmes.isPending}
            error={failedToLoad(esmes)}
            value={smppHealth.value}
            caption={smppHealth.caption}
            to="/smpp"
          />
        </Grid>
        {auth.can("cluster:read") && (
          <Grid size={QUARTER}>
            <KpiCard
              title="High Availability"
              hint={clusterHealth(cluster.data).hint}
              loading={cluster.isPending}
              error={failedToLoad(cluster)}
              value={clusterHealth(cluster.data).value}
              to="/cluster"
            />
          </Grid>
        )}
      </Section>

      <Section title="Messages">
        <Grid size={{ xs: 12, md: 8 }}>
          <Panel
            title="Last 24 Hours"
            to={lastDayLink()}
            loading={counts.isPending}
            error={failedToLoad(counts)}
            alignTop
          >
            <TableContainer>
              <Table size="small" aria-label="Messages in the last 24 hours">
                <TableHead>
                  <TableRow>
                    <TableCell sx={headSx}>Status</TableCell>
                    <TableCell sx={headSx} align="right">
                      Messages
                    </TableCell>
                    <TableCell sx={{ ...headSx, width: "50%" }}>
                      Share
                    </TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {byStatus &&
                    statuses.map((status) => {
                      const share = total
                        ? (100 * byStatus[status]) / total
                        : 0;
                      const color = messageStatusColors[status];
                      return (
                        <TableRow
                          key={status}
                          hover
                          onClick={() => navigate(lastDayLink(status))}
                          sx={{ cursor: "pointer" }}
                        >
                          <TableCell>
                            <Box
                              component={RouterLink}
                              to={lastDayLink(status)}
                              onClick={(e) => e.stopPropagation()}
                              sx={{ textDecoration: "none" }}
                            >
                              <MessageStatusChip status={status} />
                            </Box>
                          </TableCell>
                          <TableCell align="right">
                            {nf.format(byStatus[status])}
                          </TableCell>
                          <TableCell>
                            <Box
                              sx={{
                                display: "flex",
                                alignItems: "center",
                                gap: 1,
                              }}
                            >
                              <Box
                                aria-hidden
                                sx={{
                                  flexGrow: 1,
                                  height: 8,
                                  borderRadius: 4,
                                  bgcolor: "action.hover",
                                  overflow: "hidden",
                                }}
                              >
                                <Box
                                  sx={{
                                    width: `${share}%`,
                                    height: "100%",
                                    bgcolor:
                                      color === "default"
                                        ? "grey.500"
                                        : `${color}.main`,
                                  }}
                                />
                              </Box>
                              <Typography
                                variant="body2"
                                color="textSecondary"
                                sx={{ minWidth: 40, textAlign: "right" }}
                              >
                                {Math.round(share)}%
                              </Typography>
                            </Box>
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  <TableRow>
                    <TableCell sx={{ fontWeight: 600 }}>Total</TableCell>
                    <TableCell align="right" sx={{ fontWeight: 600 }}>
                      {nf.format(total)}
                    </TableCell>
                    <TableCell />
                  </TableRow>
                </TableBody>
              </Table>
            </TableContainer>
          </Panel>
        </Grid>
        <Grid size={{ xs: 12, md: 4 }}>
          <Tooltip
            title="Due: being delivered now; a growing number means delivery is falling behind. Waiting: held for a retry or until the phone is reachable."
            arrow
            describeChild
          >
            <Panel title="Delivery Queue" tabIndex={0} {...metricCard}>
              <Box
                sx={{
                  display: "flex",
                  justifyContent: "space-around",
                  width: "100%",
                  textAlign: "center",
                }}
              >
                <Box>
                  <Typography variant="h4">{nf.format(due ?? 0)}</Typography>
                  <Typography variant="body2" color="textSecondary">
                    Due
                  </Typography>
                </Box>
                <Box>
                  <Typography variant="h4">
                    {nf.format(waiting ?? 0)}
                  </Typography>
                  <Typography variant="body2" color="textSecondary">
                    Waiting
                  </Typography>
                </Box>
              </Box>
            </Panel>
          </Tooltip>
        </Grid>
      </Section>

      <Section title="System">
        <Grid size={QUARTER}>
          <KpiCard
            title="Process Memory"
            hint="RAM used by the SMSC process (resident set size): what the operating system and a container memory limit count."
            {...metricCard}
            value={memory === undefined ? "N/A" : formatBytes(memory)}
          />
        </Grid>
        <Grid size={QUARTER}>
          <KpiCard
            title="Database Size"
            hint="Size of the database on disk, its write-ahead log included."
            {...metricCard}
            value={database === undefined ? "N/A" : formatBytes(database)}
          />
        </Grid>
        <Grid size={QUARTER}>
          <KpiCard
            title="Single Sign-On"
            hint="Whether users can log in with your identity provider over OpenID Connect."
            loading={sso.isPending}
            error={failedToLoad(sso)}
            value={
              sso.data?.enabled ? (
                <Chip label="On" color="success" />
              ) : (
                <Chip label="Off" />
              )
            }
            caption={sso.data?.enabled ? sso.data.display_name : undefined}
            to={auth.can("oidc:read") ? "/single-sign-on" : undefined}
          />
        </Grid>
        <Grid size={QUARTER}>
          <KpiCard
            title="Up Since"
            hint="Time when the SMSC process was started."
            {...metricCard}
            value={
              startedAt === undefined
                ? "N/A"
                : formatDateTime(new Date(startedAt * 1000).toISOString())
            }
          />
        </Grid>
      </Section>
    </Box>
  );
}
