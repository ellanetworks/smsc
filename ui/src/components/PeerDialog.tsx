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
import { Add as AddIcon, Delete as DeleteIcon } from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  createPeer,
  DEFAULT_DIAMETER_PORT,
  type Peer,
  type PeerApplication,
  type PeerParams,
  updatePeer,
} from "@/queries/diameter";

const LABEL = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;

// isDiameterIdentity is the server's rule for a host or a realm: an FQDN (RFC 6733 clause 4.3.1).
export const isDiameterIdentity = (s: string): boolean =>
  s.length > 0 && s.length <= 253 && s.split(".").every((l) => LABEL.test(l));

const IPV4 =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

// isIPAddress accepts an IPv4 address, or what looks like an IPv6 one: the server has the last word.
const isIPAddress = (s: string): boolean =>
  IPV4.test(s) || (s.includes(":") && /^[0-9A-Fa-f:.]+$/.test(s));

const splitAddresses = (s: string): string[] =>
  s
    .split(/[\s,]+/)
    .map((a) => a.trim())
    .filter(Boolean);

// RouteDraft is a route as edited: its realm is kept while it is a default route, in case it goes back.
interface RouteDraft {
  key: number;
  application: PeerApplication;
  isDefault: boolean;
  realm: string;
  priority: string;
}

interface RouteErrors {
  destination?: string;
  realm?: string;
  priority?: string;
}

// routeErrors are why a route is not valid. A realm not typed yet is invalid, but not shown as an error.
const routeErrors = (r: RouteDraft, routes: RouteDraft[]) => {
  const errors: RouteErrors = {};
  let valid = true;

  if (!r.isDefault && !isDiameterIdentity(r.realm)) {
    valid = false;
    if (r.realm !== "") errors.realm = "Not a realm.";
  }

  if (!/^\d+$/.test(r.priority)) {
    valid = false;
    errors.priority = "0 or more.";
  }

  const twice =
    routes.filter(
      (o) =>
        o.application === r.application &&
        o.isDefault === r.isDefault &&
        (r.isDefault || o.realm.toLowerCase() === r.realm.toLowerCase()),
    ).length > 1;
  if (twice && (r.isDefault || r.realm !== "")) {
    valid = false;
    if (r.isDefault) errors.destination = "Listed twice.";
    else errors.realm = "Listed twice.";
  }

  return { valid, errors };
};

const toDraft = (peer: Peer | undefined): RouteDraft[] =>
  (peer?.routes ?? []).map((r, key) => ({
    key,
    application: r.application,
    isDefault: r.default === true,
    realm: r.realm ?? "",
    priority: String(r.priority),
  }));

export default function PeerDialog({
  peer,
  onClose,
}: {
  // peer is the peer to edit, or undefined to add one.
  peer?: Peer;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [host, setHost] = useState(peer?.host ?? "");
  const [addresses, setAddresses] = useState(
    (peer?.addresses ?? []).join(", "),
  );
  const [port, setPort] = useState(String(peer?.port ?? DEFAULT_DIAMETER_PORT));
  const [routes, setRoutes] = useState<RouteDraft[]>(toDraft(peer));
  const [nextKey, setNextKey] = useState(routes.length);

  const params = (): PeerParams => ({
    host,
    addresses: splitAddresses(addresses),
    port: Number(port),
    routes: routes.map((r) => ({
      ...(r.isDefault ? { default: true } : { realm: r.realm }),
      application: r.application,
      priority: Number(r.priority),
    })),
  });

  const mutation = useMutation({
    mutationFn: () =>
      peer ? updatePeer(peer.id, params()) : createPeer(params()),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter"] });
      showSnackbar(
        peer ? "Peer updated successfully." : "Peer added successfully.",
        "success",
      );
      onClose();
    },
  });

  const update = (key: number, change: Partial<RouteDraft>) =>
    setRoutes((rs) => rs.map((r) => (r.key === key ? { ...r, ...change } : r)));

  const addRoute = () => {
    setRoutes((rs) => [
      ...rs,
      {
        key: nextKey,
        application: "sgd",
        isDefault: !rs.some((r) => r.isDefault),
        realm: "",
        priority: "0",
      },
    ]);
    setNextKey((k) => k + 1);
  };

  const hostError =
    host === "" || isDiameterIdentity(host)
      ? undefined
      : "Enter a Diameter host, such as dea1.ipx.example.net.";
  const addressList = splitAddresses(addresses);
  const badAddress = addressList.find((a) => !isIPAddress(a));
  const addressError =
    badAddress !== undefined
      ? `${badAddress} is not an IP address.`
      : addresses !== "" && addressList.length === 0
        ? "Enter at least one address."
        : undefined;
  const portNumber = Number(port);
  const portError =
    /^\d+$/.test(port) && portNumber >= 1 && portNumber <= 65535
      ? undefined
      : "1 to 65535.";
  const checked = routes.map((r) => routeErrors(r, routes));

  const valid =
    hostError === undefined &&
    addressList.length > 0 &&
    addressError === undefined &&
    portError === undefined &&
    checked.every((c) => c.valid);

  return (
    <EditDialog
      title={peer ? "Edit Peer" : "Add Peer"}
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel={peer ? "Update" : "Add"}
      pendingLabel={peer ? "Updating…" : "Adding…"}
      failure={peer ? "Could not update the peer" : "Could not add the peer"}
    >
      <TextField
        label="Host"
        value={host}
        onChange={(e) => setHost(e.target.value.trim())}
        error={hostError !== undefined}
        helperText={
          hostError ??
          "The peer's Diameter identity. When set, the peer must give it. Optional."
        }
        placeholder="dea1.ipx.example.net"
        autoFocus
      />
      <TextField
        label="Addresses"
        value={addresses}
        onChange={(e) => setAddresses(e.target.value)}
        error={addressError !== undefined}
        helperText={
          addressError ?? "Every SCTP address of the peer, comma-separated."
        }
        placeholder="192.0.2.10, 198.51.100.10"
        required
      />
      <TextField
        label="Port"
        value={port}
        onChange={(e) => setPort(e.target.value.trim())}
        error={portError !== undefined}
        helperText={portError}
        inputMode="numeric"
        required
      />
      <Box>
        <Typography variant="subtitle2">Routes</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
          The requests this SMSC sends the peer. Lower priorities are tried
          first.
        </Typography>
        <Stack spacing={2}>
          {routes.map((r, i) => {
            const { errors } = checked[i];

            return (
              <Stack
                key={r.key}
                component="fieldset"
                aria-label={`Route ${i + 1}`}
                spacing={1.5}
                sx={{
                  border: 1,
                  borderColor: "divider",
                  borderRadius: 1,
                  m: 0,
                  p: 1.5,
                  minWidth: 0,
                }}
              >
                <Stack
                  direction={{ xs: "column", sm: "row" }}
                  spacing={1}
                  sx={{ alignItems: { sm: "flex-start" } }}
                >
                  <TextField
                    select
                    label="Application"
                    value={r.application}
                    onChange={(e) => {
                      const application = e.target.value as PeerApplication;
                      // Only SGd takes a default route: S6c goes to this SMSC's own realm.
                      update(r.key, {
                        application,
                        isDefault: application === "sgd" && r.isDefault,
                      });
                    }}
                    sx={{ flex: 1 }}
                  >
                    <MenuItem value="sgd">SGd</MenuItem>
                    <MenuItem value="s6c">S6c</MenuItem>
                  </TextField>
                  <TextField
                    select
                    label="Destination"
                    value={r.isDefault ? "default" : "realm"}
                    onChange={(e) =>
                      update(r.key, { isDefault: e.target.value === "default" })
                    }
                    error={errors.destination !== undefined}
                    helperText={errors.destination}
                    sx={{ flex: 1 }}
                  >
                    <MenuItem value="realm">Realm</MenuItem>
                    <MenuItem
                      value="default"
                      disabled={r.application !== "sgd"}
                    >
                      Default
                    </MenuItem>
                  </TextField>
                  <TextField
                    label="Priority"
                    value={r.priority}
                    onChange={(e) =>
                      update(r.key, { priority: e.target.value.trim() })
                    }
                    error={errors.priority !== undefined}
                    helperText={errors.priority}
                    inputMode="numeric"
                    sx={{ width: { sm: 100 } }}
                  />
                  <Tooltip title="Remove route" arrow>
                    <IconButton
                      aria-label={`Remove route ${i + 1}`}
                      onClick={() =>
                        setRoutes((rs) => rs.filter((o) => o.key !== r.key))
                      }
                      sx={{ alignSelf: { xs: "flex-end", sm: "center" } }}
                    >
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                </Stack>
                {r.isDefault ? (
                  <Typography variant="body2" color="text.secondary">
                    Every realm without another route, except this SMSC&apos;s
                    own.
                  </Typography>
                ) : (
                  <TextField
                    label="Realm"
                    value={r.realm}
                    onChange={(e) =>
                      update(r.key, { realm: e.target.value.trim() })
                    }
                    error={errors.realm !== undefined}
                    helperText={errors.realm}
                    placeholder="epc.mnc002.mcc208.3gppnetwork.org"
                    required
                  />
                )}
              </Stack>
            );
          })}
          <Box>
            <Button startIcon={<AddIcon />} onClick={addRoute}>
              Add Route
            </Button>
          </Box>
        </Stack>
      </Box>
    </EditDialog>
  );
}
