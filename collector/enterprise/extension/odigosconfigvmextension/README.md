# Odigos Config VM Extension

Provides Odigos collector configuration on VMs, where there are no InstrumentationConfigs to watch.
It is the VM counterpart of the OSS `odigos_config_k8s` extension and implements the same
`collector.OdigosConfigExtension` interface, so processors such as `odigospiimasking` work unchanged.

| Status | |
| --- | --- |
| Stability | development |
| Distributions | `odigos-enterprise-collector` only |

## Behavior

The extension reads the vm-agent actions directory (`actions.d`) and merges every enabled
`piiMasking` action into one config: categories and custom maskings are unioned, the same way the
k8s instrumentor merges PiiMasking Actions. Other action types are ignored, and a file that fails to
parse is skipped without dropping the rules of the other files.

The directory is watched, so adding, editing or removing a `piiMasking` action is pushed to
registered processors without a collector reload. Actions have no scopes yet, so every resource maps
to the single cache key `global`.

## Configuration

```yaml
extensions:
  odigos_config_vm:
    actions_dir: /var/lib/odigos-agent/config/actions.d

processors:
  odigospiimasking:
    odigos_config_extension: odigos_config_vm

service:
  extensions: [odigos_config_vm]
```

A `piiMasking` action in `actions.d`:

```yaml
- name: pii-masking
  type: piiMasking
  signals: [TRACES]
  config:
    piiCategories: [CREDIT_CARD, EMAIL]
    customFormatMaskings:
      - lookupKey: customer_id
        dataFormat: json
    customRegexMaskings:
      - regex: 'id=([^&]+)'
```
