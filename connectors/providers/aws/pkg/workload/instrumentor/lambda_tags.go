package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

func lambdaAppliedFingerprint(env map[string]string, layers []string) string {
	sorted := []string{}
	for _, arn := range layers {
		if name, _, ok := store.ParseLayerVersionARN(arn); ok && store.IsOdigosLayerName(name) {
			sorted = append(sorted, arn)
		}
	}
	slices.Sort(sorted)
	raw, _ := json.Marshal(struct {
		Config, Wrapper string
		Layers          []string
	}{env[EnvLambdaConfig], env[store.EnvExecWrapper], sorted})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func snapshotTagRef(value string) (managedManifest, error) {
	if !strings.HasPrefix(value, "v2:") || len(value) != 67 {
		return managedManifest{}, fmt.Errorf("invalid Odigos Lambda snapshot tag")
	}
	return managedManifest{Version: 2, SnapshotID: strings.TrimPrefix(value, "v2:")}, nil
}

// Read current ownership and recover an accepted update whose tag promotion was interrupted.
// A separate pending reference is written BEFORE AWS configuration changes; it never replaces
// the accepted reference until the change succeeds. No rollback state lives in resource tags.
func (i *LambdaInstrumentor) readLambdaTags(ctx context.Context, client *lambda.Client, fn store.LambdaFunction, env map[string]string, layers []string) (map[string]string, error) {
	result, err := client.ListTags(ctx, &lambda.ListTagsInput{Resource: aws.String(fn.FunctionARN)})
	if err != nil {
		return nil, fmt.Errorf("read Lambda ownership tags: %w", err)
	}
	tags := result.Tags
	selected := tags[store.TagLambdaManaged]
	if pending := tags[store.TagLambdaPending]; pending != "" {
		ref, err := snapshotTagRef(pending)
		if err != nil {
			return nil, err
		}
		m, err := i.loadManifest(ctx, fn.FunctionARN, ref)
		if err != nil {
			return nil, err
		}
		if m.AppliedFingerprint == lambdaAppliedFingerprint(env, layers) {
			selected = pending
		}
	}
	virtual := store.LambdaAttributionEnvironment(env, tags)
	if selected != "" {
		ref, err := snapshotTagRef(selected)
		if err != nil {
			return nil, err
		}
		m, err := i.loadManifest(ctx, fn.FunctionARN, ref)
		if err != nil {
			return nil, err
		}
		ref.Layer, ref.CollectorLayer = m.Layer, m.CollectorLayer
		virtual[store.EnvOdigosManaged], err = marshalManifest(ref)
		if err != nil {
			return nil, err
		}
	}
	for _, key := range []string{store.EnvOdigosManaged, store.EnvOdigosIaC} {
		if value, ok := virtual[key]; ok {
			env[key] = value
		}
	}
	return tags, nil
}
func snapshotPriorTags(tags map[string]string) map[string]*string {
	out := map[string]*string{}
	for _, key := range store.LambdaTagKeys {
		out[key] = nil
		if key != store.TagLambdaManaged && key != store.TagLambdaPending {
			if value, ok := tags[key]; ok {
				v := value
				out[key] = &v
			}
		}
	}
	return out
}
func (i *LambdaInstrumentor) markLambdaPending(ctx context.Context, client *lambda.Client, arn, id string) error {
	_, err := client.TagResource(ctx, &lambda.TagResourceInput{Resource: aws.String(arn), Tags: map[string]string{store.TagLambdaPending: "v2:" + id}})
	if err != nil {
		return fmt.Errorf("record pending Lambda instrumentation tag: %w", err)
	}
	return nil
}

// A pending tag must exist before submitting a configuration update so a restart can recover
// its snapshot. Writing the tag invalidates the revision read with that configuration, even
// when the tag value is unchanged. Return false after staging it to force a full re-read;
// only submit the update when the preceding read already saw the desired pending tag.
func (i *LambdaInstrumentor) updateTaggedLambdaConfiguration(ctx context.Context, client *lambda.Client, input *lambda.UpdateFunctionConfigurationInput, tags map[string]string, snapshotID string) (bool, error) {
	if tags[store.TagLambdaPending] != "v2:"+snapshotID {
		if err := i.markLambdaPending(ctx, client, aws.ToString(input.FunctionName), snapshotID); err != nil {
			return false, err
		}
		return false, nil
	}
	_, err := client.UpdateFunctionConfiguration(ctx, input)
	return err == nil, err
}

func (i *LambdaInstrumentor) promoteLambdaTags(ctx context.Context, client *lambda.Client, arn string, m managedManifest) error {
	tags := map[string]string{store.TagLambdaManaged: "v2:" + m.SnapshotID, store.TagLambdaSDK: m.Layer}
	remove := []string{store.TagLambdaPending}
	if m.CollectorLayer != "" {
		tags[store.TagLambdaCollector] = m.CollectorLayer
	} else {
		remove = append(remove, store.TagLambdaCollector)
	}
	if _, err := client.TagResource(ctx, &lambda.TagResourceInput{Resource: aws.String(arn), Tags: tags}); err != nil {
		return fmt.Errorf("promote Lambda instrumentation tags: %w", err)
	}
	if _, err := client.UntagResource(ctx, &lambda.UntagResourceInput{Resource: aws.String(arn), TagKeys: remove}); err != nil {
		return fmt.Errorf("remove pending Lambda instrumentation tag: %w", err)
	}
	return nil
}
func restoreLambdaTags(ctx context.Context, client *lambda.Client, arn string, previous map[string]*string) error {
	set := map[string]string{}
	remove := []string{store.TagLambdaManaged, store.TagLambdaPending}
	for _, key := range []string{store.TagLambdaIaC, store.TagLambdaSDK, store.TagLambdaCollector} {
		if value := previous[key]; value != nil {
			set[key] = *value
		} else {
			remove = append(remove, key)
		}
	}
	if len(set) > 0 {
		if _, err := client.TagResource(ctx, &lambda.TagResourceInput{Resource: aws.String(arn), Tags: set}); err != nil {
			return err
		}
	}
	_, err := client.UntagResource(ctx, &lambda.UntagResourceInput{Resource: aws.String(arn), TagKeys: remove})
	return err
}

func validateLambdaTagCapacity(tags map[string]string, collector bool) error {
	keys := map[string]bool{}
	for key := range tags {
		if !strings.HasPrefix(key, "aws:") {
			keys[key] = true
		}
	}
	for _, key := range []string{store.TagLambdaManaged, store.TagLambdaPending, store.TagLambdaSDK} {
		keys[key] = true
	}
	if collector {
		keys[store.TagLambdaCollector] = true
	}
	if len(keys) > 50 {
		return fmt.Errorf("Lambda instrumentation needs %d resource tags including pending ownership, exceeding AWS's 50-tag limit", len(keys))
	}
	return nil
}

// Taking over an IaC-instrumented function must not turn its original layer/wrapper into
// connector-owned additions. Keep enough state to return it to the customer's deployment.
func preserveIaCOriginal(m *managedManifest, env, tags map[string]string, layers []string) {
	if env[store.EnvOdigosManaged] != "" || tags[store.TagLambdaManaged] != "" {
		return
	}
	sdk, collector := tags[store.TagLambdaSDK], tags[store.TagLambdaCollector]
	if tags[store.TagLambdaIaC] == "" {
		var marker struct {
			Version               int `json:"v"`
			Layer, CollectorLayer string
		}
		if json.Unmarshal([]byte(env[store.EnvOdigosIaC]), &marker) != nil || marker.Version != 1 {
			return
		}
		sdk, collector = marker.Layer, marker.CollectorLayer
		version := "v1"
		m.PrevTags[store.TagLambdaIaC] = &version
		if sdk != "" {
			v := sdk
			m.PrevTags[store.TagLambdaSDK] = &v
		}
		if collector != "" {
			v := collector
			m.PrevTags[store.TagLambdaCollector] = &v
		}
	}
	if wrapper, ok := env[store.EnvExecWrapper]; ok {
		value := wrapper
		m.PrevEnv[store.EnvExecWrapper] = &value
	}
	for _, arn := range []string{sdk, collector} {
		if arn != "" && contains(layers, arn) {
			m.PriorLayers = append(m.PriorLayers, arn)
		}
	}
}

// Public upstream layers use the same wrapper paths. A familiar filename alone does
// not make a previously unowned function's original wrapper an Odigos addition.
func preserveInitialLambdaWrapper(m *managedManifest, env map[string]string) {
	if env[store.EnvOdigosManaged] != "" || env[EnvCollectorEndpoint] != "" || env[EnvCollectorInsecure] != "" {
		return
	}
	if wrapper, ok := env[store.EnvExecWrapper]; ok {
		value := wrapper
		m.PrevEnv[store.EnvExecWrapper] = &value
	}
}
