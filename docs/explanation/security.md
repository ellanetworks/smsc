---
description: How Ella SMSC controls who can use its UI and API, protects message content, and decides whom to trust on Diameter.
---

# Security

## Users and roles

Everyone who uses the UI or the API is a user with a role: Admin, Read Only, Network Manager or Submitter. The role decides what the user can see and do, from reading settings to managing other users. See [Roles](../reference/api/index.md#roles).

The first user, an Admin, is created when Ella SMSC is initialized. Admins create the others.

Ella SMSC limits login attempts, from each address and for each account, to slow down password guessing.

## Applications

Applications, such as an OTA platform, use an API token rather than a password. A token belongs to a user and has that user's role, but it cannot create API tokens, change a password, or manage users. Tokens can expire, and an Admin or the token's user can delete one at any time.

Give each application its own Submitter user: it can send messages, and read only the messages it sent.

## Message content

Messages carry personal content. Reading it is a permission of its own: a Read Only user sees that messages were sent and delivered, but not what they say. Every read of the content is recorded in an [audit event](../reference/observability.md#audit-events), with who read it and from where.

## HTTPS

Ella SMSC serves the UI and the API over plain HTTP unless it is given a certificate in `api.tls` (see the [configuration file](../reference/config_file.md)). Use HTTPS whenever the UI or the API is reachable over a network you do not control.

## Diameter peers

Ella SMSC accepts any Diameter peer that connects and supports S6c or SGd, and its Diameter connections are not encrypted. Keep its Diameter address on a network that only the core network can reach.

Through an edge agent, other networks can send short messages to Ella SMSC, which does not filter them by realm. Have the edge agent accept only the operator's roaming partners.
