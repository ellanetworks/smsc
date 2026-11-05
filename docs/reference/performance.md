---
description: Reference of the measured capacity and performance of Ella SMSC.
---

# Performance

Measured short message capacity of Ella SMSC, with 10,000 subscribers, on 1 node and on a 3-node [cluster](../explanation/high_availability.md) of 4 vCPU hosts.

## Short Messages from Phones

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### 1 Node

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- |
| 500 | 1,800,000 | 100% | 66 ms | 100% | 48 ms | 124 ms | 1.18 |
| 1,000 | 3,600,000 | 100% | 161 ms | 100% | 153 ms | 381 ms | 1.74 |
| 1,500 | 5,400,000 | 91.0% | 313 ms | 100% | 11.1 s | 26.1 s | 1.73 |

### 3 Nodes

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU of the leader (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ------------------------- |
| 500 | 1,800,000 | 100% | 81 ms | 100% | 57 ms | 133 ms | 1.27 |
| 1,000 | 3,600,000 | 100% | 202 ms | 100% | 192 ms | 449 ms | 1.90 |
| 1,500 | 5,400,000 | 99.999% | 410 ms | 100% | 16.8 s | 38.2 s | 1.83 |

Messages that are not accepted are answered with a temporary error, for the sender to retry.

## Short Messages from ESMEs

Submission time: from the `submit_sm` to its `submit_sm_resp`, the message accepted and stored. Delivery time: from the `submit_sm` to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### Without Delivery Receipts

| Nodes | Rate (/s) | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 1 | 500 | 100% | 76 ms | 100% | 43 ms | 130 ms | 1.21 | 600 MiB |
| 1 | 1,000 | 100% | 126 ms | 100% | 94 ms | 200 ms | 1.74 | 613 MiB |
| 3 | 500 | 100% | 75 ms | 100% | 52 ms | 128 ms | 1.25 | 352 MiB |
| 3 | 1,000 | 100% | 452 ms | 100% | 173 ms | 531 ms | 1.87 | 541 MiB |

### With Delivery Receipts

Receipt time: from the `submit_sm` to the `deliver_sm` of its delivery receipt.

| Nodes | Rate (/s) | Submission time, p99 | Delivery time, mean | Delivery time, p99 | Receipt time, mean | Receipt time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | -------------------- | ------------------- | ------------------ | ------------------ | ----------------- | ----------- | --------------- |
| 1 | 300 | 62 ms | 35 ms | 127 ms | 60 ms | 165 ms | 1.24 | 610 MiB |
| 1 | 600 | 136 ms | 83 ms | 219 ms | 337 ms | 1.05 s | 1.54 | 663 MiB |
| 3 | 300 | 81 ms | 40 ms | 146 ms | 71 ms | 189 ms | 1.29 | 510 MiB |
| 3 | 600 | 157 ms | 82 ms | 240 ms | 194 ms | 494 ms | 1.76 | 522 MiB |

Every message was submitted, delivered and receipted.

In a cluster, CPU and memory are those of the node the ESME is bound to.

## Test Setup

| Item | Value |
| ---- | ----- |
| Version | Ella SMSC built from source, after v0.0.1, default configuration, with the SMPP listener |
| Hosts | Google Cloud `e2-standard-4`: 4 vCPU, 16 GB RAM, 50 GB SSD persistent disk, Ubuntu 26.04, in one zone |
| Load generator | Separate `e2-standard-8` in the same zone |
| Core network | Simulated MME and HSS, over SGd and S6c, over SCTP, connected to every node: answers at once |
| Subscribers | 10,000, all reachable |
| Short messages from phones | SMS-SUBMIT of 8-bit text, 25 to 30 octets, without status report requests, each from a subscriber to another at random, spread over every node |
| ESME | 1, with 4 transceiver sessions to one node, at most 10 `submit_sm` without a response per session |
| Short messages from ESMEs | `submit_sm` of 8-bit data, `data_coding` `0x04`, 12 to 17 octets, each to a subscriber at random. With delivery receipts: `registered_delivery` `0x01` |
| Rates | Each held 60 s, in increasing order, on a new database for each table |
| Runs | 1 of each table |
| Success | Delivered, or receipted, within 60 s of the end of the rate |
