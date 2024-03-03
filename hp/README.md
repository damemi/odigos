# HP related information and files

## categories and sub-categories
HP needs non-standard span attributes for category and sub-category, for compatibility with their system.
The way to do it is with a custom processor.
The `category-attributes.yaml` file in this directory contains an example of how this can be achieved.

As of 2024-03-03, they have not yet given us a list of the values to populate these attributes with.

## host.name

HP asked for us to have an `host.name` attribute on each span, which they use internally. Confusingly, this should not be the `k8s.node.name` which is the actual host, but rather `k8s.pod.name`. They also need the vm to populate this attribute later on.

The `host-name-attr.yaml` is a custom processor that can be used to populate this attribute.
