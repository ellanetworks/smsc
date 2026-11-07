---
description: A hands-on tutorial to run Ella SMSC as a high-availability cluster on Google Kubernetes Engine.
---

# Run a High Availability Cluster on GKE

In this tutorial, you run Ella SMSC as a three-node cluster on Google Kubernetes Engine (GKE), and watch it survive the loss of a node and a rolling restart.

## Pre-requisites

- A Google Cloud project with billing and the Kubernetes Engine API enabled.
- The `gcloud` CLI with the `gke-gcloud-auth-plugin` component, logged in to that project.
- `kubectl` and `git`.

## 1. Create a GKE cluster

```shell
gcloud container clusters create ella-smsc \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 3 \
  --image-type UBUNTU_CONTAINERD \
  --enable-dataplane-v2
gcloud container clusters get-credentials ella-smsc --zone us-central1-a
```

## 2. Deploy Ella SMSC

```shell
git clone --depth 1 https://github.com/ellanetworks/smsc.git
kubectl create namespace ella
kubectl create secret generic ella-smsc-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
kubectl apply -k smsc/k8s/ha
kubectl get pods -n ella --watch
```

Wait until the three pods are `Running`, then press `Ctrl+C`.

## 3. Initialize Ella SMSC

```shell
kubectl port-forward -n ella pod/ella-smsc-0 5010:5010
```

Open `http://localhost:5010` and create the first user. Then stop the port-forward with `Ctrl+C`.

## 4. Look at the cluster

Expose the UI on a load balancer:

```shell
kubectl patch service ella-smsc -n ella -p '{"spec":{"type":"LoadBalancer"}}'
kubectl get service ella-smsc -n ella --watch
```

Wait until `EXTERNAL-IP` shows an address, then press `Ctrl+C`. Open `http://<EXTERNAL-IP>:5010` and log in. On the **Cluster** page, the three nodes appear as **Healthy**, each with its own **Diameter Host**. Note the leader.

## 5. Lose a node

Delete the leader's pod:

```shell
kubectl delete pod -n ella ella-smsc-<n>
```

On the **Cluster** page, another node becomes the leader. The pod comes back, and its node returns to **Healthy** with the same **Diameter Host**.

## 6. Restart the nodes one at a time

```shell
kubectl rollout restart statefulset/ella-smsc -n ella
kubectl rollout status statefulset/ella-smsc -n ella
```

On the **Cluster** page, the nodes restart one at a time, and all three return to **Healthy**.

## 7. Clean up

```shell
kubectl delete -k smsc/k8s/ha
kubectl delete pvc -n ella --all
kubectl wait --for=delete pv --all --timeout=5m
gcloud container clusters delete ella-smsc --zone us-central1-a
```
