# Ella SMSC (beta)

<p align="center">
  <img src="docs/images/logo.svg" alt="Ella SMSC" width="120"/>
</p>

[![ella-smsc](https://snapcraft.io/ella-smsc/badge.svg)](https://snapcraft.io/ella-smsc)

**Ella SMSC** lets private cellular network subscribers send and receive SMS messages. It is an SMS Service Center (SMSC) that integrates with 4G and 5G core networks.

[Get Started Now!](https://docs.ellanetworks.com/smsc/tutorials/first_sms/)

## Key Features

- **SMS over 4G and 5G**: phones send and receive SMS through the MME or SMSF of the core, over SGd, with routing from the HSS over S6c.
- **SMS over IP**: phones registered with an IP-SM-GW, such as Ella IMS's, send and receive SMS through it.
- **Store and forward**: messages wait while a phone is unreachable, are retried, and go out as soon as the HSS says it is back, within their validity period.
- **3GPP compliant**: status reports, replace short message and reject duplicates, per TS 23.040 and TS 29.338.
- **Application messages**: send text from the web UI or the HTTP API, from a number or a name such as `EllaSMSC`, and binary messages such as SIM OTA updates from the HTTP API.
- **Delivery visibility**: every routing lookup and delivery attempt is recorded, with why it failed.
- **Secure**: users with roles, API tokens, HTTPS, and audited reads of message content.
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
