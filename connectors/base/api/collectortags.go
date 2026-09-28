package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Tags on a managed Collector's cloud objects.
//
// Identity tags + TagIaC identify ownership (written at create time).
// TagManaged marks deployer-created objects and alone authorizes deletion.
// Manual-mode artifacts carry the identity tags for OpAMP correlation, never TagManaged.
//
//	 ONE SPELLING ON EVERY CLOUD, and it is the narrowest rule of the three that has to accept it:
//
//	AWS tag keys     letters, digits, spaces and _ . : / = + - @, up to 128
//	Azure tag names  anything except < > % & \ ? /, up to 512, matched case-insensitively
//	GCP label keys   ^[a-z][a-z0-9_-]{0,62}$ — no dot, no slash, no uppercase
const (
	TagComponent   = "odigos-component"    // distinguishes Collector objects
	TagConnector   = "odigos-connector"    // connector name; adoption key (not unique alone)
	TagConnectorID = "odigos-connector-id" // CR UID; unique but not stable across recreation
	TagRegion      = "odigos-region"       // regional Collector identity
	TagNetwork     = "odigos-network"      // host VPC / VNet metadata; not Collector identity
	// TagIaC marks that an Odigos artifact created this object. Matched on presence
	// (any value disqualifies deletion). Descriptive payload lives in EnvOdigosIaC
	// as CollectorIaCMarker — tag values cannot carry image refs across providers.
	TagIaC = "odigos-iac"
	// TagManaged marks deployer-created objects. Never written by artifacts or on adoption.
	TagManaged = "odigos-managed"
)

const (
	ComponentCollector = "collector"
	// TagManagedTrue is the only value that grants deletion authority.
	TagManagedTrue = "true"
)

const CollectorIaCMarkerVersion = 1

// CollectorIaCMarker is the JSON payload of EnvOdigosIaC — a field subset of IaCMarker.
// Source/Release identify the artifact; Image is used by conformance.
type CollectorIaCMarker struct {
	Version int    `json:"v"`
	Source  string `json:"source,omitempty"`
	Release string `json:"version,omitempty"`
	Image   string `json:"image,omitempty"`
}

// CollectorTags is the parsed tag set of one cloud object. Absent tags are zero values.
type CollectorTags struct {
	Component   string
	Connector   string
	ConnectorID string
	Region      string
	Network     string
	Managed     string // raw TagManaged value; compare against TagManagedTrue
	IaC         string // raw TagIaC value; presence drives attribution
}

// ParseCollectorTags reads the Collector tag set. Keys are matched case-insensitively
// (Azure treats tag names that way); unknown tags are ignored.
func ParseCollectorTags(tags map[string]string) CollectorTags {
	return CollectorTags{
		Component:   collectorTag(tags, TagComponent),
		Connector:   collectorTag(tags, TagConnector),
		ConnectorID: collectorTag(tags, TagConnectorID),
		Region:      collectorTag(tags, TagRegion),
		Network:     collectorTag(tags, TagNetwork),
		Managed:     collectorTag(tags, TagManaged),
		IaC:         collectorTag(tags, TagIaC),
	}
}

// collectorTag looks key up exactly first, then case-insensitively.
func collectorTag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return v
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// IsCollector reports whether the tags identify a managed Collector object.
func (t CollectorTags) IsCollector() bool { return t.Component == ComponentCollector }

// HasOwnershipMarker reports whether TagManaged is set to TagManagedTrue.
func (t CollectorTags) HasOwnershipMarker() bool { return t.Managed == TagManagedTrue }

// HasIaCMarker reports whether an Odigos artifact recorded itself as creator (presence alone).
func (t CollectorTags) HasIaCMarker() bool { return t.IaC != "" }

// ParseCollectorIaCMarker decodes the EnvOdigosIaC payload. Absent or malformed → false.
func ParseCollectorIaCMarker(s string) (CollectorIaCMarker, bool) {
	if s == "" {
		return CollectorIaCMarker{}, false
	}
	var m CollectorIaCMarker
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return CollectorIaCMarker{}, false
	}
	return m, true
}

// CollectorIdentityTagKeys are the tags that identify the regional Collector in both modes.
func CollectorIdentityTagKeys() []string {
	return []string{TagComponent, TagConnector, TagConnectorID, TagRegion}
}

// CollectorHostMetadataTagKeys are tags describing where the regional Collector is hosted. They are
// deliberately separate from CollectorIdentityTagKeys: moving the host network does not create a new
// Collector identity.
func CollectorHostMetadataTagKeys() []string { return []string{TagNetwork} }

// CollectorArtifactTagKeys are identity + host metadata + TagIaC. TagManaged is deliberately absent.
func CollectorArtifactTagKeys() []string {
	keys := append([]string{}, CollectorIdentityTagKeys()...)
	keys = append(keys, CollectorHostMetadataTagKeys()...)
	return append(keys, TagIaC)
}

// CollectorIdentityTags renders the regional identity and host metadata set. Does not include TagManaged.
func CollectorIdentityTags(connector, connectorID, region, network string) map[string]string {
	return map[string]string{
		TagComponent:   ComponentCollector,
		TagConnector:   connector,
		TagConnectorID: connectorID,
		TagRegion:      region,
		TagNetwork:     network,
	}
}

// CollectorTagKeys are every key this package defines, in a fixed order.
func CollectorTagKeys() []string {
	return append(CollectorArtifactTagKeys(), TagManaged)
}

// Constrained to GCP's label rules (strictest of AWS/Azure/GCP).
const (
	CollectorTagKeyMaxLen   = 63
	CollectorTagValueMaxLen = 63
)

// ValidCollectorTagKey reports whether a key is writable on every provider.
func ValidCollectorTagKey(s string) bool {
	if s == "" || len(s) > CollectorTagKeyMaxLen || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	return tagSafe(s)
}

// ValidCollectorTagValue reports whether a value is writable on every provider. Empty is valid.
func ValidCollectorTagValue(s string) bool {
	return len(s) <= CollectorTagValueMaxLen && tagSafe(s)
}

func tagSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// ValidateCollectorTags reports Odigos tags that some provider would reject.
// Only Odigos keys are checked; customer tags are ignored. Problems are collected
// in fixed key order for stable status messages.
func ValidateCollectorTags(tags map[string]string) error {
	var problems []string
	for _, k := range CollectorTagKeys() {
		v, ok := tags[k]
		if !ok {
			continue
		}
		if !ValidCollectorTagKey(k) {
			problems = append(problems, fmt.Sprintf("key %q is not writable on every provider", k))
		}
		if !ValidCollectorTagValue(v) {
			problems = append(problems, fmt.Sprintf(
				"%s=%q: a value must match [a-z0-9_-] and be at most %d characters",
				k, v, CollectorTagValueMaxLen))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("collector tags are not portable across providers: %s", strings.Join(problems, "; "))
}
