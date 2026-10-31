# Configuration File

Ella SMSC is configured using a YAML file. Start Ella SMSC with the `--config` flag to specify its path. Unknown keys are rejected. Changes take effect on restart.

The operator and delivery settings are not in this file: they are in the database, and set with the UI or the [API](https://docs.ellanetworks.com/smsc/reference/api/index.md).

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
  - `drain_delay` (duration, optional): How long Ella SMSC keeps serving after it is told to stop, with [`GET /api/v1/readyz`](https://docs.ellanetworks.com/smsc/reference/api/status/#check-readiness) failing. For example `5s`. Default `0s`.
- `diameter` (object): Diameter, to the HSS, MMEs, SMSFs and IP-SM-GWs.
  - `name` (string): The network interface to listen on, on all its addresses, advertised to peers. Either `name` or `address` must be set.
  - `address` (string): The IP address to listen on, advertised to peers. A specific address, not `0.0.0.0` or `::`. Either `name` or `address` must be set.
  - `port` (int, optional): The SCTP port to listen on. Default `3868`.
- `smpp` (object, optional): The [SMPP 3.4](https://docs.ellanetworks.com/smsc/reference/smpp/index.md) listener for ESMEs, such as OTA platforms. Without it, Ella SMSC does not listen for SMPP. The ESME accounts are in the database, managed with the [API](https://docs.ellanetworks.com/smsc/reference/api/esmes/index.md).
  - `address` (string): The IP address to listen on. `0.0.0.0` or `::` for all addresses.
  - `port` (int, optional): The TCP port to listen on. Default `2775`. Not the API's port.
  - `tls` (object, optional): Wrap each connection in TLS, 1.2 or later, from its first octet. SMPP 3.4 has no TLS: this is a common convention, which the ESME must support. Default: plain TCP, for a private link or a lab only.
    - `cert` (string): The path to the PEM certificate chain, the server's certificate first.
    - `key` (string): The path to the PEM private key.
- `telemetry` (object, optional): The [traces](https://docs.ellanetworks.com/smsc/reference/observability/#traces).
  - `enabled` (boolean, optional): Export traces. Default `false`.
  - `otlp_endpoint` (string): The `host:port` of an OTLP/gRPC collector, over plain gRPC. Required when `enabled` is `true`.

## Example

```
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
smpp:
  address: 0.0.0.0
  port: 2775
  tls:
    cert: /etc/smsc/smpp.pem
    key: /etc/smsc/smpp.key
telemetry:
  enabled: true
  otlp_endpoint: localhost:4317
```

With the snap, the configuration file is `/var/snap/ella-smsc/common/smsc.yaml`.
