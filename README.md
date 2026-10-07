# Ella SMSC (beta)

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

<p align="center">
  <img src="ui/public/logo-mark.svg" alt="Ella SMSC Logo" width="120"/>
</p>

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

- [Ella Core](https://github.com/ellanetworks/core) running with PLMN `00101`
- Two phones attached to the network

### 1. Install the SMSC

Connect to the host running Ella Core and install the SMSC snap:

```sh
sudo snap install ella-smsc
```

Start the SMSC:

```sh
sudo snap start --enable ella-smsc.smscd
```

### 2. Access the SMSC UI

Open your browser at `http://<host-ip>:5010`.

The **Cores** page shows no connected cores yet.

### 3. Connect Ella Core

In the Ella Core UI, go to **Operator** and scroll to the **SMS** section.

Click **Add Service Center** and set:

- **Address**: `127.0.0.1`
- **Numbers**: `+15550000000`

Click **Add**. Set the switch to **SMS is ON**.

The service center shows **Connected**.

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
