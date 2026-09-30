import {
  Box,
  Card,
  CardContent,
  CardHeader,
  Chip,
  type ChipProps,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import { type DiameterPeerState, getDiameterStatus } from "@/queries/diameter";
import DomainName from "@/components/DomainName";
import Fields from "@/components/Fields";
import QueryAlert from "@/components/QueryAlert";
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

export default function DiameterStatusCard() {
  const { data, error, isPending } = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterStatus,
    refetchInterval: DIAMETER_REFRESH_MS,
  });

  return (
    <Card
      component="section"
      aria-labelledby="diameter-title"
      variant="outlined"
    >
      <CardHeader
        title="Diameter"
        slotProps={{ title: { id: "diameter-title", component: "h2" } }}
      />
      <CardContent>
        <QueryAlert
          error={error}
          hasData={data !== undefined}
          subject="Diameter status"
        />
        {isPending && <Skeleton variant="rounded" height={80} />}
        {data && (
          <Fields
            rows={[
              ["Host", data.host],
              ["Realm", data.realm],
              [
                "HSS",
                <Chip
                  key="hss"
                  label={data.hss_available ? "reachable" : "unreachable"}
                  color={data.hss_available ? "success" : "error"}
                  size="small"
                />,
              ],
            ]}
          />
        )}
        {data && (
          <Typography
            id="diameter-peers-title"
            variant="subtitle1"
            component="h3"
            sx={{ mt: 2, mb: 1 }}
          >
            Peers
          </Typography>
        )}
        {data && data.peers.length === 0 && (
          <Typography color="text.secondary">No peers.</Typography>
        )}
        {data && data.peers.length > 0 && (
          <TableContainer>
            <Table size="small" aria-labelledby="diameter-peers-title">
              <TableHead>
                <TableRow>
                  <TableCell>Peer</TableCell>
                  <TableCell>Address</TableCell>
                  <TableCell>State</TableCell>
                  <TableCell>Applications</TableCell>
                  <TableCell>Since</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {data.peers.map((peer) => (
                  <TableRow key={`${peer.host}|${peer.address}`}>
                    <TableCell>
                      <Box data-field="host">
                        {peer.host ? <DomainName name={peer.host} /> : "—"}
                      </Box>
                      {peer.realm && (
                        <Typography
                          data-field="realm"
                          variant="caption"
                          color="text.secondary"
                          sx={{ display: "block" }}
                        >
                          <DomainName name={peer.realm} />
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell>{peer.address || "—"}</TableCell>
                    <TableCell>
                      <Chip
                        label={peer.state}
                        color={stateColor(peer.state)}
                        size="small"
                      />
                    </TableCell>
                    <TableCell>
                      {peer.applications.map(applicationLabel).join(", ") ||
                        "—"}
                    </TableCell>
                    <TableCell>{formatTimestamp(peer.since)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </CardContent>
    </Card>
  );
}
