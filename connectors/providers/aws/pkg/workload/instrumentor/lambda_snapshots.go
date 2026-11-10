package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type lambdaSnapshotStore interface {
	SaveLambdaSnapshot(context.Context, string, string, []byte) error
	LoadLambdaSnapshot(context.Context, string, string) ([]byte, error)
}

// Keep the marker small while retaining exact layer ownership for read-only observers.
// Each update references an immutable snapshot: a failed AWS revision update cannot corrupt
// the rollback state of the configuration that AWS still runs.
func (i *LambdaInstrumentor) persistManifest(ctx context.Context, arn string, m managedManifest) (string, error) {
	if i.snapshots == nil {
		return "", fmt.Errorf("Lambda rollback snapshot store is unavailable")
	}
	m.Version = managedManifestVersion
	m.SnapshotID = ""
	payload, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%x", sha256.Sum256(payload))
	if err := i.snapshots.SaveLambdaSnapshot(ctx, arn, id, payload); err != nil {
		return "", err
	}
	return marshalManifest(managedManifest{Version: 2, SnapshotID: id, Layer: m.Layer, CollectorLayer: m.CollectorLayer, Config: m.Config})
}

func (i *LambdaInstrumentor) loadManifest(ctx context.Context, arn string, ref managedManifest) (managedManifest, error) {
	if i.snapshots == nil {
		return managedManifest{}, fmt.Errorf("Lambda rollback snapshot store is unavailable; refusing to modify the function")
	}
	payload, err := i.snapshots.LoadLambdaSnapshot(ctx, arn, ref.SnapshotID)
	if err != nil {
		return managedManifest{}, err
	}
	m, ok := parseManifest(string(payload))
	if !ok || m.PrevEnv == nil || m.SnapshotID != "" || fmt.Sprintf("%x", sha256.Sum256(mustCanonicalManifest(m))) != ref.SnapshotID || (ref.Layer != "" && m.Layer != ref.Layer) || (ref.CollectorLayer != "" && m.CollectorLayer != ref.CollectorLayer) {
		return managedManifest{}, fmt.Errorf("invalid Lambda rollback snapshot %s; refusing to modify the function", ref.SnapshotID)
	}
	return m, nil
}

// Postgres JSONB may reorder object keys; hash the same canonical encoding used on insert.
func mustCanonicalManifest(m managedManifest) []byte { b, _ := json.Marshal(m); return b }

func cloneLambdaEnv(env map[string]string) map[string]string {
	copy := make(map[string]string, len(env))
	for k, v := range env {
		copy[k] = v
	}
	return copy
}

func validateLambdaEnvSize(env map[string]string) error {
	size := 0
	for k, v := range env {
		size += len(k) + len(v)
	}
	if size > 4096 {
		return fmt.Errorf("Lambda environment requires %d bytes, exceeding AWS's 4096-byte limit; reduce application environment variables or destination header sizes", size)
	}
	return nil
}
