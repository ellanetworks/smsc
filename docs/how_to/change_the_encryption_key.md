---
description: Step-by-step instructions to replace the encryption key of a running Ella SMSC deployment, without downtime.
---

# Change the Encryption Key

Replace the [encryption key](../explanation/encryption_at_rest.md) with a new key file, or with a key on an HSM.

=== "Snap"

    ## 1. Create the new key

    ```shell
    openssl rand -base64 32 > encryption-new.key
    ```

    Copy it to every node.

    ## 2. Switch each node

    On each node, one at a time, keep the current key as the previous key, and install the new one:

    ```shell
    sudo mv /var/snap/ella-smsc/common/encryption.key /var/snap/ella-smsc/common/encryption-previous.key
    sudo install -m 0600 encryption-new.key /var/snap/ella-smsc/common/encryption.key
    ```

    Add to `/var/snap/ella-smsc/common/smsc.yaml`:

    ```yaml
    encryption:
      previous_key:
        file:
          path: /var/snap/ella-smsc/common/encryption-previous.key
    ```

    Restart the node:

    ```shell
    sudo snap restart ella-smsc.smscd
    ```

    ## 3. Verify

    Once every node runs with the new key, the leader logs:

    ```shell
    sudo snap logs -n all ella-smsc.smscd | grep retire_cluster_key
    ```

    ## 4. Remove the previous key

    On each node, remove the `encryption` block from `smsc.yaml`.

    Keep a copy of `encryption-previous.key` for the backups taken before the change, and delete it from the nodes.

=== "Binary, to an HSM"

    ## 1. Prepare the HSM

    On every node, follow steps 1 and 3 of [Use an HSM](use_an_hsm.md).

    ## 2. Switch each node

    On each node, one at a time, add the `encryption` block of [Use an HSM](use_an_hsm.md), step 2, with the current key file as the previous key:

    ```yaml
    encryption:
      key:
        type: pkcs11
        pkcs11:
          library: /usr/lib/x86_64-linux-gnu/pkcs11/yubihsm_pkcs11.so
          token_label: YubiHSM
          pin_file: /etc/ella-smsc/hsm.pin
          key_label: ella-smsc
          mechanism: rsa-oaep
      previous_key:
        file:
          path: /var/lib/ella-smsc/encryption.key
    ```

    Restart Ella SMSC.

    ## 3. Verify

    Once every node runs with the HSM key, the leader logs `retire_cluster_key`.

    ## 4. Remove the previous key

    On each node, remove `previous_key` from the configuration file, and delete the previous key file.

    Keep a copy of the previous key file for the backups taken before the change.

=== "Kubernetes"

    ## 1. Keep the current key as the previous key

    ```shell
    kubectl get secret ella-smsc-encryption -n ella -o jsonpath='{.data.key}' | base64 -d > encryption-previous.key
    kubectl create secret generic ella-smsc-encryption-previous -n ella --from-file=key=encryption-previous.key
    helm upgrade ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella --reuse-values \
      --set encryption.previousSecretName=ella-smsc-encryption-previous
    kubectl rollout status statefulset/ella-smsc -n ella
    ```

    ## 2. Replace the key

    ```shell
    kubectl create secret generic ella-smsc-encryption -n ella --from-literal=key="$(openssl rand -base64 32)" \
      --dry-run=client -o yaml | kubectl apply -f -
    kubectl rollout restart statefulset/ella-smsc -n ella
    kubectl rollout status statefulset/ella-smsc -n ella
    ```

    ## 3. Verify

    ```shell
    kubectl logs -n ella -l app.kubernetes.io/name=ella-smsc --tail=-1 | grep retire_cluster_key
    ```

    ## 4. Remove the previous key

    ```shell
    helm upgrade ella-smsc oci://ghcr.io/ellanetworks/charts/ella-smsc --namespace ella --reuse-values \
      --set encryption.previousSecretName=
    ```

    Keep `encryption-previous.key` for the backups taken before the change, and delete the Secret:

    ```shell
    kubectl delete secret ella-smsc-encryption-previous -n ella
    ```
