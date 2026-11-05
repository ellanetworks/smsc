# Ella SMSC (beta)

<p align="center">
  <img src="docs/images/logo.svg" alt="Ella SMSC" width="120"/>
</p>

[![ella-smsc](https://snapcraft.io/ella-smsc/badge.svg)](https://snapcraft.io/ella-smsc)

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

[Get Started Now!](https://docs.ellanetworks.com/smsc/tutorials/first_sms/)

## Key Features

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

## Quick Links

- [Website](https://ellanetworks.com/)
- [Documentation](https://docs.ellanetworks.com/smsc/)
- [Snap Store Listing](https://snapcraft.io/ella-smsc)
- [Ella Core](https://github.com/ellanetworks/core)
- [Contributing](CONTRIBUTING.md)

## License

Ella SMSC is licensed under Business Source (BUSL-1.1).

You need a [Production License](https://ellanetworks.com/pricing) to run Ella SMSC in production. For more information, view our [License FAQ](https://ellanetworks.com/license-faq).
