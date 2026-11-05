---
description: An SMS Service Center for private 4G and 5G networks - easy to operate, reliable, and secure.
---

# Ella SMSC

<p align="center">
  <img src="images/logo.svg" alt="Ella SMSC Logo" width="200" />
</p>

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

## Key features

- **SMS over NAS**: phones on 4G and 5G send and receive SMS through the MME or SMSF of the core over SGd.
- **SMS over IP**: phones registered with an IP-SM-GW, such as Ella IMS's, send and receive SMS through it.
- **Store and forward**: messages wait while a phone is unreachable, are retried, and go out as soon as the HSS says it is back, within their validity period.
- **Application messages**: send messages to phones from the web UI, the HTTP API, or SMPP 3.4 for SIM OTA platforms and other ESMEs.
- **Delivery visibility**: every routing lookup and delivery attempt is recorded, with why it failed.
- **Single sign-on**: users log in with your identity provider, such as Google, Microsoft or Okta, over OpenID Connect.
- **Secure**: users with roles, API tokens, HTTPS, and audited reads of message content.
- **Scalable**: serve SMS to up to 100,000 subscribers.
- **Highly available**: deploy Ella SMSC as a high-availability cluster to ensure continuous operation with failover capabilities.
- **Easy to operate**: a single binary with an embedded database and configurable retention, a web UI, an HTTP API, and Prometheus metrics. Install with snap, Docker or Kubernetes.

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
