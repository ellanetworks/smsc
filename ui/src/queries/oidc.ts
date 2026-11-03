import { apiFetch } from "@/queries/utils";
import type { RoleID } from "@/queries/users";

export interface GroupRole {
  group: string;
  role_id: RoleID;
}

export interface OIDCSettings {
  enabled: boolean;
  external_url: string;
  redirect_url: string;
  issuer: string;
  client_id: string;
  client_secret_set: boolean;
  display_name: string;
  scopes: string[];
  session_lifetime_seconds: number;
  trust_unverified_email: boolean;
  ca_certificate: string;
  groups_claim: string;
  group_roles: GroupRole[];
}

export interface UpdateOIDCSettings {
  enabled: boolean;
  external_url: string;
  issuer: string;
  client_id: string;
  client_secret: string;
  display_name: string;
  scopes: string[];
  session_lifetime_seconds: number;
  trust_unverified_email: boolean;
  ca_certificate: string;
  groups_claim: string;
  group_roles: GroupRole[];
}

export interface OIDCDiscovery {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  jwks_uri: string;
  signing_algorithms: string[];
  keys: number;
  pkce: boolean;
  issuer_parameter: boolean;
}

export interface OIDCLogin {
  enabled: boolean;
  display_name?: string;
}

export const MIN_SESSION_LIFETIME_SECONDS = 300;
export const MAX_SESSION_LIFETIME_SECONDS = 30 * 24 * 3600;
export const OIDC_LOGIN_PATH = "/api/v1/auth/oidc/login";

export const getOIDC = () => apiFetch<OIDCSettings>("/api/v1/oidc");

export const updateOIDC = (settings: UpdateOIDCSettings) =>
  apiFetch<OIDCSettings>("/api/v1/oidc", { method: "PUT", body: settings });

export const discoverOIDC = (issuer: string, caCertificate: string) =>
  apiFetch<OIDCDiscovery>("/api/v1/oidc/discovery", {
    method: "POST",
    body: { issuer, ca_certificate: caCertificate },
  });

export const getOIDCLogin = () =>
  apiFetch<OIDCLogin>("/api/v1/auth/oidc", { anonymous: true });

export const oidcLoginErrors: Record<string, string> = {
  rate_limited: "Too many login attempts. Try again in a minute.",
  unavailable:
    "Single sign-on is not available. Try again later, or log in with your password.",
  failed: "Single sign-on failed. Try again.",
  denied: "Your identity provider did not let you sign in.",
  not_set_up:
    "Your account has not been set up in Ella SMSC. Contact an administrator.",
  email_not_verified:
    "Your identity provider has not verified your email address. Contact an administrator.",
  linked_elsewhere:
    "Your Ella SMSC account is linked to another identity provider account. Contact an administrator.",
  no_group:
    "None of your groups gives access to Ella SMSC. Contact an administrator.",
  groups_overage:
    "You are in too many groups for your identity provider to list them. Contact an administrator.",
  too_many_users:
    "Ella SMSC has reached its number of users. Contact an administrator.",
  error: "Single sign-on failed. Try again, or contact an administrator.",
};
