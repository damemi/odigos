package instrumentor

import (
	"encoding/json"
	"fmt"
	"slices"

	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

const manifestVersion = 1

// manifest is the JSON value of the PropOdigosManaged property. PrevProps maps every property the connector
// manages to its value before Odigos touched it: nil means absent (rollback deletes the key), non-nil is the
// customer's value (rollback restores it verbatim). Only plain properties are recorded — secure property values
// are write-only on the platform, which is why a secure property among the managed keys blocks instrumentation
// instead of being snapshotted.
type manifest struct {
	Version int `json:"v"`
	// Deployment is the deployment the snapshot was taken on. Promoting an application copies its properties,
	// marker included, to another deployment; the copy describes a different application's prior state, so it
	// grants nothing there.
	Deployment string             `json:"dep,omitempty"`
	PrevProps  map[string]*string `json:"prevProps"`
}

func snapshot(current map[string]string, deploymentID string) manifest {
	prev := map[string]*string{}
	for _, k := range slices.Concat(managedKeys, []string{PropResourceAttributes}) {
		if v, ok := current[k]; ok {
			vv := v
			prev[k] = &vv
		} else {
			prev[k] = nil
		}
	}
	return manifest{Version: manifestVersion, Deployment: deploymentID, PrevProps: prev}
}

func priorValue(m manifest, key string) string {
	if p := m.PrevProps[key]; p != nil {
		return *p
	}
	return ""
}

func marshalManifest(m manifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal odigos mule manifest: %w", err)
	}
	return string(b), nil
}

func parseManifest(s string) (manifest, bool) {
	if s == "" {
		return manifest{}, false
	}
	var m manifest
	if err := json.Unmarshal([]byte(s), &m); err != nil || m.PrevProps == nil {
		return manifest{}, false
	}
	return m, true
}

// ownedBy checks that a present ODIGOS_MANAGED value is this connector's record for this deployment. An
// unreadable marker records nothing a rollback could restore, and taking a fresh snapshot over it would record
// Odigos's own earlier values as the customer's; a marker from another deployment was copied by a promotion.
// Either way the application is left alone until a person removes the marker.
func ownedBy(marker, deploymentID string) error {
	m, ok := parseManifest(marker)
	if !ok {
		return fmt.Errorf("the application carries an unreadable %s property, so Odigos cannot tell what it changed; remove the property to let Odigos instrument it", store.PropOdigosManaged)
	}
	if m.Deployment != "" && m.Deployment != deploymentID {
		return fmt.Errorf("the application's %s property belongs to another deployment (copied when an application was promoted); remove it to let Odigos instrument this one", store.PropOdigosManaged)
	}
	return nil
}
