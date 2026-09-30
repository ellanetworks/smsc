# Ella SMSC (beta)

SMS service centre for private cellular networks.

> [!WARNING]
> - SMS messages are stored in plain text.
> - The API and configuration may change without notice.

## Key Features

- SMS over Diameter (SCTP)
  - SGd: MO/MT forwarding with the MME
  - S6c: routing info, delivery status reports, and alerts with the HSS
- Store-and-forward with retries and validity periods
- Delivery attempt history with failure causes
- HTTP API
- Web UI
- SQLite storage

## Getting Started

### Prerequisites

- Ella Core deployed with MCC `001`, MNC `01`
- Two phones attached to the network
- A Linux host for the SMSC, reachable from Ella Core, e.g. `192.0.2.10`
- Go 1.26, Node 24

### 1. Build the SMSC

```sh
sudo modprobe sctp
npm ci --prefix ui && npm run build --prefix ui
go build -o smsc ./cmd/smsc
```

### 2. Configure the SMSC

Create `smsc.yaml`:

```yaml
db:
  path: smsc.db
service_centre:
  address: "15550000000"
diameter:
  origin_host: smsc.example.org
  origin_realm: example.org
  address: 192.0.2.10
  port: 3868
hss:
  realm: epc.mnc001.mcc001.3gppnetwork.org
numbering:
  country_code: "1"
api:
  address: 192.0.2.10
  port: 5010
```

### 3. Start the SMSC

```sh
./smsc --config smsc.yaml
```

### 4. Connect Ella Core

In the Ella Core UI, go to **Operator** > **SMS**, click the edit icon, and set:

- **SMSC Address**: `192.0.2.10`
- **SMSC Port**: `3868`
- **SMS Number**: `+15550001111`

Click **Update**. Set the switch to **ON**.

**SMSC Link** shows **Connected**.

### 5. Open the SMSC UI

Open `http://192.0.2.10:5010`.

In **Diameter**, **HSS** shows **reachable** and the Ella Core peer shows **open**.

### 6. Give subscribers an MSISDN

In the Ella Core UI, for each of the two subscribers:

- Go to **Subscribers** and open the subscriber.
- In **Provisioning**, click the edit icon next to **MSISDN**.
- Enter `+15551230001` for the first subscriber and `+15551230002` for the second. Click **Update**.

### 7. Set the SMSC number on the SIM cards

On each SIM card, set the SMSC number to `+15550000000`.

### 8. Send an SMS between phones

From the first phone, send `hello` to `+15551230002`.

The second phone receives `hello`.

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

### Test

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
