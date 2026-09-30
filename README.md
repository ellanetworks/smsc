# Ella SMSC (beta)

SMS service centre for private cellular networks.

!!! warning
    This project is in beta.
      - The SMS messages are stored plain-text.
      - The API and configuration may change without notice.

## Features

- SMS over Diameter (SCTP)
  - SGd: MO/MT forwarding with the MME
  - S6c: routing info, delivery status reports, and alerts with the HSS
- Store-and-forward with retries and validity periods
- Delivery attempt history with failure causes
- HTTP API
- Web UI
- SQLite storage

## How-to Guides

### Build

#### From source

```sh
npm ci --prefix ui && npm run build --prefix ui
go build -o smsc ./cmd/smsc
```

#### Container Image

```sh
rockcraft pack
```

### Run

```sh
./smsc --config smsc.yaml
```

## Test

```sh
sudo modprobe sctp
go test ./...
npm test --prefix ui
```

## Reference

### Configuration File

See [`smsc.yaml`](smsc.yaml).

### API

[`openapi.yaml`](internal/api/openapi.yaml), served at `GET /api/v1/openapi.yaml`.
