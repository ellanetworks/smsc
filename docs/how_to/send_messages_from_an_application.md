---
description: Send short messages from an application, such as an OTA platform, over the Ella SMSC API.
---

# Send Messages from an Application

This guide gives an application, such as an OTA or eSIM platform, an API token with which it sends short messages and follows their delivery.

## 1. Serve the API over TLS

The token crosses the network in every request. Unless the application runs on the SMSC's host, set `api.tls` in the [configuration file](../reference/config_file.md):

```yaml
api:
  address: 0.0.0.0
  port: 5010
  tls:
    cert: /path/to/cert.pem
    key: /path/to/key.pem
```

Restart Ella SMSC. The UI and API are then at `https://<host>:5010`.

## 2. Create a user for the application

In the UI, go to **Users** and click **Create**. Set:

- **Email**: an address for the application, such as `ota@example.com`
- **Password**: any password of at least 8 characters
- **Role**: **Submitter**

Click **Create**.

A Submitter sends messages and reads only those it sent. See [roles](../reference/api/index.md#roles).

## 3. Create an API token

Open the user, and in **API Tokens**, click **Create Token**. Name it after the application, set an expiry date or check **No expiry**, and click **Create**.

Copy the token. It is not shown again.

## 4. Configure the application

Have the application send the token in the `Authorization` header of each request, and send a message:

```shell
curl https://<host>:5010/api/v1/messages \
  -H "Authorization: Bearer ellasmsc_..." \
  -H "Content-Type: application/json" \
  -d '{"from": "+15550001000", "to": "+15551230002", "text": "hello"}'
```

Then follow the message with `GET /api/v1/messages/{id}`. See the [Messages API](../reference/api/messages.md).

## Revoke the token

Open the user, and in **API Tokens**, delete the token. Requests with it get `401` at once.
