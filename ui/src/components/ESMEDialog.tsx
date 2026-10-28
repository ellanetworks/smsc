import { useState } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  MenuItem,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { ContentCopy as ContentCopyIcon } from "@mui/icons-material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import PasswordField from "@/components/PasswordField";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
  createESME,
  type CreatedESME,
  type ESME,
  type ESMEUpdate,
  MAX_PASSWORD_LENGTH,
  MAX_SYSTEM_ID_LENGTH,
  updateESME,
} from "@/queries/smpp";
import { listUsers, RoleID, roleLabels } from "@/queries/users";

const PRINTABLE = /^[\x20-\x7E]*$/;

// systemIDError is why s is not a system ID: 1 to 15 printable ASCII characters.
export const systemIDError = (s: string): string | undefined =>
  s.length >= 1 && s.length <= MAX_SYSTEM_ID_LENGTH && PRINTABLE.test(s)
    ? undefined
    : `1 to ${MAX_SYSTEM_ID_LENGTH} printable ASCII characters.`;

// passwordError is why s is not an SMPP password: 1 to 8 printable ASCII characters.
export const passwordError = (s: string): string | undefined =>
  s.length >= 1 && s.length <= MAX_PASSWORD_LENGTH && PRINTABLE.test(s)
    ? undefined
    : `1 to ${MAX_PASSWORD_LENGTH} printable ASCII characters.`;

const IPV4 =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

const isIPv6 = (s: string): boolean => {
  if (!/^[0-9A-Fa-f:.]+$/.test(s)) return false;
  const halves = s.split("::");
  if (halves.length > 2) return false;
  const groups = halves.flatMap((h) => (h === "" ? [] : h.split(":")));
  // An embedded IPv4 address, last, counts as two groups.
  const last = groups.at(-1);
  const v4 = last !== undefined && last.includes(".");
  if (v4 && !IPV4.test(last)) return false;
  const hex = v4 ? groups.slice(0, -1) : groups;
  if (!hex.every((g) => /^[0-9A-Fa-f]{1,4}$/.test(g))) return false;
  const count = hex.length + (v4 ? 2 : 0);
  return halves.length === 2 ? count <= 7 : count === 8;
};

// isPrefix accepts an IPv4 or an IPv6 prefix, such as 10.1.0.0/24 or 2001:db8::/32: the server has the last word.
export const isPrefix = (s: string): boolean => {
  const slash = s.lastIndexOf("/");
  if (slash < 0) return false;
  const address = s.slice(0, slash);
  const bits = s.slice(slash + 1);
  if (!/^\d{1,3}$/.test(bits)) return false;
  const n = Number(bits);
  if (IPV4.test(address)) return n <= 32;
  return isIPv6(address) && n <= 128;
};

const splitNetworks = (s: string): string[] =>
  s
    .split(/[\s,]+/)
    .map((a) => a.trim())
    .filter(Boolean);

export default function ESMEDialog({
  esme,
  onCreated,
  onClose,
}: {
  // esme is the ESME to edit, or undefined to add one.
  esme?: ESME;
  // onCreated is given the new ESME, with the password the server generated, if it did.
  onCreated?: (esme: CreatedESME) => void;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  // The users an ESME can belong to; listing them needs the same permission as adding an ESME.
  const users = useQuery({
    queryKey: ["users", { page: 1, perPage: 100 }],
    queryFn: () => listUsers({ page: 1, perPage: 100 }),
    enabled: esme === undefined,
  });
  const { showSnackbar } = useSnackbar();
  const [systemID, setSystemID] = useState(esme?.system_id ?? "");
  const [user, setUser] = useState(esme?.user ?? "");
  const [password, setPassword] = useState("");
  const [networks, setNetworks] = useState(
    (esme?.allowed_networks ?? []).join(", "),
  );

  const params = (): ESMEUpdate => ({
    ...(password !== "" ? { password } : {}),
    allowed_networks: splitNetworks(networks),
  });

  const mutation = useMutation({
    mutationFn: (): Promise<CreatedESME> =>
      esme
        ? updateESME(esme.system_id, params())
        : createESME({ system_id: systemID, user, ...params() }),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ["esmes"] });
      showSnackbar(
        esme ? "ESME updated successfully." : "ESME added successfully.",
        "success",
      );
      if (!esme) onCreated?.(result);
      onClose();
    },
  });

  const systemIDProblem =
    esme || systemID === "" ? undefined : systemIDError(systemID);
  const passwordProblem = password === "" ? undefined : passwordError(password);
  const networkList = splitNetworks(networks);
  const badNetwork = networkList.find((n) => !isPrefix(n));
  const networksError =
    badNetwork !== undefined
      ? `${badNetwork} is not a network prefix, such as 10.1.0.0/24.`
      : undefined;

  const valid =
    (esme !== undefined ||
      (systemIDError(systemID) === undefined && user !== "")) &&
    passwordProblem === undefined &&
    networksError === undefined;

  return (
    <EditDialog
      title={esme ? "Edit ESME" : "Add ESME"}
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel={esme ? "Update" : "Add"}
      pendingLabel={esme ? "Updating…" : "Adding…"}
      failure={esme ? "Could not update the ESME" : "Could not add the ESME"}
    >
      <TextField
        label="System ID"
        value={systemID}
        onChange={(e) => setSystemID(e.target.value)}
        error={systemIDProblem !== undefined}
        helperText={
          systemIDProblem ??
          (esme
            ? undefined
            : "The ESME's system_id in its bind. Cannot be changed.")
        }
        disabled={esme !== undefined}
        required={!esme}
        autoFocus={!esme}
        autoComplete="off"
      />
      <TextField
        select
        label="User"
        value={user}
        onChange={(e) => setUser(e.target.value)}
        helperText={
          users.isError
            ? "Could not load the users."
            : "The user the ESME acts as: its messages are attributed to it, and its role decides the binds it may make. Cannot be changed."
        }
        error={users.isError}
        disabled={esme !== undefined}
        required={!esme}
      >
        {esme ? (
          <MenuItem value={esme.user}>{esme.user}</MenuItem>
        ) : (
          (users.data?.items ?? []).map((u) => (
            // A Read Only user may not bind.
            <MenuItem
              key={u.email}
              value={u.email}
              disabled={u.role_id === RoleID.ReadOnly}
            >
              {u.email} · {roleLabels[u.role_id]}
            </MenuItem>
          ))
        )}
      </TextField>
      <PasswordField
        label="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={passwordProblem !== undefined}
        helperText={
          passwordProblem ??
          (esme
            ? `1 to ${MAX_PASSWORD_LENGTH} characters. Leave blank to keep the current password.`
            : `1 to ${MAX_PASSWORD_LENGTH} characters. Leave blank to generate one.`)
        }
        autoComplete="new-password"
        autoFocus={esme !== undefined}
      />
      <TextField
        label="Allowed networks"
        value={networks}
        onChange={(e) => setNetworks(e.target.value)}
        error={networksError !== undefined}
        helperText={
          networksError ??
          "The networks the ESME may bind from, comma-separated. None: it may not bind."
        }
        placeholder="10.1.0.0/24, 2001:db8::/32"
      />
    </EditDialog>
  );
}

// ESMEPasswordDialog shows the password the server generated for a new ESME, once.
export function ESMEPasswordDialog({
  esme,
  onClose,
}: {
  esme: CreatedESME & { password: string };
  onClose: () => void;
}) {
  const copy = useCopyToClipboard();

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>ESME Password</DialogTitle>
      <DialogContent>
        <Alert
          severity="success"
          variant="outlined"
          sx={{ alignItems: "center" }}
          action={
            <Tooltip title="Copy" arrow>
              <IconButton
                aria-label="Copy password"
                size="small"
                onClick={() => void copy(esme.password, "Password")}
              >
                <ContentCopyIcon fontSize="inherit" />
              </IconButton>
            </Tooltip>
          }
        >
          <Typography variant="body2" sx={{ mb: 0.5 }}>
            The password of {esme.system_id}. Copy it now: it is not shown
            again.
          </Typography>
          <Typography
            variant="body2"
            data-testid="new-esme-password"
            sx={{
              fontFamily: "monospace",
              wordBreak: "break-all",
              userSelect: "all",
            }}
          >
            {esme.password}
          </Typography>
        </Alert>
      </DialogContent>
      <DialogActions>
        <Button variant="contained" onClick={onClose}>
          Done
        </Button>
      </DialogActions>
    </Dialog>
  );
}
