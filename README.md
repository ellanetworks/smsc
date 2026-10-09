# Ella SMSC (beta)

<p align="center">
  <img src="ui/public/logo-mark.svg" alt="Ella SMSC Logo" width="120"/>
</p>

[![ella-smsc](https://snapcraft.io/ella-smsc/badge.svg)](https://snapcraft.io/ella-smsc)

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

## Key Features

- **SMS for your private network**: Subscribers send and receive text messages from their phones.
- **4G & 5G Compliant**: Connects to [Ella Core][ella-core], or other 3GPP-compliant cores, with the SGd and S6c Diameter interfaces over SCTP. Messages are stored and forwarded, retried, expired after their validity period, and delivered as soon as a phone becomes reachable again.
- **All-in-One**: A single application with an embedded SQLite database, a web UI, and an HTTP API. Install it in one command.
- **Observable**: Prometheus metrics.
- **Source Available**: Ella SMSC is available under the Business Source License 1.1 (BUSL-1.1).

## Getting Started

### Prerequisites

- [Ella Core][ella-core] running with PLMN `00101`
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

- **Diameter Identity**: `smsc.node.epc.mnc001.mcc001.3gppnetwork.org`
- **Address**: `127.0.0.1`
- **Numbers**: `+15550000000`

Click **Add**.

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
npm install --prefix ui
npm run build --prefix ui
go build -o smsc -ldflags "-s -w -X github.com/ellanetworks/smsc/version.GitCommit=$(git rev-parse HEAD)" ./cmd/smsc
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

### Metrics

Prometheus metrics, served at `GET /api/v1/metrics`.

| Metric | Type | Description |
| --- | --- | --- |
| `ellasmsc_build_info` | Gauge | Always 1; the version and revision of the running build are in its labels |
| `ellasmsc_messages_received_total` | Counter | Short messages received, one per part, by origin (`mobile`, `api`) and result (`accepted`, `rejected`, `error`) |
| `ellasmsc_messages_completed_total` | Counter | Short messages that reached a final status, by status (`delivered`, `failed`, `expired`) |
| `ellasmsc_messages_pending` | Gauge | Short messages waiting for delivery, by state (`due`: to be delivered now or being delivered; `waiting`: for a retry or for the phone to be reachable) |
| `ellasmsc_message_delivery_duration_seconds` | Histogram | Time from submission to delivery of the short messages delivered, including the time the phone was unreachable |
| `ellasmsc_hss_peers` | Gauge | Connected HSSs that the SMSC can route messages through; at 0, messages to phones wait |
| `ellasmsc_peer_requests_total` | Counter | Requests to the HSS and the MME or AMF, by interface (`s6c`, `sgd`) and result (`success`, `failure`, `absent_user`, `error`, `timeout`) |
| `ellasmsc_peer_request_duration_seconds` | Histogram | How long requests to the HSS and the MME or AMF take, by interface |
| `ellasmsc_database_query_duration_seconds` | Histogram | How long database calls take, including the wait for the connection |
| `ellasmsc_database_query_errors_total` | Counter | Database calls that failed |
| `ellasmsc_database_storage_bytes` | Gauge | Size of the database on disk, by file (`main`, `wal`) |
| `go_*` | Gauge, Counter, Summary | Go runtime health: goroutines, heap, garbage-collection pauses |
| `process_*` | Gauge, Counter | Process health: memory, CPU, open file descriptors, start time |

[ella-core]: https://github.com/ellanetworks/core
