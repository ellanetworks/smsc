# Connect a Core at Another Site

This guide connects Ella SMSC, on Kubernetes or elsewhere, to a core, such as Ella Core, and an IMS at another site.

## Prerequisites

- **Ella SMSC**: deployed, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/smsc/how_to/deploy_ha_cluster/index.md).
- **The core and the IMS**: support SCTP for Diameter.

The examples use these values:

| Value         | Role                                       |
| ------------- | ------------------------------------------ |
| `10.3.0.0/24` | Diameter addresses of the core and the IMS |
| `10.4.0.0/14` | Pod range of the cluster                   |
| `10.2.0.53`   | Address of the cluster's DNS load balancer |
| `ella`        | Namespace of Ella SMSC                     |

## 1. Link the two sites

Set up a routed link, such as an IPsec VPN like [Google Cloud VPN](https://cloud.google.com/network-connectivity/docs/vpn/concepts/overview), between the core's site and the cluster's network, with no NAT.

Use address ranges that don't overlap between the two sites, nor with any other site on the same VPN. On the cluster's side, this includes the node subnets and the pod range.

## 2. Add the routes

- At the core's site, route `10.4.0.0/14` and `10.2.0.53` through the link.
- In the cluster's network, route `10.3.0.0/24` through the link. Include every address the core and the IMS send Diameter from.

## 3. Allow Diameter and DNS

- Allow SCTP port `3868` from `10.3.0.0/24` to `10.4.0.0/14`.
- Allow UDP and TCP port `53` from the core's site to `10.2.0.53`.

## 4. Resolve the cluster's names

Expose the cluster's DNS on an internal load balancer. On GKE:

```
apiVersion: v1
kind: Service
metadata:
  name: kube-dns-site
  namespace: kube-system
  annotations:
    networking.gke.io/load-balancer-type: Internal
spec:
  type: LoadBalancer
  selector:
    k8s-app: kube-dns
  ports:
    - name: dns-udp
      protocol: UDP
      port: 53
    - name: dns-tcp
      protocol: TCP
      port: 53
```

Get the load balancer's address, e.g. `10.2.0.53`:

```
kubectl -n kube-system get service kube-dns-site
```

At the core's site, forward `cluster.local` to `10.2.0.53`.

Check that the core's site resolves `ella-smsc-0.ella-smsc-internal.ella.svc.cluster.local`.

## 5. Add Ella SMSC to the core and the IMS

Follow [Integrate with Ella Core](https://docs.ellanetworks.com/smsc/how_to/integrate_with_ella_core/index.md), step 3. For the address of each node, enter its host name, e.g. `ella-smsc-0.ella-smsc-internal.ella.svc.cluster.local`, and enter its **Diameter Host** from the **Cluster** page.

For SMS over IP, follow [Integrate with Ella IMS](https://docs.ellanetworks.com/smsc/how_to/integrate_with_ella_ims/index.md), and add each node as an SGd peer of the IP-SM-GW the same way.

## 6. Verify

In the Ella SMSC UI, go to **Diameter**. Under **Connected Peers**, the core shows **open**, with **HSS** set to **yes**, and the IMS shows **open**, with **Applications** set to **SGd**.

Send an SMS between two phones. On the **Messages** page, it shows **delivered**.
