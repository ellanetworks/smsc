---
description: RESTful API reference for managing the operator settings of Ella SMSC.
---

# Operator

The operator is the PLMN of the SMSC, from which its Diameter identity derives, its service centre address, and the numbering plan that converts the numbers phones send to E.164 numbers.

## Get the Operator

This path returns the operator settings.

| Method | Path               |
| ------ | ------------------ |
| GET    | `/api/v1/operator` |

### Response Fields

| Field                            | Type   | Description |
| -------------------------------- | ------ | ----------- |
| `mcc`                            | string | Mobile Country Code. |
| `mnc`                            | string | Mobile Network Code. |
| `service_centre_address`         | string | E.164 number of the SMSC, which phones have as SMSC number. |
| `numbering`                      | object | Conversion of the numbers phones send to E.164 numbers. |
| `numbering.country_code`         | string | Country code, added to the numbers phones send in national format. |
| `numbering.national_prefix`      | string | Prefix that phones dial before a national number, removed before the country code is added. Empty if none. |
| `numbering.international_prefix` | string | Prefix that phones dial before an international number, removed. Empty if none. |

### Sample Response

```json
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "service_centre_address": "+15550000000",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011"
        }
    }
}
```

## Update the Operator

This path replaces the operator settings. A change of MCC or MNC changes the Diameter host and realm: the SMSC restarts Diameter, and its peers reconnect.

| Method | Path               |
| ------ | ------------------ |
| PUT    | `/api/v1/operator` |

### Request Body

| Field                            | Type   | Required | Description |
| -------------------------------- | ------ | -------- | ----------- |
| `mcc`                            | string | Yes      | 3 digits. |
| `mnc`                            | string | Yes      | 2 or 3 digits. |
| `service_centre_address`         | string | Yes      | E.164 number with `+`, e.g. `+15550000000`. |
| `numbering`                      | object | Yes      | |
| `numbering.country_code`         | string | Yes      | 1 to 3 digits, not starting with 0. |
| `numbering.national_prefix`      | string | Yes      | Up to 4 digits. Differs from `international_prefix` unless empty. |
| `numbering.international_prefix` | string | Yes      | Up to 4 digits. |

### Sample Request

```json
{
    "mcc": "001",
    "mnc": "01",
    "service_centre_address": "+15550000000",
    "numbering": {
        "country_code": "1",
        "national_prefix": "1",
        "international_prefix": "011"
    }
}
```

### Sample Response

```json
{
    "result": {
        "mcc": "001",
        "mnc": "01",
        "service_centre_address": "+15550000000",
        "numbering": {
            "country_code": "1",
            "national_prefix": "1",
            "international_prefix": "011"
        }
    }
}
```
