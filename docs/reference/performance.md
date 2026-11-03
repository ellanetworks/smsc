---
description: Reference of the measured capacity and performance of Ella SMSC.
---

# Performance

Measured short message capacity of Ella SMSC, with 10,000 subscribers on a 4 vCPU host.

## Short Messages from Phones

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 100 | 360,000 | 100% | 18 ms | 100% | 2 ms | 23 ms | 0.08 | 35 MiB |
| 400 | 1,440,000 | 100% | 20 ms | 100% | 3 ms | 23 ms | 0.25 | 54 MiB |
| 800 | 2,880,000 | 100% | 22 ms | 100% | 5 ms | 25 ms | 0.44 | 87 MiB |
| 1,000 | 3,600,000 | 100% | 21 ms | 100% | 7 ms | 26 ms | 0.52 | 129 MiB |
| 1,200 | 4,320,000 | 100% | 26 ms | 100% | 10 ms | 40 ms | 0.60 | 172 MiB |
| 1,600 | 5,760,000 | 100% | 25 ms | 100% | 1.6 s | 3.2 s | 0.66 | 226 MiB |
| 2,000 | 7,200,000 | 100% | 37 ms | 100% | 7.9 s | 16.6 s | 0.60 | 230 MiB |

Maximum rate with a delivery time p99 under 100 ms: 1,000 messages per second.

## Short Messages from ESMEs

Submission time: from the `submit_sm` to its `submit_sm_resp`, the message accepted and stored. Delivery time: from the `submit_sm` to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### Without Delivery Receipts

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 100 | 360,000 | 100% | 16 ms | 100% | 2 ms | 21 ms | 0.09 | 33 MiB |
| 400 | 1,440,000 | 100% | 34 ms | 100% | 4 ms | 38 ms | 0.27 | 34 MiB |
| 800 | 2,880,000 | 100% | 23 ms | 100% | 6 ms | 29 ms | 0.46 | 35 MiB |
| 1,000 | 3,600,000 | 100% | 29 ms | 100% | 77 ms | 614 ms | 0.57 | 36 MiB |
| 1,200 | 4,320,000 | 100% | 33 ms | 100% | 68 ms | 461 ms | 0.59 | 35 MiB |
| 1,600 | 5,760,000 | 100% | 38 ms | 100% | 5.0 s | 11.1 s | 0.61 | 36 MiB |
| 2,000 | 7,200,000 | 100% | 42 ms | 100% | 8.9 s | 18.5 s | 0.58 | 36 MiB |

Maximum rate with a delivery time p99 under 100 ms: 800 messages per second.

### With Delivery Receipts

Receipt time: from the `submit_sm` to the `deliver_sm` of its delivery receipt.

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | Receipt success rate | Receipt time, mean | Receipt time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | -------------------- | ------------------ | ----------------- | ----------- | --------------- |
| 100 | 360,000 | 100% | 20 ms | 100% | 2 ms | 25 ms | 100% | 3 ms | 27 ms | 0.15 | 34 MiB |
| 200 | 720,000 | 100% | 23 ms | 100% | 3 ms | 26 ms | 100% | 4 ms | 29 ms | 0.30 | 34 MiB |
| 300 | 1,080,000 | 100% | 23 ms | 100% | 3 ms | 26 ms | 100% | 4 ms | 29 ms | 0.48 | 34 MiB |
| 400 | 1,440,000 | 100% | 25 ms | 100% | 6 ms | 30 ms | 100% | 8 ms | 36 ms | 0.75 | 35 MiB |
| 600 | 2,160,000 | 100% | 34 ms | 100% | 36 ms | 216 ms | 100% | 43 ms | 227 ms | 0.80 | 36 MiB |
| 800 | 2,880,000 | 100% | 31 ms | 100% | 3.5 s | 7.1 s | 100% | 3.5 s | 7.1 s | 0.75 | 36 MiB |
| 1,000 | 3,600,000 | 100% | 28 ms | 100% | 8.0 s | 14.4 s | 100% | 8.0 s | 14.4 s | 0.75 | 37 MiB |

Maximum rate with a delivery time and a receipt time p99 under 100 ms: 400 messages per second.

## Storage

| Item | Value |
| ---- | ----- |
| Resident memory, idle | 24 MiB |
| Database size per message from a phone | About 550 bytes |
| Database size per message from an ESME | About 550 bytes |
| Database size per message from an ESME, with a delivery receipt | About 660 bytes |
| Messages kept | 30 days after their final status, by default. See [Retention](api/retention.md). |

## Test Setup

| Item | Value |
| ---- | ----- |
| Version | Ella SMSC built from source, after v0.0.1, default configuration, with the SMPP listener |
| Host | VM, 4 vCPU, 8 GB RAM, Ubuntu 26.04, Linux 7.0, on an Intel Core Ultra 7 265K |
| Open-file limit | 524,288 (systemd default) |
| Load generator | Separate VM, 10 vCPU, 10 GB RAM |
| Core network | Simulated MME and HSS, over SGd and S6c, over SCTP: answers at once |
| Subscribers | 10,000, all reachable |
| Short messages from phones | SMS-SUBMIT of 8-bit text, 25 to 30 octets, without status report requests, each from a subscriber to another at random |
| ESME | 1, with 4 transceiver sessions, at most 10 `submit_sm` without a response per session |
| Short messages from ESMEs | `submit_sm` of 8-bit data, `data_coding` `0x04`, 12 to 17 octets, each to a subscriber at random. With delivery receipts: `registered_delivery` `0x01` |
| Rates | Each held 30 s, in increasing order, on a new database for each table |
| Runs | 2 of each table. Each row is the run with the higher delivery time p99 |
| Success | Delivered, or receipted, within 60 s of the end of the rate |
