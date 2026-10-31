# Install

Choose one of the installation methods below.

Install the Ella SMSC snap:

```
sudo snap install ella-smsc
```

Configure Ella SMSC:

```
sudo vim /var/snap/ella-smsc/common/smsc.yaml
```

Start Ella SMSC:

```
sudo snap start --enable ella-smsc.smscd
```

Install the required dependencies:

```
sudo snap install go --channel=1.27/stable --classic
sudo snap install node --channel=24/stable --classic
sudo apt update
sudo apt -y install gcc
```

Clone the Ella SMSC repository:

```
git clone https://github.com/ellanetworks/smsc.git
cd smsc
```

Build the frontend:

```
npm install --prefix ui
npm run build --prefix ui
```

Build Ella SMSC:

```
go build -o smsc -ldflags "-s -w -X github.com/ellanetworks/smsc/version.GitCommit=$(git rev-parse HEAD)" ./cmd/smsc
```

Configure Ella SMSC:

```
vim smsc.yaml
```

Start Ella SMSC:

```
./smsc --config smsc.yaml
```

Create a new directory:

```
mkdir ella-smsc
cd ella-smsc
```

Copy the following file into this directory:

docker-compose.yaml

```
configs:
  ella_smsc_config:
    content: |
      logging:
        level: info
      db:
        path: /data/smsc.db
      api:
        address: 0.0.0.0
        port: 5010
      diameter:
        address: 10.3.0.7

services:
  ella-smsc:
    image: ghcr.io/ellanetworks/ella-smsc:latest
    configs:
      - source: ella_smsc_config
        target: /smsc.yaml
    entrypoint: /bin/smsc --config /smsc.yaml
    restart: unless-stopped
    network_mode: host
    volumes:
      - ella-smsc-data:/data

volumes:
  ella-smsc-data:
```

Set `diameter.address` to an address of the host that the core can reach.

Start the Ella SMSC container:

```
docker compose up -d
```

Ensure your Kubernetes cluster is running with the [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni) installed, and the `sctp` kernel module loaded on its nodes.

Download the Ella SMSC manifests:

```
git clone --depth 1 https://github.com/ellanetworks/smsc.git
```

Edit `smsc/k8s/smsc-diameter-nad.yaml` to set `master` to the host interface that connects to your core, and set the Diameter address. Use the same address for `diameter.address` in `smsc/k8s/smsc-configmap.yaml`.

Deploy Ella SMSC:

```
kubectl create namespace ella
kubectl apply -k smsc/k8s
```

Ella SMSC's UI and API are on the `ella-smsc` NodePort Service.

To enable SMPP, uncomment the `smpp` block in `smsc/k8s/smsc-configmap.yaml`, or `smsc/k8s/pod-network/smsc-configmap.yaml`. SMPP is on the pod network, port `2775` of the `ella-smsc` Service, in both variants. The Service has `externalTrafficPolicy: Local`, so the SMSC sees the ESMEs' own addresses, which their allowed networks are checked against. Terminate TLS in the SMSC pod, with `smpp.tls` and a Secret mounted in the pod: a load balancer, sidecar or ingress that terminates TLS hides the client address.

Ella SMSC's UI and API are at `http://<host-ip>:5010`. Open the UI and create the first user.

With an [`smpp` block](https://docs.ellanetworks.com/smsc/reference/config_file/index.md) in the configuration file, Ella SMSC listens for SMPP 3.4 ESMEs, such as OTA platforms, on port `2775`. See [SMPP](https://docs.ellanetworks.com/smsc/reference/smpp/index.md).

See the [configuration reference](https://docs.ellanetworks.com/smsc/reference/config_file/index.md) for all options.
