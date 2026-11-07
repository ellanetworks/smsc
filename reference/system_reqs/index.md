# System Requirements

Ella SMSC runs on Linux. It supports **amd64** and **arm64** architectures.

|                      | Up to 1,000 subscribers | Up to 100,000 subscribers |
| -------------------- | ----------------------- | ------------------------- |
| **CPU**              | 1 core                  | 4 cores                   |
| **Memory**           | 1 GB RAM                | 8 GB RAM                  |
| **Disk**             | 10 GB                   | 100 GB                    |
| **Operating System** | Ubuntu 26.04 LTS        | Ubuntu 26.04 LTS          |
| **Kernel**           | SCTP, for Diameter      | SCTP, for Diameter        |

Each column is sized for 20 short messages per subscriber per day, kept for 30 days, with at least 7 times the busiest hour's rate to spare. The 100,000 subscriber host is the one of the [performance reference](https://docs.ellanetworks.com/smsc/reference/performance/index.md).
