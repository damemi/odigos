package engine

// SourceIdentityFunc picks, from an entry span's resource attributes, the
// subset that identifies the Odigos source the transaction belongs to. The
// result is stored on the transaction row as descriptive metadata. Each
// platform (k8s, vm agent, ...) has its own notion of source identity; the
// engine defaults to sourceidentity.FromK8sResourceAttributesMap.
type SourceIdentityFunc func(resourceAttrs map[string]string) map[string]string
