---
description: Step-by-step instructions to let users sign in to Ella SMSC through an OpenID Connect identity provider, such as Keycloak or Microsoft Entra ID.
---

# Configure Single Sign-On

This guide lets users sign in to the Ella SMSC UI through an OpenID Connect identity provider. It gives the steps for Keycloak and Microsoft Entra ID; other providers are set up the same way.

## Prerequisites

- **Ella SMSC**: served over HTTPS, at a URL with no path.
- **Identity provider**: an OpenID Connect provider, reachable from Ella SMSC over HTTPS, that puts the user's `email` in the ID token.
- **Ella SMSC account**: an Admin that signs in with a password.

The examples use these values:

| Value | Role |
|---|---|
| `https://smsc.example.com` | Ella SMSC's external URL |
| `https://smsc.example.com/api/v1/auth/oidc/callback` | Redirect URL: the external URL followed by `/api/v1/auth/oidc/callback` |

## 1. Register Ella SMSC at the identity provider

=== "Keycloak"

    In the realm users sign in to:

    1. Go to **Clients** and click **Create client**.
    2. Set **Client type** to **OpenID Connect** and **Client ID** to `smsc`. Click **Next**.
    3. Turn on **Client authentication**. Keep **Standard flow** checked. Click **Next**.
    4. Set **Valid redirect URIs** to the redirect URL. Click **Save**.
    5. In the client's **Credentials** tab, copy the **Client Secret**.

=== "Microsoft Entra ID"

    In the [Microsoft Entra admin center](https://entra.microsoft.com):

    1. Go to **Entra ID** > **App registrations** and click **New registration**.
    2. Set **Name** to `Ella SMSC`, and **Supported account types** to **Accounts in this organizational directory only**.
    3. Under **Redirect URI**, choose **Web** and enter the redirect URL. Click **Register**.
    4. In **Certificates & secrets** > **Client secrets**, click **New client secret**, then copy its **Value**.

=== "Other providers"

    1. Register a web application, or confidential client, with the redirect URL.
    2. Create a client secret for it.
    3. Allow the `openid`, `email` and `profile` scopes.

    If the provider does not send `email_verified` in the ID token, turn on **Trust email addresses from this provider** in step 2.

## 2. Configure Ella SMSC

In the UI, go to **Single Sign-On** and click **Edit**. Set:

| Field | Value |
|---|---|
| **Display name** | The name on the login button, such as `Keycloak` |
| **Issuer URL** | Keycloak: `https://<keycloak-host>/realms/<realm>`. Entra ID: `https://login.microsoftonline.com/<Directory (tenant) ID>/v2.0`. Otherwise, from the provider. |
| **Client ID** | Keycloak: `smsc`. Entra ID: the **Application (client) ID**. Otherwise, from the provider. |
| **Client secret** | The secret from step 1 |
| **External URL** | `https://smsc.example.com` |
| **Scopes** | `openid email profile` |
| **Session lifetime (hours)** | `24`, or less to end access sooner after offboarding |
| **CA certificate** | The PEM CA certificate of the identity provider, only if a private CA signs its certificate |
| **Trust email addresses from this provider** | Off, unless the provider does not mark email addresses as verified, as Entra ID does not. |

Check that the **Redirect URL** on the page is the one registered in step 1.

## 3. Test the identity provider

In the dialog, click **Test**. Fix the issuer URL or the CA certificate until the test passes.

## 4. Enable single sign-on

Turn on **Enabled** and click **Update**.

## 5. Sign in

1. Give a user access: [create their user](manage_single_sign_on_users.md#give-a-user-access), or [map their groups to a role](manage_single_sign_on_users.md#map-groups-to-roles).
2. In a private browser window, open `https://smsc.example.com`, and click **Sign in with** your display name.
3. Sign in at the identity provider. Ella SMSC opens.

If the login page shows an error, the `auth_oidc_login` [audit event](../reference/observability.md#audit-events) in the logs gives the reason.
