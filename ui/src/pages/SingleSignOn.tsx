import { useState } from "react";
import {
  Box,
  Button,
  Chip,
  IconButton,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import { ContentCopy as ContentCopyIcon } from "@mui/icons-material";
import { useQuery } from "@tanstack/react-query";
import OIDCDialog from "@/components/OIDCDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getOIDC } from "@/queries/oidc";
import { roleLabels } from "@/queries/users";

const orNone = (value: string) => value || "None";

const duration = (seconds: number) =>
  seconds % 3600 === 0
    ? `${seconds / 3600} hours`
    : `${Math.round(seconds / 60)} minutes`;

export default function SingleSignOn() {
  const [editing, setEditing] = useState(false);
  const copy = useCopyToClipboard();
  const oidc = useQuery({ queryKey: ["oidc"], queryFn: getOIDC });
  const { data, isPending } = oidc;

  return (
    <Box component="section" aria-labelledby="sso-title">
      <PageHeader
        id="sso-title"
        title="Single Sign-On"
        description="Let users sign in through your organization's identity provider, with OpenID Connect."
        action={
          <Button
            variant="contained"
            color="success"
            disabled={!data}
            onClick={() => setEditing(true)}
          >
            Edit
          </Button>
        }
      />
      <QueryAlert query={oidc} subject="single sign-on settings" />
      <SettingsTable
        label="Single sign-on settings"
        loading={isPending}
        rows={[
          {
            label: "Status",
            help: "Whether the login page offers to sign in through the identity provider.",
            value: data && (
              <Chip
                size="small"
                variant="outlined"
                label={data.enabled ? "Enabled" : "Disabled"}
                color={data.enabled ? "success" : "default"}
              />
            ),
          },
          {
            label: "Display name",
            help: "The name on the login page's button.",
            value: data && orNone(data.display_name),
          },
          {
            label: "Issuer URL",
            help: "The identity provider.",
            value: data && orNone(data.issuer),
          },
          {
            label: "Client ID",
            help: "The client registered for Ella SMSC at the identity provider.",
            value: data && orNone(data.client_id),
          },
          {
            label: "Client secret",
            help: "Never shown.",
            value: data && (data.client_secret_set ? "Set" : "Not set"),
          },
          {
            label: "Redirect URL",
            help: "Register this URL for the client at the identity provider.",
            value: data && (
              <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
                <Typography
                  variant="body2"
                  sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}
                >
                  {data.redirect_url || "Set the external URL first."}
                </Typography>
                {data.redirect_url && (
                  <Tooltip title="Copy">
                    <IconButton
                      size="small"
                      aria-label="Copy the redirect URL"
                      onClick={() =>
                        void copy(data.redirect_url, "Redirect URL")
                      }
                    >
                      <ContentCopyIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                )}
              </Stack>
            ),
          },
          {
            label: "Scopes",
            help: "What Ella SMSC asks the identity provider for.",
            value: data?.scopes.join(" "),
          },
          {
            label: "Session lifetime",
            help: "After this, users sign in through the identity provider again.",
            value: data && duration(data.session_lifetime_seconds),
          },
          {
            label: "CA certificate",
            help: "Certificates trusted for the identity provider, besides the system's.",
            value: data && (data.ca_certificate ? "Set" : "System"),
          },
          {
            label: "Trust email addresses",
            help: "Whether users are linked by an email the provider has not verified.",
            value: data && (data.trust_unverified_email ? "Yes" : "No"),
          },
          {
            label: "Roles from groups",
            help: "The first matching group gives the role. Without mappings, admins create users and set their roles.",
            value:
              data &&
              (data.group_roles.length === 0 ? (
                "None"
              ) : (
                <Stack spacing={0.5}>
                  <Typography variant="body2" color="textSecondary">
                    Claim: {data.groups_claim}
                  </Typography>
                  {data.group_roles.map((g, i) => (
                    <Typography key={g.group} variant="body2">
                      {i + 1}. {g.group} → {roleLabels[g.role_id]}
                    </Typography>
                  ))}
                </Stack>
              )),
          },
        ]}
      />
      {editing && data && (
        <OIDCDialog settings={data} onClose={() => setEditing(false)} />
      )}
    </Box>
  );
}
