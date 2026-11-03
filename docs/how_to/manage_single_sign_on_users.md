---
description: Step-by-step instructions to give, map, pin, unlink and remove the access of users who sign in to Ella SMSC through single sign-on.
---

# Manage Single Sign-On Users

These guides manage users who sign in through the identity provider. [Configure single sign-on](configure_single_sign_on.md) first.

## Give a user access

1. In the UI, go to **Users** and click **Create**. Set:
    - **Email**: the user's email address at the identity provider
    - **Password**: empty
    - **Role**: the user's role
2. Click **Create**.

The user signs in with **Sign in with** the display name. Their first login links their user to their account at the identity provider.

## Map groups to roles

Users in a mapped group get their role from it at each login, and get a user at their first login. Users in no mapped group cannot sign in through the identity provider, unless their role is [pinned](#pin-a-users-role).

### 1. Send groups in the ID token

=== "Keycloak"

    1. Go to **Clients**, open the `smsc` client, and open the **Client scopes** tab.
    2. Click `smsc-dedicated`, then **Configure a new mapper**, or **Add mapper** > **By configuration**, then **Group Membership**.
    3. Set **Name** and **Token Claim Name** to `groups`. Turn off **Full group path**. Keep **Add to ID token** on.
    4. Click **Save**.

    The groups are group names, such as `smsc-admins`.

=== "Microsoft Entra ID"

    1. Go to **Entra ID** > **App registrations** and open the app.
    2. In **Token configuration**, click **Add groups claim**.
    3. Check **Security groups**. Under **ID**, choose **Group ID**.
    4. Click **Save**.

    The groups are group object IDs, such as `5a1c2f3e-8b7d-4c6e-9f0a-1b2c3d4e5f60`: the **Object ID** of each group in **Entra ID** > **Groups**.

    Entra ID lists at most 200 groups in an ID token, and a user in more cannot sign in. If users are in more, check **Groups assigned to the application** instead of **Security groups**, and add the mapped groups to the app in **Entra ID** > **Enterprise applications** > the app > **Users and groups**. This needs Entra ID P1 or P2.

### 2. Map the groups in Ella SMSC

1. Go to **Single Sign-On** and click **Edit**.
2. Set **Groups claim** to `groups`.
3. Click **Add group** for each group, and set the group and its role. Put the most privileged first: a user in several groups gets the role of the first.
4. Click **Update**.

## Pin a user's role

A pinned role is kept whatever the user's groups.

1. Go to **Users** and open the user.
2. In **Account**, edit the role: choose the role, check **Pin role**, and click **Update**.

To unpin, uncheck **Pin role**. The role then follows the user's groups from their next login.

## Unlink a user

Unlink a user whose account at the identity provider was replaced, so that their next login links the new account by email.

1. Go to **Users** and open the user.
2. In **Account**, click **Unlink**, then **Unlink**.

## Offboard a user

1. Remove or disable the user at the identity provider. They can no longer sign in, but their current session lasts until the **Session lifetime** ends.
2. To end their access at once, go to **Users** in Ella SMSC, open the user, and click **Delete**. This also deletes their API tokens and [ESMEs](../reference/api/esmes.md).

To keep the user and their ESMEs instead, open the user and:

1. In **Account**, click **Unlink**, then **Unlink**. This ends their single sign-on sessions.
2. Click **Remove Password**, then **Remove**, if they have one.
3. In **API Tokens**, delete each token.
