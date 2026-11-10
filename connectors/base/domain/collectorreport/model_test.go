package collectorreport

import (
	"testing"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

func baseInput() ModelInput {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	return ModelInput{
		Region:        "eu-west-1",
		HostNetworkID: "vpc-1",
		Kind:          "aws.fargate-collector",
		Status:        collectorstatus.StatusReady,
		Mode:          "automatic",
		Origin:        "odigos",
		Desired: collectorsettings.Region{
			Region:  "eu-west-1",
			Network: collectorsettings.Network{Enabled: true, CPU: "1024", Memory: "2048"},
		},
		Provider:  ProviderAWS,
		UpdatedAt: now,
		Now:       now,
	}
}

func TestToModelCarriesIdentityAndDesired(t *testing.T) {
	in := baseInput()
	in.Desired.HostNetworkID = "vpc-1"
	got := ToModel(in)

	if got.Region != "eu-west-1" || got.NetworkID != "vpc-1" {
		t.Errorf("identity = (%q, %q)", got.Region, got.NetworkID)
	}
	if got.Kind != "aws.fargate-collector" {
		t.Errorf("kind = %q", got.Kind)
	}
	if !got.Desired.Enabled || got.Desired.CPU != "1024" || got.Desired.Memory != "2048" {
		t.Errorf("desired = %+v", got.Desired)
	}
	if got.Desired.NetworkID == nil || *got.Desired.NetworkID != "vpc-1" {
		t.Errorf("desired networkId = %v", got.Desired.NetworkID)
	}
}

func TestToModelCarriesRetentionPolicy(t *testing.T) {
	in := baseInput()
	in.Desired.Retention = collectorsettings.Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 48}
	got := ToModel(in).Desired.Retention
	if got == nil || got.IdleScaleToZeroAfterHours != 6 || got.IdleDeleteAfterHours != 48 {
		t.Fatalf("retention = %+v", got)
	}
}

// The prose message and the structured status must come from the same state machine, or a client
// branching on one and a human reading the other would see different Collectors.
func TestStatusAgreesWithTheProseReport(t *testing.T) {
	in := baseInput()
	in.Status = collectorstatus.StatusDeploying
	got := ToModel(in)

	prose := Build(Input{
		Region: in.Region, Mode: in.Mode, Origin: in.Origin,
		Status: in.Status, HeartbeatInterval: in.HeartbeatInterval, Now: in.Now,
	})
	if got.Status != string(prose.Status) {
		t.Errorf("structured status %q disagrees with the prose report's %q", got.Status, prose.Status)
	}
	if got.Message != prose.Message {
		t.Errorf("message = %q, want the prose report's %q", got.Message, prose.Message)
	}
}

func TestLivenessNeverConnectedWithoutASession(t *testing.T) {
	if got := ToModel(baseInput()); got.Liveness != string(LivenessNeverConnected) {
		t.Errorf("liveness = %q, want %q", got.Liveness, LivenessNeverConnected)
	}
	if got := ToModel(baseInput()); got.InstanceUID != nil {
		t.Errorf("instanceUid = %v, want null for a Collector that never connected", *got.InstanceUID)
	}
}

// Authority is two independent bits. Only automatic+odigos may be deleted; the other combinations are
// converged or observed but never destroyed.
func TestAuthorityReflectsModeAndOrigin(t *testing.T) {
	cases := []struct {
		name               string
		mode, origin       string
		attribution        string
		mutable, deletable bool
	}{
		{"automatic odigos", "automatic", "odigos", "mine", true, true},
		{"automatic customer", "automatic", "customer", "mine", true, false},
		{"manual odigos", "manual", "odigos", "mine", false, false},
		{"manual customer", "manual", "customer", "mine", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			in.Mode, in.Origin = tc.mode, tc.origin
			in.Attribution = tc.attribution
			in.Mutable, in.Deletable = tc.mutable, tc.deletable

			got := ToModel(in).Authority
			if got.Mode != tc.mode || got.Origin != tc.origin {
				t.Errorf("authority = %+v", got)
			}
			if got.Mutable != tc.mutable || got.Deletable != tc.deletable {
				t.Errorf("mutable/deletable = %v/%v, want %v/%v", got.Mutable, got.Deletable, tc.mutable, tc.deletable)
			}
		})
	}
}

// A tag is not an image identity: a drift check against one can only ever report that the tag still
// matches, so the image block appears only once a digest is known.
func TestImageOnlyAppearsWithADigest(t *testing.T) {
	in := baseInput()
	in.ImageTag = "v1.2.3"
	if got := ToModel(in); got.Current.Image != nil {
		t.Errorf("an image block was rendered from a tag alone: %+v", got.Current.Image)
	}

	in.ImageDigest = "sha256:abc"
	in.ImageArch = "arm64"
	got := ToModel(in)
	if got.Current.Image == nil || got.Current.Image.Digest != "sha256:abc" {
		t.Fatalf("image = %+v", got.Current.Image)
	}
	if got.Current.Image.Tag == nil || *got.Current.Image.Tag != "v1.2.3" {
		t.Errorf("tag = %v", got.Current.Image.Tag)
	}
}

// An unset numeric setting must not render as a deliberate zero — a min count of 0 on the wire reads as
// a configured floor of zero replicas.
func TestUnsetScalingNumbersAreAbsentNotZero(t *testing.T) {
	got := ToModel(baseInput())
	scaling := got.Desired.Scaling
	if scaling.MinCount != nil || scaling.MaxCount != nil || scaling.TargetCPUPercent != nil {
		t.Errorf("unset scaling numbers rendered as values: %+v", scaling)
	}
}

func TestCurrentCarriesEndpointAndLatches(t *testing.T) {
	firstReady := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	in := baseInput()
	in.EndpointGRPC = "collector.internal:4317"
	in.EndpointSource = "created"
	in.TLSMode = collectoringress.TLSOff
	in.IngressTier = collectoringress.TierNetworkIdentities
	in.FirstReadyAt = &firstReady

	got := ToModel(in).Current
	if got.Endpoint.Grpc == nil || *got.Endpoint.Grpc != "collector.internal:4317" {
		t.Errorf("grpc endpoint = %v", got.Endpoint.Grpc)
	}
	if got.Endpoint.HTTP != nil {
		t.Errorf("an absent http endpoint rendered as %v", *got.Endpoint.HTTP)
	}
	if got.FirstReadyAt == nil || *got.FirstReadyAt != "2026-09-14T08:00:00Z" {
		t.Errorf("firstReadyAt = %v", got.FirstReadyAt)
	}
}

// Most severe first, so a client rendering only the first issue shows the one that matters.
func TestIssuesAreOrderedMostSevereFirst(t *testing.T) {
	in := baseInput()
	in.Problems = []collectorstatus.Problem{
		{Code: "PlaintextDefault", Severity: collectorstatus.SeverityInfo, Message: "plaintext"},
		{Code: "ConfigRejected", Severity: collectorstatus.SeverityError, Message: "refused"},
		{Code: "CidrFallback", Severity: collectorstatus.SeverityDegraded, Message: "fallback"},
	}
	got := ToModel(in).Issues

	want := []string{"error", "warning", "info"}
	for idx, severity := range want {
		if got[idx].Severity != severity {
			t.Fatalf("issue %d severity = %q, want %q", idx, got[idx].Severity, severity)
		}
	}
}

// A pending condition clears without anyone doing anything; a failure does not, and telling them apart
// is the difference between "wait" and "go fix something".
func TestPendingIssuesAreTransient(t *testing.T) {
	in := baseInput()
	in.Problems = []collectorstatus.Problem{
		{Code: "Converging", Severity: collectorstatus.SeverityPending, Message: "building"},
		{Code: "ConfigRejected", Severity: collectorstatus.SeverityError, Message: "refused"},
	}
	for _, issue := range ToModel(in).Issues {
		wantTransient := issue.Code == "Converging"
		if issue.Transient != wantTransient {
			t.Errorf("issue %q transient = %v, want %v", issue.Code, issue.Transient, wantTransient)
		}
	}
}

func TestExtraIssuesCarryTypedDetails(t *testing.T) {
	in := baseInput()
	in.ExtraIssues = []*model.ManagedCollectorIssue{
		ConfigurationIssue("AmbiguousHostNetwork", "eu-west-1", "networkId",
			"several networks are eligible", []string{"vpc-1", "vpc-2"}, collectorstatus.SeverityError),
	}
	issues := ToModel(in).Issues
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}

	details, ok := issues[0].Details.(*model.ManagedCollectorConfigurationIssueDetails)
	if !ok {
		t.Fatalf("details = %T, want *model.ManagedCollectorConfigurationIssueDetails", issues[0].Details)
	}
	if details.Region != "eu-west-1" || len(details.Candidates) != 2 {
		t.Errorf("details = %+v", details)
	}
	// "Odigos refused to guess" is only actionable if the reader is told what it was choosing between.
	if details.Candidates[0] != "vpc-1" || details.Candidates[1] != "vpc-2" {
		t.Errorf("candidates = %v", details.Candidates)
	}
}

// A client rendering an infrastructure view must not be shown a provider's worth of blank fields for a
// provider that has none of them.
func TestDetailsOnlyForAKnownProvider(t *testing.T) {
	in := baseInput()
	in.WorkloadRef = "arn:aws:ecs:::service/collector"
	in.SubnetIDs = []string{"subnet-1"}

	aws, ok := ToModel(in).Details.(*model.AWSManagedCollectorDetails)
	if !ok {
		t.Fatalf("details = %T, want the AWS block", ToModel(in).Details)
	}
	if aws.WorkloadRef == nil || *aws.WorkloadRef != "arn:aws:ecs:::service/collector" {
		t.Errorf("workloadRef = %v", aws.WorkloadRef)
	}

	in.Provider = "gcp"
	if got := ToModel(in); got.Details != nil {
		t.Errorf("details = %+v, want null for a provider with no typed block", got.Details)
	}
}

// Two reads of unchanged state must produce the same list rather than a reshuffle.
func TestToModelsIsOrderedByRegion(t *testing.T) {
	inputs := []ModelInput{}
	for _, region := range []string{"us-east-1", "ap-south-1", "eu-west-1"} {
		in := baseInput()
		in.Region = region
		inputs = append(inputs, in)
	}
	got := ToModels(inputs)

	want := []string{"ap-south-1", "eu-west-1", "us-east-1"}
	for idx := range want {
		if got[idx].Region != want[idx] {
			t.Fatalf("order = %v, want %v", regions(got), want)
		}
	}
}

func regions(reports []*model.ManagedCollectorReport) []string {
	out := make([]string, 0, len(reports))
	for _, r := range reports {
		out = append(out, r.Region)
	}
	return out
}
