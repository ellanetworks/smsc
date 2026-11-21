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
    stop_grace_period: 50s
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

Ensure your Kubernetes cluster meets the [Kubernetes requirements](https://docs.ellanetworks.com/smsc/reference/system_reqs/#kubernetes).

Create the namespace:

```
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=restricted
```

Deploy Ella SMSC:

```
helm install ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella
```

Open the UI and API on your machine:

```
kubectl port-forward -n ella service/ella-smsc 5010:5010
```

Diameter is at `ella-smsc-diameter.ella.svc.cluster.local:3868`. See the [chart reference](https://github.com/ellanetworks/smsc/tree/main/charts/ella-smsc) for all values, such as Diameter on a Multus network and SMPP.

Ella SMSC's UI and API are at `http://<host-ip>:5010`. Open the UI and create the first user.

See the [configuration reference](https://docs.ellanetworks.com/smsc/reference/config_file/index.md) for all options.
