---
description: An SMS Service Center for private 4G and 5G networks - easy to operate, reliable, and secure.
---

# Ella SMSC

<p align="center">
  <img src="images/logo.svg" alt="Ella SMSC Logo" width="200" />
</p>

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

## Key features

- **SMS over 4G and 5G**: phones send and receive SMS through the MME or SMSF of the core, over SGd, with routing from the HSS over S6c.
- **SMS over IP**: delivery to phones registered with an IP-SM-GW, such as Ella IMS's.
- **Store and forward**: messages wait while a phone is unreachable, and go out as soon as the HSS says it is back.
- **Status reports**: phones that ask for them learn whether their messages were delivered.
- **Application messages**: send SMS to phones from the HTTP API.
- **Easy to operate**: a single binary with an embedded database, a web UI, an HTTP API, and Prometheus metrics.

## In this documentation

<div class="grid cards" markdown>

-   [__Tutorials__](tutorials/index.md)

    ---

    **Start here**: a hands-on introduction to Ella SMSC. Send your first SMS.

-   [__How-to Guides__](how_to/index.md)

    ---

    **Step-by-step guides** covering key operations and common tasks.

-   [__Reference__](reference/index.md)

    ---

    **Technical information** - API, configuration, metrics, and more.

-   [__Explanation__](explanation/index.md)

    ---

    **Discussion and clarification** of key topics.

</div>
