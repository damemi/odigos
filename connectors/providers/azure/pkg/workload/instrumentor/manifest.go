package instrumentor

import (
	"encoding/json"
	"fmt"

	store "github.com/odigos-io/odigos/connectors/providers/azure/pkg/workload/store"
)

// EnvOdigosManaged is the ownership marker + change snapshot stamped on the app container. Defined in the
// workload store, which the detector also imports, so the writer here and the reader there cannot drift.
const EnvOdigosManaged = store.EnvOdigosManaged

// EnvOdigosIaC is the attribution marker the CUSTOMER's own deployment definition carries in manual (IaC)
// mode, from the instructions this connector publishes (see api.EnvOdigosIaC). The connector never writes it.
//
// It proves instrumentation is applied and records which agent was staged; it does NOT prove ownership and
// grants no mutation rights, because it carries no snapshot of what a rollback would restore.
const EnvOdigosIaC = store.EnvOdigosIaC

const managedManifestVersion = 1

// managedManifest is the JSON payload stored in EnvOdigosManaged. PrevEnv maps each env key we
// touched to its pre-Odigos value (nil = was absent).
type managedManifest struct {
	Version  int                `json:"v"`
	Language string             `json:"lang"`
	PrevEnv  map[string]*string `json:"prevEnv,omitempty"`
}

func marshalManifest(m managedManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal odigos container-apps manifest: %w", err)
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

func managedEnvKeys(lang string) []string {
	keys := []string{
		EnvOdigosManaged, EnvCollectorEndpoint,
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

func priorValue(key string, env map[string]string) *string {
	switch key {
	case EnvOdigosManaged, EnvCollectorEndpoint:
		// odigos-namespaced — always ours, never a customer value, so rollback deletes rather than restores.
		return nil
	}
	if v, ok := env[key]; ok {
		vv := v
		return &vv
	}
	return nil
}

func priorEnv(m managedManifest, key string) string {
	if prev, ok := m.PrevEnv[key]; ok && prev != nil {
		return *prev
	}
	return ""
}
