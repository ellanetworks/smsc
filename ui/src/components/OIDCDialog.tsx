import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  FormControlLabel,
  IconButton,
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
  discoverOIDC,
  type GroupRole,
  MAX_SESSION_LIFETIME_SECONDS,
  MIN_SESSION_LIFETIME_SECONDS,
  type OIDCSettings,
  updateOIDC,
} from "@/queries/oidc";
import { RoleID } from "@/queries/users";

const HTTPS_URL = /^https:\/\/[^\s/?#]+(\/[^\s?#]*)?$/;
const HTTPS_ORIGIN = /^https:\/\/[^\s/?#]+\/?$/;

const hours = (seconds: number) => String(+(seconds / 3600).toFixed(2));

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
    settings.external_url || window.location.origin,
  );
  const [scopes, setScopes] = useState(settings.scopes.join(" "));
  const [lifetime, setLifetime] = useState(
    hours(settings.session_lifetime_seconds),
  );
  const [caCertificate, setCACertificate] = useState(settings.ca_certificate);
  const [trustEmail, setTrustEmail] = useState(settings.trust_unverified_email);
  const [groupsClaim, setGroupsClaim] = useState(settings.groups_claim);
  const [groupRoles, setGroupRoles] = useState<GroupRole[]>(
    settings.group_roles,
  );

  const mutation = useMutation({
    mutationFn: () =>
      updateOIDC({
        enabled,
        external_url: externalURL.trim().replace(/\/$/, ""),
        issuer: issuer.trim(),
        client_id: clientID.trim(),
        client_secret: clientSecret,
        display_name: displayName.trim(),
        scopes: scopes.split(/\s+/).filter(Boolean),
        session_lifetime_seconds: lifetimeSeconds,
        trust_unverified_email: trustEmail,
        ca_certificate: caCertificate.trim(),
        groups_claim: groupsClaim.trim(),
        group_roles: groupRoles.map((g) => ({ ...g, group: g.group.trim() })),
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

  const lifetimeSeconds = Math.round(Number(lifetime) * 3600);
  const errors = {
    displayName:
      enabled && displayName.trim() === ""
        ? "Enter the name to show on the login page."
        : undefined,
    issuer:
      (enabled || issuer !== "") && !HTTPS_URL.test(issuer.trim())
        ? "An https URL with no query or fragment."
        : undefined,
    clientID:
      enabled && clientID.trim() === "" ? "Enter the client ID." : undefined,
    clientSecret:
      enabled && !settings.client_secret_set && clientSecret === ""
        ? "Enter the client secret."
        : undefined,
    externalURL:
      (enabled || externalURL !== "") && !HTTPS_ORIGIN.test(externalURL.trim())
        ? "The https address of Ella SMSC, with no path."
        : undefined,
    scopes: !scopes.split(/\s+/).includes("openid")
      ? "Include openid."
      : undefined,
    lifetime:
      !Number.isFinite(lifetimeSeconds) ||
      lifetimeSeconds < MIN_SESSION_LIFETIME_SECONDS ||
      lifetimeSeconds > MAX_SESSION_LIFETIME_SECONDS
        ? "From 5 minutes (0.09) to 30 days (720)."
        : undefined,
    groupsClaim:
      groupRoles.length > 0 && groupsClaim.trim() === ""
        ? "Enter the claim that holds the groups."
        : undefined,
    groups: groupRoles.some(
      (g, i) =>
        g.group.trim() === "" ||
        groupRoles.findIndex((o) => o.group.trim() === g.group.trim()) !== i,
    )
      ? "Each group must be named, and appear once."
      : undefined,
  };
  const valid = Object.values(errors).every((e) => e === undefined);

  const setGroup = (i: number, change: Partial<GroupRole>) =>
    setGroupRoles((rows) =>
      rows.map((row, j) => (j === i ? { ...row, ...change } : row)),
    );
  const move = (i: number, by: number) =>
    setGroupRoles((rows) => {
      const next = [...rows];
      [next[i], next[i + by]] = [next[i + by], next[i]];
      return next;
    });

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
        onChange={(e) => setIssuer(e.target.value)}
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
          "The https address users open Ella SMSC at. It sets the redirect URL."
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
      <TextField
        label="Session lifetime (hours)"
        type="number"
        value={lifetime}
        onChange={(e) => setLifetime(e.target.value)}
        error={errors.lifetime !== undefined}
        helperText={
          errors.lifetime ??
          "After this, users sign in through the identity provider again."
        }
        slotProps={{ htmlInput: { min: 0.09, max: 720, step: "any" } }}
      />
      <TextField
        label="CA certificate"
        value={caCertificate}
        onChange={(e) => setCACertificate(e.target.value)}
        helperText="Optional. PEM certificates to trust for the identity provider, if a private CA signs its certificate."
        multiline
        minRows={2}
        maxRows={8}
        slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
      />
      <Box>
        <Button
          variant="outlined"
          onClick={() => test.mutate()}
          disabled={!HTTPS_URL.test(issuer.trim()) || test.isPending}
        >
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        {test.isSuccess && (
          <Alert severity="success" sx={{ mt: 1 }}>
            Ella SMSC can use this identity provider.
          </Alert>
        )}
        {test.isError && (
          <Alert severity="error" sx={{ mt: 1 }}>
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
          Users are linked by email even when the provider has not verified it.
          Turn this on only for a provider that does not say whether an email is
          verified, such as Microsoft Entra ID, and that controls the addresses
          of its users.
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
      {groupRoles.map((g, i) => (
        <Stack key={i} direction="row" spacing={1} sx={{ alignItems: "start" }}>
          <TextField
            label={`Group ${i + 1}`}
            value={g.group}
            onChange={(e) => setGroup(i, { group: e.target.value })}
            sx={{ flex: 1 }}
          />
          <Box sx={{ flex: 1 }}>
            <RoleField
              value={g.role_id}
              onChange={(role) => setGroup(i, { role_id: role })}
            />
          </Box>
          <Tooltip title="Move up">
            <span>
              <IconButton
                aria-label={`Move group ${i + 1} up`}
                disabled={i === 0}
                onClick={() => move(i, -1)}
              >
                <ArrowUpwardIcon fontSize="small" />
              </IconButton>
            </span>
          </Tooltip>
          <Tooltip title="Move down">
            <span>
              <IconButton
                aria-label={`Move group ${i + 1} down`}
                disabled={i === groupRoles.length - 1}
                onClick={() => move(i, 1)}
              >
                <ArrowDownwardIcon fontSize="small" />
              </IconButton>
            </span>
          </Tooltip>
          <Tooltip title="Remove">
            <IconButton
              aria-label={`Remove group ${i + 1}`}
              onClick={() =>
                setGroupRoles((rows) => rows.filter((_, j) => j !== i))
              }
            >
              <DeleteIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Stack>
      ))}
      {errors.groups && <Alert severity="error">{errors.groups}</Alert>}
      <Box>
        <Button
          startIcon={<AddIcon />}
          onClick={() =>
            setGroupRoles((rows) => [
              ...rows,
              { group: "", role_id: RoleID.ReadOnly },
            ])
          }
        >
          Add group
        </Button>
      </Box>
    </EditDialog>
  );
}
