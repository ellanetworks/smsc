---
description: Reference of the configuration file of Ella SMSC.
---

# Configuration File

Ella SMSC is configured using a YAML file. Start Ella SMSC with the `--config` flag to specify its path. Unknown keys are rejected. Changes take effect on restart.

The operator and delivery settings are not in this file: they are in the database, and set with the UI or the [API](api/index.md).

## Parameters

- `logging` (object, optional): The logging configuration.
    - `level` (string, optional): The log level: `debug`, `info`, `warn`, or `error`. Default `info`.
- `db` (object): The database configuration.
    - `path` (string): The path to the SQLite database file. Created if absent.
- `api` (object): The HTTP API and UI.
    - `address` (string): The IP address to listen on. `0.0.0.0` or `::` for all addresses.
    - `port` (int, optional): The TCP port to listen on. Default `5010`.
    - `tls` (object, optional): Serve over HTTPS. Default: plain HTTP.
        - `cert` (string): The path to the PEM certificate chain, the server's certificate first.
        - `key` (string): The path to the PEM private key.
    - `drain_delay` (duration, optional): How long Ella SMSC keeps serving after it is told to stop, with [`GET /api/v1/readyz`](api/status.md#check-readiness) failing. For example `5s`. Default `0s`.
- `diameter` (object): Diameter, to the HSS, MMEs, SMSFs and IP-SM-GWs.
    - `name` (string): The network interface to listen on, on all its addresses, advertised to peers. Either `name` or `address` must be set.
    - `address` (string): The IP address to listen on, advertised to peers. A specific address, not `0.0.0.0` or `::`. Either `name` or `address` must be set.
    - `port` (int, optional): The SCTP port to listen on. Default `3868`.

## Example

```yaml
logging:
  level: info
db:
  path: smsc.db
api:
  address: 0.0.0.0
  port: 5010
diameter:
  address: 127.0.0.1
  port: 3868
```

With the snap, the configuration file is `/var/snap/ella-smsc/common/smsc.yaml`.
