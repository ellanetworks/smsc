# Ella SMSC Helm Chart

[Ella SMSC](https://docs.ellanetworks.com/smsc/) is an SMS service center for private 4G and 5G networks. This chart deploys Ella SMSC as a standalone node or as a high-availability cluster.

## Prerequisites

The cluster must meet the [Kubernetes requirements](https://docs.ellanetworks.com/smsc/reference/system_reqs/#kubernetes).

## Install

A standalone node, with Diameter on the pod network:

```bash
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=restricted
helm install ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella
```

A high-availability cluster:

```bash
kubectl create secret generic ella-smsc-cluster --namespace ella --from-literal=secret="$(openssl rand -base64 32)"
helm install ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella --set replicas=3
```

Diameter on a Multus network, with a static address:

```yaml
diameter:
  networkAttachment:
    enabled: true
    master: eth1
    ipam:
      type: static
      addresses:
        - address: 192.168.252.7/24
```

An SMPP listener with TLS:

```yaml
smpp:
  enabled: true
  tls:
    secretName: ella-smsc-smpp-tls
```

See [Install](https://docs.ellanetworks.com/smsc/how_to/install/) and [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/).

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `replicas` | `1` | Number of pods. `1` deploys a standalone node. `3` or more deploy a high-availability cluster, with one pod per Kubernetes node. |
| `cluster.secretName` | `<release>-cluster` | Secret that holds the cluster secret in its `secret` key, in high availability. |
| `config` | logging at `info` | [Configuration file](https://docs.ellanetworks.com/smsc/reference/config_file/) content, merged over the database, API, Diameter, SMPP and cluster settings that the chart sets. |
| `diameter.service.enabled` | `true` | Create the `<release>-diameter` Service for Diameter on a standalone node on the pod network. |
| `diameter.service.type` | `ClusterIP` | Type of the Diameter Service. |
| `diameter.service.annotations` | `{}` | Annotations of the Diameter Service. |
| `diameter.networkAttachment.enabled` | `false` | Attach the pods to a Multus network and use it for Diameter, instead of the pod network. |
| `diameter.networkAttachment.create` | `true` | Create a macvlan NetworkAttachmentDefinition. Set to `false` to use your own. |
| `diameter.networkAttachment.name` | `<release>-diameter` | Name of the NetworkAttachmentDefinition. |
| `diameter.networkAttachment.interface` | `diameter` | Name of the Diameter interface in the pod. |
| `diameter.networkAttachment.master` | `eth1` | Host interface that connects to the Diameter peers. |
| `diameter.networkAttachment.mode` | `bridge` | macvlan mode. |
| `diameter.networkAttachment.ipam` | `{}` | CNI IPAM configuration. Required when `create` is `true`. `static` IPAM supports one pod only. |
| `smpp.enabled` | `false` | Listen for SMPP on port `2775`, exposed by the `<release>` Service. |
| `smpp.tls.secretName` | | `kubernetes.io/tls` Secret with the SMPP certificate and key. Without it, SMPP is plain TCP. |
| `service.type` | `ClusterIP` | Type of the `<release>` Service, for the API on port `5010` and SMPP. |
| `service.externalTrafficPolicy` | | External traffic policy, when `service.type` is `NodePort` or `LoadBalancer`. |
| `service.annotations` | `{}` | Annotations of the `<release>` Service. |
| `ingress.enabled` | `false` | Create an Ingress that exposes the API and UI. |
| `ingress.className` | | IngressClass name. Empty uses the cluster default. |
| `ingress.host` | | Host name of the API and UI. Required when the Ingress is enabled. |
| `ingress.annotations` | `{}` | Ingress annotations, e.g. for cert-manager. |
| `ingress.tls` | `[]` | Ingress TLS entries, each with `secretName` and `hosts`. |
| `networkPolicy.enabled` | `true` | Create a NetworkPolicy that admits the API, Diameter and SMPP from anywhere, and the cluster port from the other pods only. |
| `serviceMonitor.enabled` | `false` | Create a Prometheus Operator ServiceMonitor for `/api/v1/metrics` on the `<release>` Service. |
| `serviceMonitor.interval` | `30s` | Scrape interval. |
| `serviceMonitor.labels` | `{}` | Extra ServiceMonitor labels. |
| `persistence.size` | `10Gi` | Size of each pod's data volume. |
| `persistence.storageClassName` | default StorageClass | StorageClass of the data volumes. |
| `resources` | 500m CPU, 256 MiB requested; 4 CPU, 512 MiB limit | Container resources. |
| `image.repository` | `ghcr.io/ellanetworks/ella-smsc` | Image repository. |
| `image.tag` | chart `appVersion` | Image tag. |
| `image.digest` | | Image digest. Takes precedence over `image.tag`. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Image pull secrets. |
| `serviceAccount.create` | `true` | Create a service account. |
| `serviceAccount.name` | `<release>` | Service account name. |
| `serviceAccount.annotations` | `{}` | Service account annotations. |
| `clusterDomain` | `cluster.local` | Kubernetes cluster domain, used in the cluster addresses of the pods. |
| `nodeSelector` | `{}` | Node selector. |
| `tolerations` | `[]` | Tolerations. |
| `affinity` | one pod per Kubernetes node in high availability | Affinity. Replaces the default anti-affinity. |
| `priorityClassName` | | Priority class. |
| `podAnnotations` | `{}` | Extra pod annotations. |
| `podLabels` | `{}` | Extra pod labels. |
| `nameOverride` | | Overrides the chart name. |
| `fullnameOverride` | | Overrides the resource name prefix. |
| `tests.image.repository` | `registry.access.redhat.com/ubi10/ubi-minimal` | Image of the `helm test` pod. |
| `tests.image.tag` | `10.2` | Tag of the `helm test` image. |
