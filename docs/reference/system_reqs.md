---
description: System requirements for running Ella SMSC.
---

# System Requirements

Ella SMSC runs on Linux. It supports **amd64** and **arm64** architectures.

|  | Up to 1,000 subscribers | Up to 100,000 subscribers |
|------------|-------------|-------------|
| **CPU** | 1 core | 4 cores |
| **Memory** | 1 GB RAM | 8 GB RAM |
| **Disk** | 10 GB | 100 GB |
| **Operating System** | Ubuntu 26.04 LTS | Ubuntu 26.04 LTS |
| **Kernel** | SCTP, for Diameter | SCTP, for Diameter |

Each column is sized for 20 short messages per subscriber per day, kept for 30 days, with at least 7 times the busiest hour's rate to spare. The 100,000 subscriber host is the one of the [performance reference](performance.md).

## Kubernetes

Ella SMSC should work on most Kubernetes distributions. It has been validated on **EKS**, **GKE**, **OpenShift**, **RKE2**, and **kind**.

| Requirement | Value |
|-------------|-------|
| **Kubernetes** | 1.30 or later |
| **Helm** | 3.8 or later |
| **Nodes** | Meet the host requirements above |
| **Diameter network** | A CNI plugin that supports SCTP. For `diameter.networkAttachment`, [Multus](https://github.com/k8snetworkplumbingwg/multus-cni) with the `macvlan` [CNI plugin](https://github.com/containernetworking/plugins) and an IPAM plugin |
| **Pod Security** | `restricted` |
| **Storage** | A default StorageClass with `ReadWriteOnce` volumes, one per pod |
| **High availability** | Three nodes |
