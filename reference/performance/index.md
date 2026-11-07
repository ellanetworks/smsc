# Performance

Measured short message capacity of Ella SMSC, with 100,000 subscribers, on 1 node and on a 3-node [cluster](https://docs.ellanetworks.com/smsc/explanation/high_availability/index.md), each node on 4 CPU cores.

Ella SMSC sustains 2,000 short messages per second from phones, on 1 node or a 3-node cluster, with every message delivered and a delivery p99 under 120 ms.

## Short Messages from Phones

Submission time: from the MO-Forward-Short-Message request to its answer, the message accepted and stored. Delivery time: from the MO-Forward-Short-Message request to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### 1 Node

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ----------- |
| 500       | 1,800,000         | 100%                    | 33 ms                | 100%                  | 34 ms               | 59 ms              | 0.26        |
| 1,000     | 3,600,000         | 100%                    | 34 ms                | 100%                  | 32 ms               | 59 ms              | 0.40        |
| 1,500     | 5,400,000         | 100%                    | 39 ms                | 100%                  | 36 ms               | 68 ms              | 0.53        |
| 2,000     | 7,200,000         | 100%                    | 49 ms                | 100%                  | 41 ms               | 81 ms              | 0.68        |

### 3 Nodes

| Rate (/s) | Messages per hour | Submission success rate | Submission time, p99 | Delivery success rate | Delivery time, mean | Delivery time, p99 | CPU of the leader (cores) |
| --------- | ----------------- | ----------------------- | -------------------- | --------------------- | ------------------- | ------------------ | ------------------------- |
| 500       | 1,800,000         | 100%                    | 90 ms                | 100%                  | 75 ms               | 135 ms             | 0.24                      |
| 1,000     | 3,600,000         | 100%                    | 67 ms                | 100%                  | 65 ms               | 96 ms              | 0.40                      |
| 1,500     | 5,400,000         | 100%                    | 75 ms                | 100%                  | 68 ms               | 108 ms             | 0.56                      |
| 2,000     | 7,200,000         | 100%                    | 79 ms                | 100%                  | 71 ms               | 113 ms             | 0.72                      |

## Short Messages from ESMEs

Submission time: from the `submit_sm` to its `submit_sm_resp`, the message accepted and stored. Delivery time: from the `submit_sm` to the MT-Forward-Short-Message request of its delivery to the recipient's MME (TS 29.338 §6.2).

### Without Delivery Receipts

| Nodes | Rate (/s) | Rate reached (/s) | Submission time, p99 | Delivery time, mean | Delivery time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | ----------------- | -------------------- | ------------------- | ------------------ | ----------- | --------------- |
| 1     | 1,000     | 1,000             | 33 ms                | 33 ms               | 58 ms              | 0.39        | 71 MiB          |
| 1     | 2,000     | 1,998             | 283 ms               | 86 ms               | 304 ms             | 0.62        | 71 MiB          |
| 3     | 1,000     | 999               | 75 ms                | 67 ms               | 115 ms             | 0.39        | 83 MiB          |
| 3     | 2,000     | 1,170             | 14.0 s               | 6.90 s              | 14.0 s             | 0.41        | 135 MiB         |

### With Delivery Receipts

Receipt time: from the `submit_sm` to the `deliver_sm` of its delivery receipt.

| Nodes | Rate (/s) | Submission time, p99 | Delivery time, mean | Delivery time, p99 | Receipt time, mean | Receipt time, p99 | CPU (cores) | Resident memory |
| ----- | --------- | -------------------- | ------------------- | ------------------ | ------------------ | ----------------- | ----------- | --------------- |
| 1     | 600       | 34 ms                | 34 ms               | 61 ms              | 58 ms              | 97 ms             | 0.35        | 57 MiB          |
| 1     | 1,200     | 42 ms                | 37 ms               | 74 ms              | 2.10 s             | 4.33 s            | 0.54        | 90 MiB          |
| 3     | 600       | 64 ms                | 67 ms               | 121 ms             | 1.53 s             | 2.96 s            | 0.31        | 82 MiB          |
| 3     | 1,200     | 113 ms               | 81 ms               | 148 ms             | 10.2 s             | 18.2 s            | 0.52        | 99 MiB          |

Every message was submitted, delivered and receipted.

In a cluster, CPU and memory are those of the node the ESME is bound to.

## Test Setup

| Item                       | Value                                                                                                                                                  |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Version                    | Ella SMSC built from source, after v0.0.1, default configuration, with the SMPP listener                                                               |
| Host                       | 1 host: Intel Core Ultra 7 265K, 20 cores, NVMe SSD with ext4, Ubuntu 26.04, Linux 7.0                                                                 |
| Nodes                      | Each pinned to its own 4 cores, on its own loopback address. The nodes of a cluster share the SSD.                                                     |
| Load generator             | On the same host, pinned to 8 other cores                                                                                                              |
| Core network               | Simulated MME and HSS, over SGd and S6c, over SCTP, connected to every node: answers at once                                                           |
| Subscribers                | 100,000, all reachable                                                                                                                                 |
| Short messages from phones | SMS-SUBMIT of 8-bit text, 25 to 30 octets, without status report requests, each from a subscriber to another at random, spread over every node         |
| ESME                       | 1, with 4 transceiver sessions to one node, at most 10 `submit_sm` without a response per session                                                      |
| Short messages from ESMEs  | `submit_sm` of 8-bit data, `data_coding` `0x04`, 12 to 17 octets, each to a subscriber at random. With delivery receipts: `registered_delivery` `0x01` |
| Rates                      | Each held 20 s, in increasing order, on a new database for each table                                                                                  |
| Runs                       | 1 of each table                                                                                                                                        |
| Success                    | Delivered, or receipted, within 20 s of the end of the rate                                                                                            |
