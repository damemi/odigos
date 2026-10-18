package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

func TestTaggedLambdaUpdateRereadsAfterTaggingAndPreservesRevisionGuard(t *testing.T) {
	ctx := context.Background()
	arn := "arn:aws:lambda:us-east-1:123456789012:function:sample"
	revision := "before-tag"
	tags := map[string]string{}
	tagWrites, updates := 0, 0
	var appliedEnv map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"RevisionId": revision, "Environment": map[string]any{"Variables": map[string]string{"CUSTOMER": "edited during staging"}}})
		case http.MethodPost:
			tagWrites++
			var body struct{ Tags map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k, v := range body.Tags {
				tags[k] = v
			}
			revision = "after-tag"
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			updates++
			var body struct {
				RevisionID  string `json:"RevisionId"`
				Environment struct{ Variables map[string]string }
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.RevisionID != revision {
				w.Header().Set("X-Amzn-ErrorType", "PreconditionFailedException")
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"Type":"User","message":"revision mismatch"}`))
				return
			}
			appliedEnv = body.Environment.Variables
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := lambda.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}, func(o *lambda.Options) { o.BaseEndpoint = aws.String(server.URL); o.RetryMaxAttempts = 1 })
	i := &LambdaInstrumentor{}
	stale := lambdaConfigUpdateInput(arn, []string{"layer"}, map[string]string{"CUSTOMER": "before staging"}, aws.String(revision))
	applied, err := i.updateTaggedLambdaConfiguration(ctx, client, stale, map[string]string{}, "snapshot")
	if err != nil || applied || tagWrites != 1 || updates != 0 {
		t.Fatalf("staging must not submit stale configuration: applied=%v err=%v tags=%d updates=%d", applied, err, tagWrites, updates)
	}
	// The caller re-reads and rebuilds the merge, preserving a customer's concurrent edit.
	cur, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(arn)})
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := currentEnv(cur.Environment)
	wantEnv[EnvLambdaConfig] = `{"v":1}`
	fresh := lambdaConfigUpdateInput(arn, []string{"layer"}, wantEnv, cur.RevisionId)
	applied, err = i.updateTaggedLambdaConfiguration(ctx, client, fresh, tags, "snapshot")
	if err != nil || !applied || tagWrites != 1 || updates != 1 || !reflect.DeepEqual(appliedEnv, wantEnv) {
		t.Fatalf("fresh update did not converge without re-tagging: applied=%v err=%v tags=%d updates=%d env=%v", applied, err, tagWrites, updates, appliedEnv)
	}
	// A real competing deployment must still fail the optimistic concurrency check.
	revision = "customer-deployment"
	applied, err = i.updateTaggedLambdaConfiguration(ctx, client, fresh, tags, "snapshot")
	if applied || !revisionConflict(err) || tagWrites != 1 {
		t.Fatalf("customer deployment was not protected: applied=%v err=%v tags=%d", applied, err, tagWrites)
	}
}

func TestLambdaTagTransitionSurvivesFailedUpdateAndRestart(t *testing.T) {
	ctx := context.Background()
	tags := map[string]string{"customer-tag": "keep"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"Tags": tags})
		case http.MethodPost:
			var body struct{ Tags map[string]string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for k, v := range body.Tags {
				tags[k] = v
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			for _, key := range r.URL.Query()["tagKeys"] {
				delete(tags, key)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	client := lambda.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}, func(o *lambda.Options) { o.BaseEndpoint = aws.String(server.URL); o.RetryMaxAttempts = 1 })
	db := memorySnapshots{}
	i := &LambdaInstrumentor{snapshots: db}
	fn := store.LambdaFunction{FunctionARN: "arn:aws:lambda:us-east-1:123456789012:function:sample"}
	original := map[string]string{"CUSTOMER": "original"}
	updated := map[string]string{"CUSTOMER": "original", EnvLambdaConfig: `{"v":1,"s":"service","t":7,"e":"collector:4317"}`, store.EnvExecWrapper: "/opt/otel-handler"}
	sdk := "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-nodejs-x86_64:2"
	m := buildManifest(original, sdk)
	m.PrevTags = snapshotPriorTags(tags)
	m.AppliedFingerprint = lambdaAppliedFingerprint(updated, []string{sdk})
	marker, err := i.persistManifest(ctx, fn.FunctionARN, m)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := parseManifest(marker)
	if err := i.markLambdaPending(ctx, client, fn.FunctionARN, ref.SnapshotID); err != nil {
		t.Fatal(err)
	}
	// AWS did not apply the staged change: no ownership of the original function is claimed.
	old := cloneLambdaEnv(original)
	if _, err := i.readLambdaTags(ctx, client, fn, old, nil); err != nil {
		t.Fatal(err)
	}
	if _, owned := i.ownership(ctx, fn, old); owned {
		t.Fatal("failed update claimed original function")
	}
	// The configuration succeeded, then the connector restarted before promoting tags.
	restarted := &LambdaInstrumentor{snapshots: db}
	live := cloneLambdaEnv(updated)
	live["CUSTOMER"] = "edited after update"
	if _, err := restarted.readLambdaTags(ctx, client, fn, live, []string{sdk, "arn:aws:lambda:us-east-1:123456789012:layer:customer:1"}); err != nil {
		t.Fatal(err)
	}
	accepted, owned := restarted.ownership(ctx, fn, live)
	if !owned || accepted.SnapshotID != ref.SnapshotID {
		t.Fatal("accepted pending update could not be recovered")
	}
	if err := restarted.promoteLambdaTags(ctx, client, fn.FunctionARN, accepted); err != nil {
		t.Fatal(err)
	}
	if tags[store.TagLambdaPending] != "" || tags[store.TagLambdaManaged] != "v2:"+ref.SnapshotID || tags[store.TagLambdaSDK] != sdk {
		t.Fatalf("wrong promoted tags: %v", tags)
	}
	for _, value := range tags {
		if len(value) > 256 {
			t.Fatal("tag value exceeds AWS limit")
		}
	}
	// A second attempt fails: the accepted reference must still restore the original snapshot.
	next := m
	next.Layer = sdk + "0"
	next.AppliedFingerprint = "different"
	nextMarker, err := i.persistManifest(ctx, fn.FunctionARN, next)
	if err != nil {
		t.Fatal(err)
	}
	nextRef, _ := parseManifest(nextMarker)
	if err := i.markLambdaPending(ctx, client, fn.FunctionARN, nextRef.SnapshotID); err != nil {
		t.Fatal(err)
	}
	live = cloneLambdaEnv(updated)
	if _, err := restarted.readLambdaTags(ctx, client, fn, live, []string{sdk}); err != nil {
		t.Fatal(err)
	}
	kept, _ := parseManifest(live[store.EnvOdigosManaged])
	if kept.SnapshotID != ref.SnapshotID {
		t.Fatal("failed retry replaced accepted ownership")
	}
	if err := restoreLambdaTags(ctx, client, fn.FunctionARN, m.PrevTags); err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags["customer-tag"] != "keep" {
		t.Fatalf("rollback removed customer tags or stranded ownership: %v", tags)
	}
}

func TestLambdaTagCapacityAndFingerprint(t *testing.T) {
	tags := map[string]string{}
	for n := 0; n < 48; n++ {
		tags[fmt.Sprint(n)] = "customer"
	}
	if err := validateLambdaTagCapacity(tags, true); err == nil {
		t.Fatal("too many tags accepted before function mutation")
	}
	delete(tags, "0")
	delete(tags, "1")
	if err := validateLambdaTagCapacity(tags, true); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotTagRef("v2:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotTagRef("garbage"); err == nil {
		t.Fatal("invalid tag reference accepted")
	}
}

func TestTakingOverIaCPreservesOriginalLayerAndWrapper(t *testing.T) {
	sdk := "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-nodejs-x86_64:1"
	tags := map[string]string{store.TagLambdaIaC: "v1", store.TagLambdaSDK: sdk}
	env := store.LambdaAttributionEnvironment(map[string]string{store.EnvExecWrapper: "/opt/otel-handler", EnvLambdaConfig: `{"v":1,"s":"customer","t":1,"e":"customer:4317"}`}, tags)
	m := buildManifest(env, "new-sdk")
	m.PrevTags = snapshotPriorTags(tags)
	preserveIaCOriginal(&m, env, tags, []string{sdk})
	if len(m.PriorLayers) != 1 || m.PriorLayers[0] != sdk {
		t.Fatal("original IaC layer would be lost")
	}
	if m.PrevEnv[store.EnvExecWrapper] == nil || *m.PrevEnv[store.EnvExecWrapper] != "/opt/otel-handler" {
		t.Fatal("original IaC wrapper would be lost")
	}
	m.AppliedFingerprint = "compact"
	env[store.EnvExecWrapper] = "/opt/new-wrapper"
	restoreLambdaEnvironment(env, m)
	if env[store.EnvExecWrapper] != "/opt/otel-handler" || !strings.Contains(env[EnvLambdaConfig], "customer") {
		t.Fatal("original IaC configuration not restored")
	}
}

func TestInitialCustomerSDKWrapperIsPreserved(t *testing.T) {
	env := map[string]string{store.EnvExecWrapper: "/opt/otel-handler"}
	m := buildManifest(env, "sdk")
	preserveInitialLambdaWrapper(&m, env)
	if m.PrevEnv[store.EnvExecWrapper] == nil || *m.PrevEnv[store.EnvExecWrapper] != "/opt/otel-handler" {
		t.Fatal("customer's upstream SDK wrapper was mistaken for an Odigos addition")
	}
}
