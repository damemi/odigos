# HP related information and files

## java SDK

Odigos enterprise supports 3 types of java SDKs:
- The one in opensource (tier: community, type: native)
- The one where instrumentation themselves are done with eBPF for walmart (tier: enterprise, type: ebpf)
- use native instrumentations, but process and export the data with eBPF (tier: enterprise, type: native)

The default otel sdk for on-prem installations is the second one (enterprise ebpf), which at the moment have just 2 instrumentations with very few attributes.

HPE is using the third one (enterprise native) which is the most complete one, with all the instrumentations and attributes, and with ebpf improved performance.

To change the default SDK:

```bash
kubectl patch odigosconfigurations.odigos.io odigos-config -n odigos-system --type='json' -p='[{"op": "replace", "path": "/spec/defaultSDKs/java/sdkType", "value": "native"}]'
```

## Enabling go code attributes

HPE need us to populate code attributes. In go this is done in eBPF, and need to be enabled:

```bash
kubectl patch odigosconfigurations.odigos.io odigos-config -n odigos-system --type='json' -p='[{"op": "add", "path": "/spec/goAutoIncludeCodeAttributes", "value": true}]'
odigos upgrade --yes
```

## categories and sub-categories
HP needs non-standard span attributes for category and sub-category, for compatibility with their system.
The way to do it is with a custom processor.
The `category-attributes.yaml` file in this directory contains an example of how this can be achieved.

As of 2024-03-03, they have not yet given us a list of the values to populate these attributes with.

## host.name

HP asked for us to have an `host.name` attribute on each span, which they use internally. Confusingly, this should not be the `k8s.node.name` which is the actual host, but rather `k8s.pod.name`. They also need the vm to populate this attribute later on.

The `host-name-attr.yaml` is a custom processor that can be used to populate this attribute.

