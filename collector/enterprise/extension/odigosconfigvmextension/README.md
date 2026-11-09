# Odigos Config VM Extension

Provides Odigos collector configuration on VMs, where there are no InstrumentationConfigs to watch.
It is the VM counterpart of the OSS `odigos_config_k8s` extension and implements the same
`collector.OdigosConfigExtension` interface, so processors such as `odigospiimasking`,
`odigosurltemplate` and `odigossqlquery` work unchanged.

| Status | |
| --- | --- |
| Stability | development |
| Distributions | `odigos-enterprise-collector` only |

## Behavior

The extension reads the vm-agent actions directory (`actions.d`) and merges the enabled actions of
each supported type into one config, the same way the k8s instrumentor merges Actions:

- `piiMasking`: categories and custom maskings are unioned.
- `urlTemplatization`: templates are unioned, and default templatization configs are merged
  (`disabled` wins, `skipForNonSuccessCodes` wins over a status codes list, status codes are unioned).
  When no action sets `default`, default templatization is enabled.
- `dbQueryTemplatization`: `templatizeLiterals` and `removePostgresCastOperator` are enabled if any
  action enables them.
- `inferDbAttributes`: enabled when any action exists.

Other action types are ignored, and a file that fails to parse is skipped without dropping the rules
of the other files.

The directory is watched, so adding, editing or removing an action is pushed to registered
processors without a collector reload. Actions have no scopes yet, so every resource maps to the
single cache key `global`.

## Configuration

```yaml
extensions:
  odigos_config_vm:
    actions_dir: /var/lib/odigos-agent/config/actions.d

processors:
  odigospiimasking:
    odigos_config_extension: odigos_config_vm
  odigosurltemplate:
    odigos_config_extension: odigos_config_vm
  odigossqlquery:
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

A `urlTemplatization` action in `actions.d`:

```yaml
- name: url-templatization
  type: urlTemplatization
  signals: [TRACES]
  config:
    templatizationRules:
      - /api/v1/users/{id}
    default:
      skipPolicy:
        skipForNonSuccessCodes: true
```
