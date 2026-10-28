---
description: Reference of the measured capacity and performance of Ella SMSC.
---

# Performance

Measured short message capacity of Ella SMSC, with 10,000 subscribers on a 4 vCPU host.

## Short Messages

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 100 | 360,000 | 100% | 15 ms | 100% | 2 ms | 19 ms | 0.07 | 25 MiB |
| 400 | 1,440,000 | 100% | 17 ms | 100% | 2 ms | 19 ms | 0.23 | 43 MiB |
| 800 | 2,880,000 | 100% | 18 ms | 100% | 4 ms | 20 ms | 0.40 | 76 MiB |
| 1,200 | 4,320,000 | 100% | 20 ms | 100% | 7 ms | 23 ms | 0.56 | 125 MiB |
| 1,600 | 5,760,000 | 100% | 21 ms | 100% | 11 ms | 40 ms | 0.69 | 217 MiB |
| 2,000 | 7,200,000 | 100% | 23 ms | 100% | 3.1 s | 6.9 s | 0.69 | 218 MiB |

Maximum rate with a delivery time p99 under 100 ms: 1,600 messages per second.

## Storage

| Item | Value |
| ---- | ----- |
| Resident memory, idle | 15 MiB |
| Database size per message | About 490 bytes |
| Messages kept | All: Ella SMSC does not delete messages |

## Test Setup

| Item | Value |
| ---- | ----- |
| Version | Ella SMSC built from source, after v0.0.1, default configuration |
| Host | VM, 4 vCPU, 8 GB RAM, Ubuntu 26.04, Linux 7.0, on an Intel Core Ultra 7 265K |
| Open-file limit | 524,288 (systemd default) |
| Load generator | Separate VM, 10 vCPU, 10 GB RAM |
| Core network | Simulated MME and HSS, over SGd and S6c, over SCTP: answers at once |
| Subscribers | 10,000, each sending to another at random, all reachable |
| Short messages | SMS-SUBMIT of 8-bit text, 20 to 30 octets, without status report requests |
| Rates | Each held 30 s |
