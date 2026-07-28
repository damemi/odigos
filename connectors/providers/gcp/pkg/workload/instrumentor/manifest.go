package instrumentor

import (
	"encoding/json"
	"fmt"
)

// EnvOdigosManaged is the ownership marker + change snapshot the instrumentor stamps on the ingress
// container. Its presence is the authoritative proof this connector instrumented the service (a
// customer's own OpenTelemetry setup never carries it), and its JSON payload records the pre-Odigos value
// of every env key we changed so rollback restores the service exactly.
const EnvOdigosManaged = "ODIGOS_MANAGED"

const managedManifestVersion = 1

// managedManifest is the JSON payload stored in the EnvOdigosManaged env var on the ingress container.
// PrevEnv maps each env key we set/appended/deleted to the value it had before we touched it: a nil entry
// means the key was absent (rollback deletes it), a non-nil entry is the customer's prior value (rollback
// restores it verbatim). Language records which agent was injected so re-instrument and rollback know the
// managed key set. The sidecar container, shared volume, volume mount and container dependency we add are
// removed on rollback by their well-known Odigos names, so they are not recorded here.
type managedManifest struct {
	Version  int                `json:"v"`
	Language string             `json:"lang"`
	PrevEnv  map[string]*string `json:"prevEnv,omitempty"`
}

func marshalManifest(m managedManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal odigos cloud-run manifest: %w", err)
	}
	return string(b), nil
}

func parseManifest(s string) (managedManifest, bool) {
	if s == "" {
		return managedManifest{}, false
	}
	var m managedManifest
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return managedManifest{}, false
	}
	return m, true
}

// managedEnvKeys returns every env var name the instrumentor sets for a language: the ownership marker,
// the OTEL export/identity vars, the language's static activation vars, and its loader (append) vars.
// Their pre-Odigos values are snapshotted on first instrument so rollback restores the container exactly.
func managedEnvKeys(lang string) []string {
	keys := []string{
		EnvOdigosManaged,
		EnvServiceName, EnvResourceAttributes,
		EnvOTLPEndpoint, EnvOTLPProtocol,
		EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter,
		EnvOtelLogLevel,
	}
	spec := languageSpecs[lang]
	for k := range spec.staticEnv {
		keys = append(keys, k)
	}
	for _, a := range spec.appendEnv {
		keys = append(keys, a.name)
	}
	return keys
}

// buildManifest snapshots the pre-Odigos value of every managed key for a language. A re-instrument
// reuses the snapshot already stored in the marker so our own values are never mistaken for the
// customer's; only the language is refreshed (it never changes for a given service in practice).
func buildManifest(env map[string]string, existingMarker, lang string) managedManifest {
	if existing, ok := parseManifest(existingMarker); ok {
		existing.Language = lang
		return existing
	}
	prev := make(map[string]*string)
	for _, k := range managedEnvKeys(lang) {
		prev[k] = priorValue(k, env)
	}
	return managedManifest{Version: managedManifestVersion, Language: lang, PrevEnv: prev}
}

// priorValue is the value rollback should restore for an env key: the customer's current value, except
// for keys that are unmistakably ours (the ownership marker), which are recorded as absent (deleted on
// rollback).
func priorValue(key string, env map[string]string) *string {
	if key == EnvOdigosManaged {
		return nil
	}
	if v, ok := env[key]; ok {
		vv := v
		return &vv
	}
	return nil
}

// priorEnv returns the customer's original value for an env key from the snapshot ("" if it was absent).
// Used to rebuild append-style loader vars (JAVA_TOOL_OPTIONS/NODE_OPTIONS/PYTHONPATH) from the original
// rather than from the current (already-modified) value, so re-instrument does not accumulate our value.
func priorEnv(m managedManifest, key string) string {
	if prev, ok := m.PrevEnv[key]; ok && prev != nil {
		return *prev
	}
	return ""
}
