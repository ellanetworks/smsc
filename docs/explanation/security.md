---
description: How Ella SMSC controls who can use its UI and API, protects message content, and decides whom to trust on Diameter.
---

# Security

## Users and roles

Everyone who uses the UI or the API is a user with a role: Admin, Read Only, Network Manager or Submitter. The role decides what the user can see and do, from reading settings to managing other users. See [Roles](../reference/api/index.md#roles).

The first user, an Admin, is created when Ella SMSC is initialized. Admins create the others.

Ella SMSC limits login attempts, from each address and for each account, to slow down password guessing.

## Sign-in methods

Users sign in to the UI with a password, through the organization's identity provider ([single sign-on](../how_to/configure_single_sign_on.md)), or both.

| | Password | Single sign-on |
|---|---|---|
| Credentials kept by | Ella SMSC | The identity provider, with its own policies, such as multi-factor authentication |
| Role set by | An Admin | An Admin, or the user's groups at the identity provider |
| Session | 30 days | The session lifetime, 24 hours by default |
| Access removed by | Deleting the user in Ella SMSC, at once | Removing the user at the identity provider, at the end of their session |

Password login stays when single sign-on is on, and at least one Admin always keeps a password. An identity provider that is down or misconfigured cannot lock everyone out.

A user is matched to their account at the identity provider, not to their email address, so a changed or reused email address does not give access to another user. The email address is used once, to link the account at its first login, and only if the identity provider has verified it. A provider that does not verify email addresses, such as Microsoft Entra ID, must be trusted explicitly: anyone who can set their email address there could take over the matching user. An account with no user in Ella SMSC gets one only from a [group mapping](../how_to/manage_single_sign_on_users.md#map-groups-to-roles).

Ella SMSC does not learn when someone is removed at the identity provider. Their session lasts until its lifetime ends, and their API tokens until deleted. A shorter session lifetime narrows that window, at the cost of signing in more often. To end access at once, [offboard the user](../how_to/manage_single_sign_on_users.md#offboard-a-user) in Ella SMSC.

The identity provider's client secret is stored in the database, and so in its [backups](../how_to/back_up_and_restore.md).

## Applications

Applications, such as an OTA platform, use an API token rather than a password. A token belongs to a user and has that user's role, but it cannot create API tokens, change a password, or manage users. Tokens can expire, and an Admin or the token's user can delete one at any time.

Give each application its own Submitter user: it can send messages, and read only the messages it sent.

## ESMEs

An ESME, such as an OTA platform, binds to the [SMPP listener](../reference/smpp.md) with a `system_id` and a password. SMPP 3.4 limits that password to 8 characters, so Ella SMSC adds other checks:

- Each ESME acts as a user, whose role decides whether it may submit or receive.
- An ESME binds only from the networks in its allow-list. An empty list allows no address.
- Passwords are stored as bcrypt hashes. A password the SMSC generates has 8 random letters and digits.
- Binds are limited from each address, and wrong passwords for each ESME. Every refused bind gets the same answer, so a client cannot tell which check failed.
- Only an Admin, logged in rather than with an API token, creates, changes or deletes ESMEs.

SMPP 3.4 has no encryption: the password and the messages cross the network in clear. TLS, with `smpp.tls`, is a common convention outside SMPP 3.4, which the ESME must support. Use plain TCP only over a private link, such as an IPsec VPN, or in a lab.

The allow-list checks the address of the TCP connection. Terminate TLS in Ella SMSC itself, not in a proxy or load balancer, which would hide the ESME's address. On Kubernetes, the Service keeps it with `externalTrafficPolicy: Local`.

## Message content

Messages carry personal content. Reading it is a permission of its own: a Read Only user sees that messages were sent and delivered, but not what they say. Every read of the content is recorded in an [audit event](../reference/observability.md#audit-events), with who read it and from where.

## HTTPS

Ella SMSC serves the UI and the API over plain HTTP unless it is given a certificate in `api.tls` (see the [configuration file](../reference/config_file.md)). Use HTTPS whenever the UI or the API is reachable over a network you do not control.

## Diameter peers

Ella SMSC accepts any Diameter peer that connects and supports S6c or SGd, and its Diameter connections are not encrypted. Keep its Diameter address on a network that only the core network can reach.

Through an edge agent, other networks can send short messages to Ella SMSC, which does not filter them by realm. Have the edge agent accept only the operator's roaming partners.
