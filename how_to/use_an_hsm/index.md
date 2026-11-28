# Use an HSM

Keep the [encryption key](https://docs.ellanetworks.com/smsc/explanation/encryption_at_rest/index.md) on a hardware security module (HSM), so that it never leaves the device. Here we use a YubiHSM 2, but any PKCS#11 HSM works.

## Prerequisites

- A Linux host, with the HSM's PKCS#11 library installed.
- Ella SMSC installed.

## 1. Store the HSM PIN

Store the HSM's PIN in a file that Ella SMSC can read:

```
echo -n '0001password' | sudo install -D -m 0600 /dev/stdin /etc/ella-smsc/hsm.pin
```

Replace `0001password` with your PIN.

## 2. Configure Ella SMSC to use the HSM

Add the `encryption` block to the SMSC's configuration file:

```
encryption:
  key:
    type: pkcs11
    pkcs11:
      library: /usr/lib/x86_64-linux-gnu/pkcs11/yubihsm_pkcs11.so
      token_label: YubiHSM
      pin_file: /etc/ella-smsc/hsm.pin
      key_label: ella-smsc
      mechanism: rsa-oaep
```

## 3. Configure the HSM's library

Configure the library as its vendor documents. For a YubiHSM 2 on USB:

```
echo "connector = yhusb://" | sudo tee /etc/ella-smsc/yubihsm_pkcs11.conf
```

Start Ella SMSC with `YUBIHSM_PKCS11_CONF=/etc/ella-smsc/yubihsm_pkcs11.conf` in its environment.

## 4. Verify

Ella SMSC's logs name the HSM key, such as `ella-smsc/eb17800de3a5eb9f`.
