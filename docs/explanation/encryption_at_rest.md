---
description: What Ella SMSC encrypts on disk, where its encryption key lives, and what that protects against.
---

# Encryption at Rest

Ella SMSC encrypts message content and its own secrets before writing them to disk.

## What is encrypted

| Encrypted | Not encrypted |
| --- | --- |
| Message content, text and binary, SIM data download commands and answers included | Phone numbers, times and delivery status: Ella SMSC searches and routes on them |
| The keys that sign logins and join nodes, and the single sign-on secrets | Passwords and tokens: stored as hashes, never readable |

## The encryption key

One key protects everything, on every node of a cluster. It lives in one of two places:

- **A key file.** On a snap, Ella SMSC creates it next to the database. On Kubernetes, it comes from a Secret you create.
- **An HSM.**The key never leaves the device. See [Use an HSM](../how_to/use_an_hsm.md).

## Backups

Backups are encrypted, and never hold the key. Without the key, a backup cannot be restored: keep a copy of it, apart from the backups. See [Back Up and Restore](../how_to/back_up_and_restore.md).

## Locked nodes

A node that cannot use the key, because its HSM is unplugged or its key file is wrong, is locked. It stays in the cluster and keeps its copy of the data, but refuses messages and logins. It unlocks on its own once the key is back.

## Rotating and changing the key

- [Rotating](../reference/api/encryption.md) creates new keys for new data. Older data stays readable.
- [Changing the key](../how_to/change_the_encryption_key.md), for example from a key file to an HSM, is done one node at a time, without downtime.
