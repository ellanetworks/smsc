# API

## Authentication

Requests send a token in the `Authorization` header:

```
Authorization: Bearer <token>
```

The token is either:

- an access token, from [log in](https://docs.ellanetworks.com/smsc/reference/api/auth/#log-in) or [refresh the access token](https://docs.ellanetworks.com/smsc/reference/api/auth/#refresh-the-access-token), valid for 15 minutes;
- an [API token](https://docs.ellanetworks.com/smsc/reference/api/users/#create-an-api-token), which starts with `ellasmsc_`.

These paths take no token: `GET /api/v1/livez`, `GET /api/v1/readyz`, `GET /api/v1/status`, `GET /api/v1/metrics`, `GET /api/v1/openapi.yaml`, `POST /api/v1/init`, `POST /api/v1/cluster/join`, `POST /api/v1/auth/login`, `/refresh` and `/logout`, and `GET /api/v1/auth/oidc`, `/oidc/login` and `/oidc/callback`.

### Roles

| Role            | `role_id` | Allows                                                                                                                                                                                    |
| --------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Admin           | `1`       | Everything.                                                                                                                                                                               |
| Read Only       | `2`       | Reading messages without their content, Diameter, operator, delivery and retention settings, ESMEs, and the [cluster](https://docs.ellanetworks.com/smsc/reference/api/cluster/index.md). |
| Network Manager | `3`       | Read Only, plus reading message content, sending messages, updating operator, delivery and retention settings, and managing Diameter peers.                                               |
| Submitter       | `4`       | Sending messages, and reading the messages it sent, their content included.                                                                                                               |

An API token has the role of its user, but cannot create API tokens, change a password, or manage users, [single sign-on](https://docs.ellanetworks.com/smsc/reference/api/oidc/index.md) or [ESMEs](https://docs.ellanetworks.com/smsc/reference/api/esmes/index.md).

Only an Admin manages ESMEs. An [ESME](https://docs.ellanetworks.com/smsc/reference/api/esmes/index.md) binds over SMPP with the role of its user.

## Responses

Ella SMSC's API responses are JSON objects. A successful response carries a `result`:

```
{
    "result": "Result content"
}
```

A failed response carries an `error`:

```
{
    "error": "Error message"
}
```

`GET /api/v1/livez` answers 204 with no body. `GET /api/v1/readyz` answers 200 when ready and 503 when not, with the same body in both cases. `GET /api/v1/metrics` returns text in the [Prometheus exposition format](https://prometheus.io/docs/instrumenting/exposition_formats/#text-format-details), `GET /api/v1/openapi.yaml` returns YAML, and unknown paths and methods answer in plain text.

Times are RFC 3339, in UTC.

## OpenAPI Specification

The full API is described by an [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0) specification embedded in the binary. Fetch it from any running Ella SMSC instance:

```
GET /api/v1/openapi.yaml
```

## Status codes

- 200 - Success.
- 201 - Created.
- 204 - No content.
- 400 - Bad request.
- 401 - Missing, invalid or expired token.
- 403 - Not allowed for the role or the token.
- 404 - Not found.
- 405 - Method not allowed.
- 409 - Conflict.
- 429 - Too many requests. `Retry-After` is the seconds to wait.
- 500 - Internal server error.
- 502 - The identity provider cannot be reached or used. See [Test an identity provider](https://docs.ellanetworks.com/smsc/reference/api/oidc/#test-an-identity-provider).
- 503 - Not ready. See [Check readiness](https://docs.ellanetworks.com/smsc/reference/api/status/#check-readiness).
