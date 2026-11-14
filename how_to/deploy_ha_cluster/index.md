# Deploy a High Availability Cluster

Ella SMSC can be deployed as a high-availability cluster to provide redundancy and failover capabilities. See [High Availability](https://docs.ellanetworks.com/smsc/explanation/high_availability/index.md) for more information.

## Prerequisites

- Three hosts meeting the standard [system requirements](https://docs.ellanetworks.com/smsc/reference/system_reqs/index.md).
- Ella SMSC installed on each host via the [Install](https://docs.ellanetworks.com/smsc/how_to/install/index.md) guide, with a `cluster` block in the [configuration file](https://docs.ellanetworks.com/smsc/reference/config_file/index.md).

## 1. Start node 1

Start node 1, open a browser at the API address and initialize it normally.

## 2. Create a join token for node 2

Still in node 1, open the **Cluster** page, click **Add Node**, click **Create Token**, then copy the token.

## 3. Start node 2

Start node 2, open a browser at the API address and click **Join an existing cluster instead**. Paste the token and click **Join**.

## 4. Add node 3

Repeat steps 2 and 3 for node 3.

## 5. Connect the Diameter peers

Connect every Diameter peer, such as Ella Core, the HSS or Ella IMS, to every node, with each node's address and Diameter host. The **Cluster** page lists each node's **Diameter Host**. Or connect the nodes and the peers through a Diameter routing agent, as in [Connect through Diameter Agents](https://docs.ellanetworks.com/smsc/how_to/connect_through_diameter_agents/index.md).

## 6. Verify

On the leader's **Cluster** page, all three nodes appear as **Healthy**.

## Prerequisites

- A Kubernetes cluster with three nodes meeting the standard [system requirements](https://docs.ellanetworks.com/smsc/reference/system_reqs/index.md).

## 1. Deploy

```
git clone --depth 1 https://github.com/ellanetworks/smsc.git
kubectl create namespace ella
kubectl create secret generic ella-smsc-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
kubectl apply -k smsc/k8s/ha
```

## 2. Initialize

```
kubectl port-forward -n ella pod/ella-smsc-0 5010:5010
```

Open `http://localhost:5010` and create the first user.

## 3. Connect the Diameter peers

Connect every Diameter peer to every pod, at `ella-smsc-<n>.ella-smsc-internal.ella.svc.cluster.local:3868`, with the pod's Diameter host. The **Cluster** page lists each node's **Diameter Host**.

## 4. Verify

On the leader's **Cluster** page, all three nodes appear as **Healthy**.
