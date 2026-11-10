package detector

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// TestLayerARNsCapturesTheAttachedSet covers the field discovery newly keeps. ListFunctions has always returned
// the attached layers and they were always discarded, which is why "the ownership marker survived but the layer
// was detached" — a workload reporting as instrumented while producing no telemetry — had no detector.
func TestLayerARNsCapturesTheAttachedSet(t *testing.T) {
	if got := layerARNs(nil); len(got) != 0 {
		t.Errorf("no layers = %v, want empty", got)
	}
	got := layerARNs([]lambdatypes.Layer{
		{Arn: aws.String("arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:7")},
		// An entry without an ARN is unusable; keeping it as "" would put an empty string in the observed set
		// and make it look like a layer nobody can identify.
		{Arn: nil},
		{Arn: aws.String("arn:aws:lambda:us-east-1:1:layer:LambdaAdapterLayerX86:25")},
	})
	if len(got) != 2 {
		t.Fatalf("layers = %v, want the 2 identifiable ARNs", got)
	}
	// The captured set has to be exactly what the store's ownership predicate reads, since that is what fills
	// a Lambda report's layerArn and what the observer diffs against.
	if arn := store.OdigosLayerARN(got); arn != got[0] {
		t.Errorf("OdigosLayerARN over the captured set = %q, want %q", arn, got[0])
	}
}

// TestLifecycleFieldsAreCapturedAsStrings pins the hand-off for the two other newly-kept fields. They are stored
// as plain strings (the store persists data, not SDK enums) and the report's SQL compares them to its own
// constants, so a rename on either side of that boundary has to be caught here — nothing else compares the
// stored form to what AWS actually returns.
func TestLifecycleFieldsAreCapturedAsStrings(t *testing.T) {
	fn := store.LambdaFunction{
		State:            string(lambdatypes.StatePending),
		LastUpdateStatus: string(lambdatypes.LastUpdateStatusInProgress),
	}
	if fn.State != "Pending" || fn.LastUpdateStatus != "InProgress" {
		t.Errorf("captured lifecycle = (%q, %q), want (Pending, InProgress) — the report query matches these literals",
			fn.State, fn.LastUpdateStatus)
	}
}
