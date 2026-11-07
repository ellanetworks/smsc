# Send Messages from an Application

This guide gives an application, such as an OTA platform, an API token to send messages with.

## 1. Create a user for the application

In the UI, go to **Users** and click **Create**. Set:

- **Email**: an address for the application, such as `ota@example.com`
- **Password**: any password of at least 8 characters
- **Role**: **Submitter**

Click **Create**.

## 2. Create an API token

Open the user, and in **API Tokens**, click **Create Token**. Name it after the application, check **No expiry**, and click **Create**.

Copy the token. It is not shown again.

## 3. Configure the application

Send a message with the token:

```
curl http://<host-ip>:5010/api/v1/messages \
  -H "Authorization: Bearer ellasmsc_..." \
  -H "Content-Type: application/json" \
  -d '{"from": "+15550001000", "to": "+15551230002", "text": "hello"}'
```

See the [Messages API](https://docs.ellanetworks.com/smsc/reference/api/messages/index.md).
