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
- `diameter` (object): Diameter, to the HSS, MMEs, SMSFs and IP-SM-GWs.
    - `address` (string): The IP address to listen on, advertised to peers. A specific address, not `0.0.0.0` or `::`.
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

The snap's configuration file is `/var/snap/ella-smsc/common/smsc.yaml`. After changing it, restart Ella SMSC:

```shell
sudo snap restart ella-smsc.smscd
```
