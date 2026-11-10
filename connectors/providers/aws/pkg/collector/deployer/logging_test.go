package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func logGroupInput() LogGroupInput {
	return LogGroupInput{
		Connector: testConnector, ConnectorID: testUID,
		Partition: "aws", AccountID: testAccount, Region: testRegion,
		RetentionDays: 7, Mode: spec.ModeAutomatic,
	}
}

func TestEnsureLogGroupCreatesTaggedAndSetsRetention(t *testing.T) {
	api := newFakeLogs()
	name, problems, err := EnsureLogGroup(context.Background(), api, logGroupInput())
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLogGroup: %v / %v", err, problems)
	}
	if name != LogGroupName(testConnector) {
		t.Fatalf("name = %q", name)
	}
	if !baseapi.ParseCollectorTags(api.groups[name]).HasOwnershipMarker() {
		t.Errorf("log group tags = %v; without the ownership marker it could never be deleted at teardown", api.groups[name])
	}
	if api.retention[name] != 7 {
		t.Errorf("retention = %d, want 7", api.retention[name])
	}
}

// TestRetentionIsWrittenEveryPass. Retention is live operational config, so lowering it through
// `updateConfig` has to take effect without recreating anything — and recreating a log group to change
// its retention would throw away the logs.
func TestRetentionIsWrittenEveryPass(t *testing.T) {
	api := newFakeLogs()
	in := logGroupInput()
	if _, _, err := EnsureLogGroup(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	in.RetentionDays = 30
	api.calls = nil
	if _, _, err := EnsureLogGroup(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}
	if api.retention[LogGroupName(testConnector)] != 30 {
		t.Errorf("retention = %d, want the new 30", api.retention[LogGroupName(testConnector)])
	}
	// Create-and-tolerate is the idempotency mechanism, so a second CreateLogGroup call is expected —
	// what must not happen is a second GROUP.
	if len(api.groups) != 1 {
		t.Errorf("log groups = %d, want the one", len(api.groups))
	}
}

// TestAnExistingLogGroupIsTaggedRatherThanRecreated, which is also how a group Odigos created before
// the tags existed becomes attributable.
func TestAnExistingLogGroupIsTaggedRatherThanRecreated(t *testing.T) {
	api := newFakeLogs()
	name := LogGroupName(testConnector)
	api.groups[name] = map[string]string{"team": "platform"}

	if _, problems, err := EnsureLogGroup(context.Background(), api, logGroupInput()); err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLogGroup: %v / %v", err, problems)
	}
	var tagged bool
	for _, c := range api.calls {
		if strings.HasPrefix(c, "TagResource:arn:aws:logs:"+testRegion+":"+testAccount+":log-group:"+name) {
			tagged = true
		}
	}
	if !tagged {
		t.Errorf("an existing log group was not tagged: %v", api.calls)
	}
}

// TestATaggingFailureDoesNotStopProvisioning. A log group is append-only and holds nothing but the
// Collector's own stdout, so the worst case of proceeding is our logs next to the customer's — whereas
// refusing means no Collector because a group with a plausible name was already there.
func TestATaggingFailureDoesNotStopProvisioning(t *testing.T) {
	api := newFakeLogs()
	api.groups[LogGroupName(testConnector)] = nil
	api.tagResourceEr = errors.New("access denied")

	name, problems, err := EnsureLogGroup(context.Background(), api, logGroupInput())
	if err != nil {
		t.Fatalf("EnsureLogGroup: %v", err)
	}
	if name == "" {
		t.Fatal("no log group returned; a tagging failure must not stop provisioning")
	}
	if collectorstatus.HasError(problems) {
		t.Errorf("problems = %v, want nothing fatal", problems)
	}
}

// TestARetentionFailureIsDegradedNotFatal. The group exists and the Collector can write to it; the only
// loss is logs kept longer than asked, which costs money rather than telemetry.
func TestARetentionFailureIsDegradedNotFatal(t *testing.T) {
	api := newFakeLogs()
	api.retentionErr = errors.New("access denied")

	name, problems, err := EnsureLogGroup(context.Background(), api, logGroupInput())
	if err != nil {
		t.Fatalf("EnsureLogGroup: %v", err)
	}
	if name == "" {
		t.Fatal("no log group returned")
	}
	if len(problems) != 1 || problems[0].Code != ProblemLogRetentionUnset {
		t.Fatalf("problems = %v, want the retention report", problems)
	}
	if problems[0].Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problems[0].Severity)
	}
	if !strings.Contains(problems[0].Message, "storage, not") {
		t.Errorf("message does not say what is actually lost: %s", problems[0].Message)
	}
}

func TestManualModeCreatesNoLogGroup(t *testing.T) {
	api := newFakeLogs()
	in := logGroupInput()
	in.Mode = spec.ModeManual

	name, problems, err := EnsureLogGroup(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureLogGroup: %v", err)
	}
	if name != "" || len(api.calls) != 0 {
		t.Errorf("created a log group in manual mode: %q / %v", name, api.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", problems)
	}
}
