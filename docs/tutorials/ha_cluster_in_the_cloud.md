---
description: A hands-on tutorial to run Ella SMSC as a high-availability cluster on managed Kubernetes in Google Cloud or AWS.
---

# Run a High Availability Cluster in the Cloud

In this tutorial, you run Ella SMSC as a three-node cluster on managed Kubernetes, and watch it survive the loss of a node and a rolling restart.

## Pre-requisites

=== "Google Cloud"

    - A Google Cloud project with billing and the Kubernetes Engine API enabled.
    - The `gcloud` CLI with the `gke-gcloud-auth-plugin` component, logged in to that project.
    - `kubectl` and `git`.

=== "AWS"

    - An AWS account.
    - The `aws` CLI, logged in to that account.
    - `eksctl`, `kubectl` and `git`.

## 1. Create a Kubernetes cluster

=== "Google Cloud"

    ```shell
    gcloud container clusters create ella-smsc \
      --zone us-central1-a \
      --machine-type e2-standard-2 \
      --num-nodes 3 \
      --image-type UBUNTU_CONTAINERD \
      --enable-dataplane-v2
    gcloud container clusters get-credentials ella-smsc --zone us-central1-a
    ```

=== "AWS"

    Create `cluster.yaml`:

    ```yaml
    apiVersion: eksctl.io/v1alpha5
    kind: ClusterConfig
    metadata:
      name: ella-smsc
      region: us-east-1
    availabilityZones: [us-east-1a, us-east-1b, us-east-1c]
    addons:
      - name: vpc-cni
        configurationValues: '{"enableNetworkPolicy":"true"}'
      - name: eks-pod-identity-agent
      - name: aws-ebs-csi-driver
        useDefaultPodIdentityAssociations: true
        configurationValues: '{"defaultStorageClass":{"enabled":true}}'
    managedNodeGroups:
      - name: ella-smsc
        instanceType: t3.medium
        desiredCapacity: 3
    ```

    Create the cluster:

    ```shell
    eksctl create cluster -f cluster.yaml
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

Open `http://localhost:5010` and create the first user.

## 4. Look at the cluster

Open the **Cluster** page. It names the leader. Only the leader shows the health of the nodes: if `ella-smsc-0` is not the leader, stop the port-forward with `Ctrl+C`, and start it again on the leader's pod:

```shell
kubectl port-forward -n ella pod/ella-smsc-<n> 5010:5010
```

On the leader's **Cluster** page, the three nodes appear as **Healthy**, each with its own **Diameter Host**.

## 5. Lose a node

In another terminal, delete the leader's pod:

```shell
kubectl delete pod -n ella ella-smsc-<n>
```

The port-forward stops with the pod. Start it again on another pod, and open the **Cluster** page: another node is the leader. Open the new leader's page as in step 4. The deleted pod comes back, and its node returns to **Healthy** with the same **Diameter Host**.

## 6. Restart the nodes one at a time

Stop the port-forward, then:

```shell
kubectl rollout restart statefulset/ella-smsc -n ella
kubectl rollout status statefulset/ella-smsc -n ella
```

Open the leader's **Cluster** page as in step 4: all three nodes are **Healthy**.

## 7. Clean up

```shell
kubectl delete -k smsc/k8s/ha
kubectl delete pvc -n ella --all
kubectl wait --for=delete pv --all --timeout=5m
```

=== "Google Cloud"

    ```shell
    gcloud container clusters delete ella-smsc --zone us-central1-a
    ```

=== "AWS"

    ```shell
    eksctl delete cluster -f cluster.yaml --wait
    ```
