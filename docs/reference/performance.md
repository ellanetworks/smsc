---
description: Reference of the measured capacity and performance of Ella SMSC.
---

# Performance

Measured short message capacity of Ella SMSC, with 100,000 subscribers, on 1 node and on a 3-node [cluster](../explanation/high_availability.md) of 4 vCPU hosts.

Ella SMSC sustains about 1,000 short messages per second from phones, on 1 node or a 3-node cluster, with every message delivered and a delivery p99 under about 550 ms.

## Short Messages from Phones

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### 1 Node

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- |
| 500 | 1,800,000 | 100% | 59 ms | 100% | 44 ms | 111 ms | 1.15 |
| 1,000 | 3,600,000 | 100% | 182 ms | 100% | 215 ms | 541 ms | 1.75 |
| 1,500 | 5,400,000 | 91.1% | 332 ms | 100% | 17.8 s | 29.2 s | 1.70 |

### 3 Nodes

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU of the leader (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ------------------------- |
| 500 | 1,800,000 | 100% | 85 ms | 100% | 60 ms | 143 ms | 1.24 |
| 1,000 | 3,600,000 | 100% | 233 ms | 100% | 182 ms | 443 ms | 1.94 |
| 1,500 | 5,400,000 | 86.5% | 2.00 s | 100% | 15.6 s | 24.9 s | 1.84 |

Messages that are not accepted are answered with a temporary error, for the sender to retry.

## Short Messages from ESMEs

Submission time: from the `submit_sm` to its `submit_sm_resp`, the message accepted and stored. Delivery time: from the `submit_sm` to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### Without Delivery Receipts

| Nodes | Rate (/s) | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 1 | 500 | 100% | 50 ms | 100% | 41 ms | 91 ms | 1.14 | 87 MiB |
| 1 | 1,000 | 100% | 3.00 s | 100% | 1.77 s | 3.08 s | 1.61 | 169 MiB |
| 3 | 500 | 100% | 59 ms | 100% | 53 ms | 109 ms | 1.23 | 81 MiB |
| 3 | 1,000 | 100% | 1.41 s | 100% | 529 ms | 1.49 s | 1.75 | 243 MiB |

### With Delivery Receipts

Receipt time: from the `submit_sm` to the `deliver_sm` of its delivery receipt.

| Nodes | Rate (/s) | Submission time, p99 | Delivery time, mean | Delivery time, p99 | Receipt time, mean | Receipt time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | -------------------- | ------------------- | ------------------ | ------------------ | ----------------- | ----------- | --------------- |
| 1 | 300 | 45 ms | 43 ms | 88 ms | 78 ms | 140 ms | 1.06 | 63 MiB |
| 1 | 600 | 77 ms | 79 ms | 164 ms | 316 ms | 1.17 s | 1.48 | 109 MiB |
| 3 | 300 | 52 ms | 49 ms | 94 ms | 90 ms | 142 ms | 1.07 | 90 MiB |
| 3 | 600 | 87 ms | 80 ms | 169 ms | 315 ms | 1.52 s | 1.62 | 102 MiB |

Every message was submitted, delivered and receipted.

In a cluster, CPU and memory are those of the node the ESME is bound to.

## Test Setup

| Item | Value |
| ---- | ----- |
| Version | Ella SMSC built from source, after v0.0.1, default configuration, with the SMPP listener |
| Hosts | Google Cloud `e2-standard-4`: 4 vCPU, 16 GB RAM, 50 GB SSD persistent disk, Ubuntu 26.04, in one zone |
| Load generator | Separate `e2-standard-8` in the same zone |
| Core network | Simulated MME and HSS, over SGd and S6c, over SCTP, connected to every node: answers at once |
| Subscribers | 100,000, all reachable |
| Short messages from phones | SMS-SUBMIT of 8-bit text, 25 to 30 octets, without status report requests, each from a subscriber to another at random, spread over every node |
| ESME | 1, with 4 transceiver sessions to one node, at most 10 `submit_sm` without a response per session |
| Short messages from ESMEs | `submit_sm` of 8-bit data, `data_coding` `0x04`, 12 to 17 octets, each to a subscriber at random. With delivery receipts: `registered_delivery` `0x01` |
| Rates | Each held 60 s, in increasing order, on a new database for each table |
| Runs | 1 of each table |
| Success | Delivered, or receipted, within 60 s of the end of the rate |
