package instrumentor

import (
	"encoding/json"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// The ECS ownership marker is the same store.EnvOdigosManaged env var Lambda uses, stamped on the
// instrumented app container of the task definition. Its presence is the authoritative proof this
// connector instrumented the service (a customer's own OpenTelemetry setup never carries it), and its
// JSON payload records the pre-Odigos value of every env key we changed so rollback restores the task
// definition exactly. The wire format ({v, lang, prevEnv}) matches the GCP Cloud Run connector's marker
// so ownership manifests are uniform across the sidecar-based connectors.

const ecsManagedManifestVersion = 1

// ecsManagedManifest is the JSON payload stored in the EnvOdigosManaged env var on the app container.
// PrevEnv maps each env key we set/appended/deleted to the value it had before we touched it: a nil entry
// means the key was absent (rollback deletes it), a non-nil entry is the customer's prior value (rollback
// restores it verbatim). Language records which agent was injected so re-instrument and rollback know the
// managed key set. The sidecar container, shared volume, mount point and container dependency we add are
// removed on rollback by their well-known Odigos names, so they are not recorded here.
type ecsManagedManifest struct {
	Version  int                `json:"v"`
	Language string             `json:"lang"`
	PrevEnv  map[string]*string `json:"prevEnv,omitempty"`
}

func marshalECSManifest(m ecsManagedManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal odigos ecs manifest: %w", err)
	}
	return string(b), nil
}

func parseECSManifest(s string) (ecsManagedManifest, bool) {
	if s == "" {
		return ecsManagedManifest{}, false
	}
	var m ecsManagedManifest
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ecsManagedManifest{}, false
	}
	return m, true
}

// ecsManagedEnvKeys returns every env var name the instrumentor sets for a recipe: the ownership marker,
// the OTEL export/identity vars, the language's static activation vars, and its loader (append) vars.
// Their pre-Odigos values are snapshotted on first instrument so rollback restores the container exactly.
func ecsManagedEnvKeys(recipe agentRecipe) []string {
	keys := []string{
		store.EnvOdigosManaged,
		EnvServiceName, envResourceAttributes,
		envOTLPEndpoint, EnvOtlpProtocol,
		EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter,
		EnvOtelLogLevel,
	}
	for k := range recipe.staticEnv {
		keys = append(keys, k)
	}
	for _, a := range recipe.appendEnv {
		keys = append(keys, a.name)
	}
	return keys
}

// buildECSManifest snapshots the pre-Odigos value of every managed key for a recipe. A re-instrument
// reuses the snapshot already stored in the marker so our own values are never mistaken for the
// customer's; only the language is refreshed (it never changes for a given service in practice).
func buildECSManifest(env map[string]string, existingMarker string, recipe agentRecipe) ecsManagedManifest {
	if existing, ok := parseECSManifest(existingMarker); ok {
		existing.Language = recipe.lang
		return existing
	}
	prev := make(map[string]*string)
	for _, k := range ecsManagedEnvKeys(recipe) {
		prev[k] = ecsPriorValue(k, env)
	}
	return ecsManagedManifest{Version: ecsManagedManifestVersion, Language: recipe.lang, PrevEnv: prev}
}

// ecsPriorValue is the value rollback should restore for an env key: the customer's current value, except
// for keys that are unmistakably ours (the ownership marker), which are recorded as absent (deleted on
// rollback).
func ecsPriorValue(key string, env map[string]string) *string {
	if key == store.EnvOdigosManaged {
		return nil
	}
	if v, ok := env[key]; ok {
		vv := v
		return &vv
	}
	return nil
}

// ecsMarker is what a container's Odigos markers say about it, resolved from whichever of the two it carries.
//
// The two are not alternatives for the same fact — they are written by different parties and answer different
// questions (see api.EnvOdigosIaC). Managed is the one that matters downstream: it says the CONNECTOR wrote
// this and a rollback snapshot exists, which is the only basis on which anything here may mutate or restore a
// container. An IaC-marked container is read, reported and judged; it is never rolled back, because there is
// nothing recorded to roll back to.
type ecsMarker struct {
	// Managed is true when the container carries the connector's own ownership marker (automatic mode).
	Managed bool
	// Language is the agent that was staged, from whichever marker recorded it. Empty means unrecorded, which
	// a customer's hand-authored IaC marker legitimately can be — see containerDiff for why that skips the
	// language check rather than reporting drift.
	Language string
	// PrevEnv is the rollback snapshot, and it exists ONLY on a Managed marker.
	PrevEnv map[string]*string
}

// readECSMarker resolves a container's Odigos attribution, reporting false when neither marker is present.
//
// The ownership marker wins when both are there, for the same reason Lambda's recordedLayer prefers it: it is
// the richer record — it carries the snapshot — and a container the connector has taken over is the
// connector's to describe, whatever an earlier manual application also left behind.
func readECSMarker(env map[string]string) (ecsMarker, bool) {
	if m, ok := parseECSManifest(env[store.EnvOdigosManaged]); ok {
		return ecsMarker{Managed: true, Language: m.Language, PrevEnv: m.PrevEnv}, true
	}
	if m, ok := baseapi.ParseIaCMarker(env[store.EnvOdigosIaC]); ok {
		return ecsMarker{Language: m.Language}, true
	}
	return ecsMarker{}, false
}

// ecsPriorEnv returns the customer's original value for an env key from the snapshot ("" if it was
// absent). Used to rebuild append-style loader vars (JAVA_TOOL_OPTIONS/NODE_OPTIONS/PYTHONPATH) from the
// original rather than from the current (already-modified) value, so re-instrument does not accumulate
// our value.
func ecsPriorEnv(m ecsManagedManifest, key string) string {
	return priorEnvFrom(m.PrevEnv, key)
}

// markerPriorEnv is ecsPriorEnv over a resolved marker. An IaC marker carries no snapshot, so this returns ""
// for one — which is correct rather than lossy: the manual-mode loader check never asks for a prior value.
func markerPriorEnv(m ecsMarker, key string) string {
	return priorEnvFrom(m.PrevEnv, key)
}

func priorEnvFrom(prevEnv map[string]*string, key string) string {
	if prev, ok := prevEnv[key]; ok && prev != nil {
		return *prev
	}
	return ""
}
