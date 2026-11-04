---
description: Reference of the measured capacity and performance of Ella SMSC.
---

# Performance

Measured short message capacity of Ella SMSC, with 100,000 subscribers on a 4 vCPU host.

## Short Messages from Phones

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 100 | 360,000 | 100% | 27 ms | 100% | 3 ms | 33 ms | 0.09 | 36 MiB |
| 400 | 1,440,000 | 100% | 32 ms | 100% | 6 ms | 35 ms | 0.28 | 55 MiB |
| 800 | 2,880,000 | 100% | 22 ms | 100% | 6 ms | 26 ms | 0.54 | 87 MiB |
| 1,000 | 3,600,000 | 100% | 19 ms | 100% | 7 ms | 22 ms | 0.63 | 130 MiB |
| 1,200 | 4,320,000 | 100% | 22 ms | 100% | 9 ms | 31 ms | 0.68 | 174 MiB |
| 1,600 | 5,760,000 | 100% | 25 ms | 100% | 4.0 s | 7.6 s | 0.73 | 228 MiB |
| 2,000 | 7,200,000 | 100% | 113 ms | 100% | 18.3 s | 26.8 s | 0.57 | 231 MiB |

Maximum rate with a delivery time p99 under 100 ms: 1,200 messages per second.

## Short Messages from ESMEs

Submission time: from the `submit_sm` to its `submit_sm_resp`, the message accepted and stored. Delivery time: from the `submit_sm` to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### Without Delivery Receipts

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 100 | 360,000 | 100% | 27 ms | 100% | 3 ms | 34 ms | 0.09 | 34 MiB |
| 400 | 1,440,000 | 100% | 29 ms | 100% | 4 ms | 32 ms | 0.30 | 35 MiB |
| 800 | 2,880,000 | 100% | 19 ms | 100% | 6 ms | 22 ms | 0.52 | 35 MiB |
| 1,000 | 3,600,000 | 100% | 20 ms | 100% | 8 ms | 23 ms | 0.62 | 36 MiB |
| 1,200 | 4,320,000 | 100% | 41 ms | 100% | 4.4 s | 10.4 s | 0.55 | 36 MiB |
| 1,600 | 5,760,000 | 100% | 66 ms | 100% | 22.5 s | 26.7 s | 0.40 | 36 MiB |
| 2,000 | 7,200,000 | 100% | 2.9 s | 100% | 33.4 s | 38.7 s | 0.36 | 37 MiB |

Maximum rate with a delivery time p99 under 100 ms: 1,000 messages per second.

### With Delivery Receipts

Receipt time: from the `submit_sm` to the `deliver_sm` of its delivery receipt.

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | Receipt success rate | Receipt time, mean | Receipt time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | -------------------- | ------------------ | ----------------- | ----------- | --------------- |
| 100 | 360,000 | 100% | 27 ms | 100% | 3 ms | 34 ms | 100% | 4 ms | 36 ms | 0.15 | 34 MiB |
| 200 | 720,000 | 100% | 28 ms | 100% | 3 ms | 31 ms | 100% | 4 ms | 34 ms | 0.30 | 35 MiB |
| 300 | 1,080,000 | 100% | 16 ms | 100% | 3 ms | 19 ms | 100% | 4 ms | 21 ms | 0.51 | 35 MiB |
| 400 | 1,440,000 | 100% | 17 ms | 100% | 4 ms | 20 ms | 100% | 6 ms | 23 ms | 0.77 | 36 MiB |
| 600 | 2,160,000 | 100% | 20 ms | 100% | 13 ms | 40 ms | 100% | 19 ms | 49 ms | 0.89 | 36 MiB |
| 800 | 2,880,000 | 100% | 37 ms | 100% | 7.9 s | 15.2 s | 100% | 7.9 s | 15.2 s | 0.82 | 37 MiB |
| 1,000 | 3,600,000 | 100% | 44 ms | 100% | 24.9 s | 39.9 s | 100% | 24.9 s | 39.9 s | 0.56 | 38 MiB |

Maximum rate with a delivery time and a receipt time p99 under 100 ms: 600 messages per second.

## Sustained Load

Short messages from phones at 400 per second for 10 minutes: 240,000 messages, from and to subscribers at random, so most of the 100,000 send and receive.

| Rate (/s) | Messages | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | -------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 400 | 240,000 | 100% | 18 ms | 100% | 3 ms | 20 ms | 0.31 | 177 MiB |

## Storage

| Item | Value |
| ---- | ----- |
| Resident memory, idle | 24 MiB |
| Database size per message from a phone | About 590 bytes |
| Database size per message from an ESME | About 600 bytes |
| Database size per message from an ESME, with a delivery receipt | About 720 bytes |
| Messages kept | 30 days after their final status, by default. See [Retention](api/retention.md). |

## Test Setup

| Item | Value |
| ---- | ----- |
| Version | Ella SMSC built from source, after v0.0.1, default configuration, with the SMPP listener |
| Host | VM, 4 vCPU, 8 GB RAM, Ubuntu 26.04, Linux 7.0, on an Intel Core Ultra 7 265K |
| Open-file limit | 524,288 (systemd default) |
| Load generator | Separate VM, 10 vCPU, 10 GB RAM |
| Core network | Simulated MME and HSS, over SGd and S6c, over SCTP: answers at once |
| Subscribers | 100,000, all reachable |
| Short messages from phones | SMS-SUBMIT of 8-bit text, 25 to 30 octets, without status report requests, each from a subscriber to another at random |
| ESME | 1, with 4 transceiver sessions, at most 10 `submit_sm` without a response per session |
| Short messages from ESMEs | `submit_sm` of 8-bit data, `data_coding` `0x04`, 12 to 17 octets, each to a subscriber at random. With delivery receipts: `registered_delivery` `0x01` |
| Rates | Each held 30 s, except the sustained load, in increasing order, on a new database for each table |
| Runs | 2 of each rate table, 1 of the sustained load. Each row is the run with the higher delivery time p99 |
| Success | Delivered, or receipted, within 60 s of the end of the rate |
