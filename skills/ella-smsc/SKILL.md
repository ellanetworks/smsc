---
name: ella-smsc
description: >
  Manages and inspects a running Ella SMSC (SMS Service Center for private 4G and 5G
  networks: S6c to the HSS, SGd to MMEs, SMSFs and IP-SM-GWs, SMPP for ESMEs) through its
  REST API.
  Use when the user asks to send an SMS or a binary (SIM data download) message, follow
  its delivery status, look up messages to or from a phone number, read message content,
  explain why a message is pending, failed or expired, or inspect delivery attempts, message
  counts, Diameter peers (HSS, MME, SMSF, IP-SM-GW, DEA), ESMEs and their SMPP sessions, the
  operator and numbering plan, delivery and retention settings, users and API tokens,
  cluster state, or metrics of an Ella SMSC instance.
---

# Ella SMSC

## Connection

- Base URL: `ELLA_SMSC_URL`, e.g. `http://10.0.0.5:5010`. The API listens on port `5010`, over plain HTTP unless `api.tls` is set in the configuration file; use `curl -k` for a self-signed `https://` URL.
- Token: `ELLA_SMSC_TOKEN`, either an API token (starts with `ellasmsc_`, does not expire unless set to) or an access token from login (valid 15 minutes).

Ask the user for anything not in the environment or the conversation. If the user gives an email and password instead of a token, log in:

```bash
ELLA_SMSC_TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"..."}' \
  "$ELLA_SMSC_URL/api/v1/auth/login" | jq -r .result.token)
```

On a `401` with an access token, log in again.

Send `Authorization: Bearer $ELLA_SMSC_TOKEN` on every request, except `GET /api/v1/status`, `/livez`, `/readyz`, `/metrics` and `/openapi.yaml`.

## Finding the endpoint

The OpenAPI spec is the source of truth. Fetch it once per session when an operation is not covered below; never guess paths or fields:

```bash
curl -s "$ELLA_SMSC_URL/api/v1/openapi.yaml" | grep -E '^  /api'
```

| Question | Endpoint |
|----------|----------|
| Version, health | `GET /api/v1/status` |
| Messages to or from a number | `GET /api/v1/messages` (`to`, `from`, `status`, `start`, `end`) |
| Messages per status | `GET /api/v1/messages/counts` (`start`, `end`) |
| One message and its status | `GET /api/v1/messages/{id}` |
| Why it is pending or failed | `GET /api/v1/messages/{id}/attempts` |
| What it says | `GET /api/v1/messages/{id}/content` |
| HSS, MME and other Diameter connections | `GET /api/v1/diameter` (`peers[].state`, `peers[].applications`) |
| SMPP applications and their sessions | `GET /api/v1/esmes` |
| MCC, MNC, service centre address, numbering plan | `GET /api/v1/operator` |
| Validity, retry intervals | `GET /api/v1/delivery` |
| How long messages are kept | `GET /api/v1/messages/retention` |
| Cluster members, leader | `GET /api/v1/cluster` |
| Counters and gauges | `GET /api/v1/metrics` (Prometheus text, `ellasmsc_*`) |

Responses are `{"result": ...}` or `{"error": "..."}`. Paginated lists take `page` and `per_page` (default `25`, max `100`) and return `items`, `page`, `per_page`, `total_count`. Messages come newest first, delivery attempts oldest first. Diameter peers and ESMEs return `items` only.

## Sending and following a message

`POST /api/v1/messages` takes `from`, `to` and `text`. `to` is an E.164 number with `+`. `from` is an E.164 number with `+`, or a name of 1 to 11 ASCII letters and digits that is not all digits, such as `EllaSMSC`. The response has one item per part: a long text is split, and each part is its own message with its own `id` and status.

```bash
id=$(curl -s -X POST -H "Authorization: Bearer $ELLA_SMSC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"from":"EllaSMSC","to":"+15551230002","text":"Your shift starts at 7:00"}' \
  "$ELLA_SMSC_URL/api/v1/messages" | jq '.result.items[0].id')
for i in $(seq 15); do
  status=$(curl -s -H "Authorization: Bearer $ELLA_SMSC_TOKEN" \
    "$ELLA_SMSC_URL/api/v1/messages/$id" | jq -r .result.status)
  [ "$status" != pending ] && break
  sleep 2
done
echo "$status"
curl -s -H "Authorization: Bearer $ELLA_SMSC_TOKEN" "$ELLA_SMSC_URL/api/v1/messages/$id/attempts" \
  | jq -r '.result.items[] | [.started_at, .step, .node, (.node_type // "-"), .outcome] | @tsv'
```

Statuses: `pending`, `delegated` (waiting for the ESME whose route matches `to`), `delivered`, `failed`, `expired`, `replaced`. Attempts are a `routing` lookup at the HSS, then a `delivery` to the serving node (`mme`, `sgsn`, `ip_sm_gw`, `smsf_3gpp`, `smsf_non_3gpp`), each with an `outcome` such as `success`, `user_unknown`, `absent_user`, `no_hss` or `timeout`.

- A message still `pending` after the loop is not lost. With `absent_user`, the phone is unreachable: the SMSC retries after the retry intervals and when the HSS says the phone is back, until `expires_at`. Report `next_attempt_at` and stop polling.
- Messages to a phone are delivered one at a time, in order. A `pending` message with no attempts is waiting behind an older one to the same phone: list `GET /api/v1/messages?to=...&status=pending` to find it.
- A message from the API never asks for status reports, so its `status_report.reports` stays empty; the status and attempts are the delivery result.
- `to` and `from` filters match any number that contains the digits: `to=0002` matches many numbers. Pass the full number, URL-encoded (`%2B15551230002`).
- Lists and `GET /messages/{id}` carry no text. `GET /messages/{id}/content` returns it and logs an audit event, so read content only when the user asks for it.

## Changing things

Roles: Admin (`1`) does everything; Read Only (`2`) reads, without message content; Network Manager (`3`) also reads content, sends messages and changes operator, delivery, retention and Diameter peer settings; Submitter (`4`) sends messages and reads only the messages it sent. A `403` means the role or the token type is not allowed; do not retry. An API token cannot create API tokens, change a password, manage users or ESMEs, or change single sign-on settings: those need an access token from login, and ESMEs need Admin.

Before any `POST`, `PUT` or `DELETE`, sending a message included, say what will happen and get the user's approval, unless the user already asked for exactly that message. `GET` the current state first, make one change at a time, and `GET` again to confirm it.

- `PUT` replaces the whole resource: `GET` it and send every field back, changed or not. An ESME keeps its password and routes when they are left out.
- Changing MCC or MNC with `PUT /api/v1/operator` changes the Diameter host and realm: Diameter restarts and the peers reconnect. Warn the user.
- Changing `default_validity_seconds` with `PUT /api/v1/delivery` applies to messages received afterwards; new retry intervals apply to the next retries.
- Changing a Diameter peer's `host`, `addresses` or `port` reconnects it.
- Deleting an ESME closes its sessions and fails the messages waiting for it. Deleting a user deletes its ESMEs.
- An ESME password generated by the SMSC, and an API token, are shown once, in the response that creates them.

Request bodies and constraints are in the spec, or at `https://docs.ellanetworks.com/smsc/reference/api/`.
