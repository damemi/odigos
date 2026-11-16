package odigosconfigk8sextension

import "fmt"

// WorkloadKeyString returns a short string for logging (namespace/kind/name, no trailing slash).
func WorkloadKeyString(namespace, kind, name string) string {
	return fmt.Sprintf("%s/%s/%s", namespace, kind, name)
}
