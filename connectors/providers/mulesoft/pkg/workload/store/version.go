package store

import "github.com/odigos-io/odigos-enterprise/connectors/anypoint"

// MuleVersion is the reportable Mule runtime version: "4.11.0" from a CloudHub 2.0 / RTF image tag like
// "4.11.0:1e-java17", or the raw string when it cannot be parsed.
func MuleVersion(raw string) string {
	if v, ok := anypoint.ParseMuleVersion(raw); ok {
		return v.Raw
	}
	return raw
}
