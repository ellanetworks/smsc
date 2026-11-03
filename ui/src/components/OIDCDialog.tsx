import { useEffect, useRef, useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  ArrowDownward as ArrowDownwardIcon,
  ArrowUpward as ArrowUpwardIcon,
  Delete as DeleteIcon,
} from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import PasswordField from "@/components/PasswordField";
import RoleField from "@/components/RoleField";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import {
  caCertificateError,
  discoverOIDC,
  groupErrors,
  httpsURLError,
  MAX_DISPLAY_NAME_BYTES,
  MAX_GROUP_ROLES,
  MAX_SESSION_LIFETIME_SECONDS,
  MAX_VALUE_BYTES,
  MIN_SESSION_LIFETIME_SECONDS,
  type OIDCDiscovery,
  type OIDCSettings,
  scopesError,
  splitScopes,
  textError,
  updateOIDC,
} from "@/queries/oidc";
import { RoleID } from "@/queries/users";

const lifetimeUnits = [
  { label: "days", seconds: 86400 },
  { label: "hours", seconds: 3600 },
  { label: "minutes", seconds: 60 },
  { label: "seconds", seconds: 1 },
];

const unitsFor = (seconds: number) =>
  lifetimeUnits.filter((u) => u.seconds >= 60 || seconds % 60 !== 0);

const largestUnit = (seconds: number) =>
  lifetimeUnits.find((u) => seconds % u.seconds === 0)!.seconds;

interface MappingRow {
  id: number;
  group: string;
  role_id: RoleID;
}

type Direction = "up" | "down";

function DiscoveryResult({ discovery }: { discovery: OIDCDiscovery }) {
  const yesNo = (value: boolean) => (value ? "Yes" : "No");
  const rows: [string, string][] = [
    ["Signing keys", String(discovery.keys)],
    ["Signing algorithms", discovery.signing_algorithms.join(", ")],
    ["PKCE", yesNo(discovery.pkce)],
    ["Issuer parameter (RFC 9207)", yesNo(discovery.issuer_parameter)],
    ["Client authentication", discovery.token_endpoint_auth_method],
    ["UserInfo endpoint", discovery.userinfo_endpoint || "None"],
  ];

  return (
    <Alert severity="success" sx={{ mt: 1 }}>
      <AlertTitle>Ella SMSC can use this identity provider.</AlertTitle>
      <Box
        component="dl"
        sx={{
          m: 0,
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "auto 1fr" },
          columnGap: 2,
          "& dd": { m: 0, mb: { xs: 0.5, sm: 0 }, overflowWrap: "anywhere" },
        }}
      >
        {rows.map(([label, value]) => (
          <Box key={label} sx={{ display: "contents" }}>
            <Typography component="dt" variant="body2">
              {label}
            </Typography>
            <Typography
              component="dd"
              variant="body2"
              sx={{ fontWeight: "medium" }}
            >
              {value}
            </Typography>
          </Box>
        ))}
      </Box>
    </Alert>
  );
}

export default function OIDCDialog({
  settings,
  onClose,
}: {
  settings: OIDCSettings;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [enabled, setEnabled] = useState(settings.enabled);
  const [displayName, setDisplayName] = useState(settings.display_name);
  const [issuer, setIssuer] = useState(settings.issuer);
  const [clientID, setClientID] = useState(settings.client_id);
  const [clientSecret, setClientSecret] = useState("");
  const [externalURL, setExternalURL] = useState(
    settings.external_url ||
      (window.location.protocol === "https:" ? window.location.origin : ""),
  );
  const [scopes, setScopes] = useState(settings.scopes.join(" "));
  const [lifetimeUnit, setLifetimeUnit] = useState(() =>
    largestUnit(settings.session_lifetime_seconds),
  );
  const [lifetime, setLifetime] = useState(() =>
    String(settings.session_lifetime_seconds / lifetimeUnit),
  );
  const [caCertificate, setCACertificate] = useState(settings.ca_certificate);
  const [trustEmail, setTrustEmail] = useState(settings.trust_unverified_email);
  const [groupsClaim, setGroupsClaim] = useState(settings.groups_claim);
  const nextID = useRef(settings.group_roles.length);
  const [rows, setRows] = useState<MappingRow[]>(() =>
    settings.group_roles.map((g, id) => ({ id, ...g })),
  );
  const moveButtons = useRef(new Map<string, HTMLButtonElement | null>());
  const pendingFocus = useRef<{ id: number; direction: Direction } | null>(
    null,
  );

  useEffect(() => {
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    const order: Direction[] =
      target.direction === "up" ? ["up", "down"] : ["down", "up"];
    for (const direction of order) {
      const button = moveButtons.current.get(`${target.id}:${direction}`);
      if (button && !button.disabled) {
        button.focus();
        return;
      }
    }
  });

  const lifetimeSeconds = /^\d+$/.test(lifetime.trim())
    ? Number(lifetime.trim()) * lifetimeUnit
    : NaN;
  const trimmedGroups = rows.map((r) => r.group.trim());
  const scopeList = splitScopes(scopes);
  const identityChanged =
    issuer.trim() !== settings.issuer || clientID.trim() !== settings.client_id;

  const mutation = useMutation({
    mutationFn: () =>
      updateOIDC({
        enabled,
        external_url: externalURL.trim().replace(/\/$/, ""),
        issuer: issuer.trim(),
        client_id: clientID.trim(),
        client_secret: clientSecret,
        display_name: displayName.trim(),
        scopes: scopeList,
        session_lifetime_seconds: lifetimeSeconds,
        trust_unverified_email: trustEmail,
        ca_certificate: caCertificate.trim(),
        groups_claim: groupsClaim.trim(),
        group_roles: rows.map((r, i) => ({
          group: trimmedGroups[i],
          role_id: r.role_id,
        })),
      }),
    onSuccess: (updated) => {
      queryClient.setQueryData(["oidc"], updated);
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("Single sign-on updated successfully.", "success");
      onClose();
    },
  });

  const test = useMutation({
    mutationFn: () => discoverOIDC(issuer.trim(), caCertificate.trim()),
  });

  const groups = groupErrors(trimmedGroups);
  const errors = {
    displayName: textError(
      displayName.trim(),
      MAX_DISPLAY_NAME_BYTES,
      enabled ? "Enter the name to show on the login page." : undefined,
    ),
    issuer: httpsURLError(issuer.trim(), {
      missing: enabled ? "Enter the issuer URL." : undefined,
    }),
    clientID: textError(
      clientID.trim(),
      MAX_VALUE_BYTES,
      enabled ? "Enter the client ID." : undefined,
    ),
    clientSecret:
      textError(clientSecret, MAX_VALUE_BYTES) ??
      (clientSecret !== ""
        ? undefined
        : settings.client_secret_set
          ? identityChanged
            ? "Enter the client secret again: the issuer URL or client ID changed."
            : undefined
          : enabled
            ? "Enter the client secret."
            : undefined),
    externalURL: httpsURLError(externalURL.trim(), {
      origin: true,
      missing: enabled ? "Enter the external URL." : undefined,
    }),
    scopes: scopesError(scopeList),
    lifetime:
      !Number.isSafeInteger(lifetimeSeconds) ||
      lifetimeSeconds < MIN_SESSION_LIFETIME_SECONDS ||
      lifetimeSeconds > MAX_SESSION_LIFETIME_SECONDS
        ? "From 5 minutes to 30 days."
        : undefined,
    caCertificate: caCertificateError(caCertificate.trim()),
    groupsClaim: textError(
      groupsClaim.trim(),
      MAX_VALUE_BYTES,
      rows.length > 0 ? "Enter the claim that holds the groups." : undefined,
    ),
  };
  const valid =
    Object.values(errors).every((e) => e === undefined) &&
    groups.every((e) => e === undefined) &&
    rows.length <= MAX_GROUP_ROLES;

  const setRow = (i: number, change: Partial<MappingRow>) =>
    setRows((current) =>
      current.map((row, j) => (j === i ? { ...row, ...change } : row)),
    );
  const move = (i: number, direction: Direction) => {
    const by = direction === "up" ? -1 : 1;
    pendingFocus.current = { id: rows[i].id, direction };
    setRows((current) => {
      const next = [...current];
      [next[i], next[i + by]] = [next[i + by], next[i]];
      return next;
    });
  };
  const moveButton =
    (id: number, direction: Direction) => (node: HTMLButtonElement | null) => {
      moveButtons.current.set(`${id}:${direction}`, node);
    };

  return (
    <EditDialog
      title="Edit Single Sign-On"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
    >
      <FormControlLabel
        control={
          <Switch
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
        }
        label="Enabled"
      />
      <TextField
        label="Display name"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        error={errors.displayName !== undefined}
        helperText={
          errors.displayName ?? 'Shown on the login page: "Sign in with …".'
        }
        required={enabled}
      />
      <TextField
        label="Issuer URL"
        value={issuer}
        onChange={(e) => {
          setIssuer(e.target.value);
          test.reset();
        }}
        error={errors.issuer !== undefined}
        helperText={
          errors.issuer ??
          "Exactly as your identity provider names itself, such as https://idp.example.com/realms/smsc."
        }
        required={enabled}
      />
      <TextField
        label="Client ID"
        value={clientID}
        onChange={(e) => setClientID(e.target.value)}
        error={errors.clientID !== undefined}
        helperText={errors.clientID}
        required={enabled}
      />
      <PasswordField
        label="Client secret"
        value={clientSecret}
        onChange={(e) => setClientSecret(e.target.value)}
        error={errors.clientSecret !== undefined}
        helperText={
          errors.clientSecret ??
          (settings.client_secret_set
            ? "Leave empty to keep the current secret."
            : undefined)
        }
        autoComplete="new-password"
      />
      <TextField
        label="External URL"
        value={externalURL}
        onChange={(e) => setExternalURL(e.target.value)}
        error={errors.externalURL !== undefined}
        helperText={
          errors.externalURL ??
          "The https address users open Ella SMSC at, with no path. It sets the redirect URL."
        }
        required={enabled}
      />
      <TextField
        label="Scopes"
        value={scopes}
        onChange={(e) => setScopes(e.target.value)}
        error={errors.scopes !== undefined}
        helperText={errors.scopes ?? "Separated by spaces."}
      />
      <Stack direction="row" spacing={1} sx={{ alignItems: "start" }}>
        <TextField
          label="Session lifetime"
          type="number"
          value={lifetime}
          onChange={(e) => setLifetime(e.target.value)}
          error={errors.lifetime !== undefined}
          helperText={
            errors.lifetime ??
            "After this, users sign in through the identity provider again."
          }
          slotProps={{ htmlInput: { min: 1, step: 1 } }}
          sx={{ flex: 1 }}
        />
        <TextField
          select
          label="Unit"
          value={lifetimeUnit}
          onChange={(e) => setLifetimeUnit(Number(e.target.value))}
          sx={{ width: 130 }}
        >
          {unitsFor(settings.session_lifetime_seconds).map((u) => (
            <MenuItem key={u.seconds} value={u.seconds}>
              {u.label}
            </MenuItem>
          ))}
        </TextField>
      </Stack>
      <TextField
        label="CA certificate"
        value={caCertificate}
        onChange={(e) => {
          setCACertificate(e.target.value);
          test.reset();
        }}
        error={errors.caCertificate !== undefined}
        helperText={
          errors.caCertificate ??
          "Optional. PEM certificates to trust for the identity provider, if a private CA signs its certificate."
        }
        multiline
        minRows={2}
        maxRows={8}
        slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
      />
      <Box>
        <Button
          variant="outlined"
          onClick={() => test.mutate()}
          disabled={
            issuer.trim() === "" ||
            errors.issuer !== undefined ||
            errors.caCertificate !== undefined ||
            test.isPending
          }
        >
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        {test.isSuccess && <DiscoveryResult discovery={test.data} />}
        {test.isError && (
          <Alert severity="error" sx={{ mt: 1 }}>
            <AlertTitle>
              Ella SMSC cannot use this identity provider.
            </AlertTitle>
            {errorMessage(test.error)}
          </Alert>
        )}
      </Box>
      <FormControlLabel
        control={
          <Switch
            checked={trustEmail}
            onChange={(e) => setTrustEmail(e.target.checked)}
          />
        }
        label="Trust email addresses from this provider"
      />
      {trustEmail && (
        <Alert severity="warning">
          When the provider does not say whether an email address is verified,
          users are linked by it anyway. Anyone who can set an email address at
          this provider can then take over the Ella SMSC user with that address,
          Admins included. Turn this on only for a provider that does not say,
          such as Microsoft Entra ID, and that controls the addresses of its
          users. An address the provider says is not verified is always refused.
        </Alert>
      )}
      <Typography variant="subtitle1" component="h3">
        Roles from groups
      </Typography>
      <Typography variant="body2" color="textSecondary">
        Optional. With mappings, users are created at their first sign-in, and
        their role follows their groups at each sign-in, unless it is pinned.
        The first matching group gives the role.
      </Typography>
      <TextField
        label="Groups claim"
        value={groupsClaim}
        onChange={(e) => setGroupsClaim(e.target.value)}
        error={errors.groupsClaim !== undefined}
        helperText={errors.groupsClaim ?? "Such as groups."}
      />
      {rows.map((row, i) => (
        <Stack
          key={row.id}
          direction={{ xs: "column", sm: "row" }}
          spacing={1}
          sx={{
            alignItems: { xs: "stretch", sm: "start" },
            borderWidth: { xs: 1, sm: 0 },
            borderStyle: "solid",
            borderColor: "divider",
            borderRadius: 1,
            p: { xs: 1.5, sm: 0 },
          }}
        >
          <TextField
            label={`Group ${i + 1}`}
            value={row.group}
            onChange={(e) => setRow(i, { group: e.target.value })}
            error={groups[i] !== undefined}
            helperText={groups[i]}
            required
            sx={{ flex: { sm: 1 } }}
          />
          <Box sx={{ flex: { sm: 1 } }}>
            <RoleField
              value={row.role_id}
              onChange={(role) => setRow(i, { role_id: role })}
              fullWidth
            />
          </Box>
          <Stack
            direction="row"
            sx={{ justifyContent: "flex-end", pt: { sm: 1 } }}
          >
            <Tooltip title="Move up">
              <span>
                <IconButton
                  ref={moveButton(row.id, "up")}
                  aria-label={`Move group ${i + 1} up`}
                  disabled={i === 0}
                  onClick={() => move(i, "up")}
                >
                  <ArrowUpwardIcon fontSize="small" />
                </IconButton>
              </span>
            </Tooltip>
            <Tooltip title="Move down">
              <span>
                <IconButton
                  ref={moveButton(row.id, "down")}
                  aria-label={`Move group ${i + 1} down`}
                  disabled={i === rows.length - 1}
                  onClick={() => move(i, "down")}
                >
                  <ArrowDownwardIcon fontSize="small" />
                </IconButton>
              </span>
            </Tooltip>
            <Tooltip title="Remove">
              <IconButton
                aria-label={`Remove group ${i + 1}`}
                onClick={() =>
                  setRows((current) => current.filter((_, j) => j !== i))
                }
              >
                <DeleteIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </Stack>
        </Stack>
      ))}
      <Box>
        <Button
          startIcon={<AddIcon />}
          disabled={rows.length >= MAX_GROUP_ROLES}
          onClick={() => {
            const id = nextID.current++;
            setRows((current) => [
              ...current,
              { id, group: "", role_id: RoleID.ReadOnly },
            ]);
          }}
        >
          Add group
        </Button>
        {rows.length >= MAX_GROUP_ROLES && (
          <Typography
            variant="body2"
            color="textSecondary"
            component="span"
            sx={{ ml: 1 }}
          >
            At most {MAX_GROUP_ROLES} groups.
          </Typography>
        )}
      </Box>
    </EditDialog>
  );
}
