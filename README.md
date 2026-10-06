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

- [Ella Core](https://github.com/ellanetworks/core) deployed with MCC `001`, MNC `01`
- Two phones attached to the network
- A Linux host for the SMSC, reachable from Ella Core, e.g. `192.0.2.10`

### 1. Install the SMSC

```sh
sudo snap install ella-smsc --channel=edge
```

Edit the configure file at `/var/snap/ella-smsc/common/smsc.yaml` to match your network. For example:

```yaml
logging:
  level: info
db:
  path: /var/snap/ella-smsc/common/smsc.db
api:
  address: 192.0.2.10
  port: 5010
diameter:
  address: 192.0.2.10
  port: 3868
```

```sh
sudo snap start --enable ella-smsc.smscd
```

### 2. Access the SMSC UI

Open `http://192.0.2.10:5010`.

Open the **Cores** page. You should not see any cores connected yet.

### 3. Connect Ella Core

In the Ella Core UI, go to **Operator** > **SMS**, click the edit icon, and set:

- **SMSC Address**: `192.0.2.10`
- **SMSC Port**: `3868`
- **SMS Number**: `+15550001111`

Click **Update**. Set the switch to **ON**.

**SMSC Link** shows **Connected**.

In the Ella SMSC UI, go to **Cores**. The Ella Core peer shows **open** with **HSS** set to **yes**.

### 4. Give subscribers an MSISDN

In the Ella Core UI, for each of the two subscribers:

- Go to **Subscribers** and open the subscriber.
- In **Provisioning**, click the edit icon next to **MSISDN**.
- Enter `+15551230001` for the first subscriber and `+15551230002` for the second. Click **Update**.

### 5. Set the SMSC number on the SIM cards

On each SIM card, set the SMSC number to `+15550000000`.

### 6. Send an SMS between phones

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

#### Snap

```sh
snapcraft pack
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
