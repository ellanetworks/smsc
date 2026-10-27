---
description: Step-by-step instructions to install Ella SMSC.
---

# Install

Choose one of the installation methods below.

=== "Snap (Recommended)"

    Install the Ella SMSC snap:

    ```shell
    sudo snap install ella-smsc
    ```

    Configure Ella SMSC:

    ```shell
    sudo vim /var/snap/ella-smsc/common/smsc.yaml
    ```

    Start Ella SMSC:

    ```shell
    sudo snap start --enable ella-smsc.smscd
    ```

=== "Source"

    Install the required dependencies:

    ```shell
    sudo snap install go --channel=1.26/stable --classic
    sudo snap install node --channel=24/stable --classic
    sudo apt update
    sudo apt -y install gcc
    ```

    Clone the Ella SMSC repository:

    ```shell
    git clone https://github.com/ellanetworks/smsc.git
    cd smsc
    ```

    Build the frontend:

    ```shell
    npm install --prefix ui
    npm run build --prefix ui
    ```

    Build Ella SMSC:

    ```shell
    go build -o smsc -ldflags "-s -w -X github.com/ellanetworks/smsc/version.GitCommit=$(git rev-parse HEAD)" ./cmd/smsc
    ```

    Configure Ella SMSC:

    ```shell
    vim smsc.yaml
    ```

    Start Ella SMSC:

    ```shell
    ./smsc --config smsc.yaml
    ```

=== "Docker"

    Create a new directory:

    ```shell
    mkdir ella-smsc
    cd ella-smsc
    ```

    Copy the following file into this directory:

    ```yaml title="docker-compose.yaml"
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

    ```shell
    docker compose up -d
    ```

=== "Kubernetes"

    Ensure your Kubernetes cluster is running with the [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni) installed, and the `sctp` kernel module loaded on its nodes.

    Download the Ella SMSC manifests:

    ```shell
    git clone --depth 1 https://github.com/ellanetworks/smsc.git
    ```

    Edit `smsc/k8s/smsc-diameter-nad.yaml` to set `master` to the host interface that connects to your core, and set the Diameter address. Use the same address for `diameter.address` in `smsc/k8s/smsc-configmap.yaml`.

    Deploy Ella SMSC:

    ```shell
    kubectl create namespace ella
    kubectl apply -k smsc/k8s
    ```

    Ella SMSC's UI and API are on the `ella-smsc` NodePort Service.

Ella SMSC's UI and API are at `http://<host-ip>:5010`.

See the [configuration reference](../reference/config_file.md) for all options.
