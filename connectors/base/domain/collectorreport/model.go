package collectorreport

import (
	"sort"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// The structured report, beside the prose one in report.go.
//
// Both render the same facts and neither is derived from the other, which is deliberate: Report.Message
// is one sentence a human reads first and is free to be reworded, while this is what a client BRANCHES
// on. Deriving either from the other would make a wording change a client-visible one (§9.3).
//
// The split that carries the weight here is desired-vs-current. A Collector's configured size and its
// running size are different facts, and a report that showed only one of them makes drift invisible:
// mid-rollout they differ legitimately, and a week later the same difference is a stuck convergence.
// Reporting both is what lets a reader tell those apart without a provider console.

// ModelInput is everything the structured report renders, flattened.
//
// A separate input from Input rather than an extension of it, because this package must not import the
// store: the persistence layer maps its row into this once, in the coordinator, and every field here is
// therefore already the shape the wire wants. The alternative — importing store — would put a database
// dependency behind a pure rendering package that is otherwise trivially testable.
type ModelInput struct {
	// Identity. Region alone; HostNetworkID is where it runs.
	Region        string
	HostNetworkID string
	Kind          string

	// Status inputs, as report.go's Input takes them — the two renderings must agree on the state
	// machine, so both go through statusFor.
	Status            collectorstatus.Status
	Teardown          bool
	RecordedMessage   string
	Mode              string
	Origin            string
	Adopted           bool
	OpAMPLastSeenAt   *time.Time
	OpAMPInstanceUID  string
	AgentHealth       string
	HeartbeatInterval time.Duration
	Problems          []collectorstatus.Problem

	// Desired: the resolved configuration for this region.
	Desired collectorsettings.Region

	// Current: what is deployed, as last observed.
	EndpointGRPC   string
	EndpointHTTP   string
	EndpointSource string
	TLSMode        collectoringress.TLSMode
	TLSTermination string
	IngressTier    collectoringress.Tier

	ScalingMode  string
	DesiredCount *int
	RunningCount *int
	MinCount     *int
	MaxCount     *int

	ImageDigest      string
	ImageTag         string
	ImageArch        string
	LicenseExpiresAt *time.Time

	ConfigHash     string
	ConfigPushedAt *time.Time
	FirstReadyAt   *time.Time

	// Authority. Attribution is computed by the caller, which is the only thing holding the connector
	// identity the tags are compared against.
	Attribution string
	Mutable     bool
	Deletable   bool

	// Infrastructure refs, for the provider-specific details block. Provider selects which wire type is
	// built; an unrecognized one reports no details rather than an empty AWS block.
	Provider        string
	WorkloadRef     string
	RevisionRef     string
	ClusterRef      string
	LoadBalancerRef string
	TargetGroupRefs []string
	SecurityGroupID string
	SubnetIDs       []string

	// ExtraIssues are report-only conditions that do not describe Collector health, such as workload
	// routing progress, or conditions with typed details this package cannot reconstruct from a Problem.
	// Merged with the Collector's problems and sorted with them, but never used to derive its status.
	ExtraIssues []*model.ManagedCollectorIssue

	UpdatedAt time.Time
	Now       time.Time
}

// Providers whose infrastructure refs have a typed details block.
const ProviderAWS = "aws"

// ToModel renders the structured report.
func ToModel(in ModelInput) *model.ManagedCollectorReport {
	liveness := LivenessOf(in.OpAMPLastSeenAt, in.HeartbeatInterval, in.Now)
	statusInput := Input{
		Region:            in.Region,
		NetworkID:         in.HostNetworkID,
		Mode:              in.Mode,
		Origin:            in.Origin,
		Adopted:           in.Adopted,
		Status:            in.Status,
		Teardown:          in.Teardown,
		RecordedMessage:   in.RecordedMessage,
		OpAMPLastSeenAt:   in.OpAMPLastSeenAt,
		OpAMPInstanceUID:  in.OpAMPInstanceUID,
		AgentHealth:       in.AgentHealth,
		HeartbeatInterval: in.HeartbeatInterval,
		EndpointGRPC:      in.EndpointGRPC,
		EndpointHTTP:      in.EndpointHTTP,
		TLSMode:           in.TLSMode,
		IngressTier:       in.IngressTier,
		DesiredCount:      derefInt(in.DesiredCount),
		RunningCount:      derefInt(in.RunningCount),
		ConfigHash:        in.ConfigHash,
		ConfigPushedAt:    in.ConfigPushedAt,
		Problems:          in.Problems,
		Now:               in.Now,
	}
	prose := Build(statusInput)

	out := &model.ManagedCollectorReport{
		Region:    in.Region,
		NetworkID: in.HostNetworkID,
		Kind:      in.Kind,
		Status:    string(prose.Status),
		Message:   prose.Message,
		Liveness:  string(liveness),
		Desired:   desiredModel(in.Desired),
		Current:   currentModel(in),
		Authority: authorityModel(in),
		Issues:    issueModels(in),
		Details:   detailsModel(in),
		UpdatedAt: rfc3339(&in.UpdatedAt),
	}
	if in.OpAMPInstanceUID != "" {
		out.InstanceUID = &in.OpAMPInstanceUID
	}
	return out
}

// ToModels renders a fleet in a stable order, so two reads of unchanged state produce the same list.
func ToModels(inputs []ModelInput) []*model.ManagedCollectorReport {
	out := make([]*model.ManagedCollectorReport, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, ToModel(in))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}

func desiredModel(d collectorsettings.Region) *model.ManagedCollectorDesired {
	out := &model.ManagedCollectorDesired{
		Enabled:          d.Enabled,
		CPU:              d.CPU,
		Memory:           d.Memory,
		LogRetentionDays: d.LogRetentionDays,
		Arch:             d.Arch,
		Scaling: &model.ManagedCollectorScalingTarget{
			Mode:                d.Scaling.Mode,
			DesiredCount:        nonZeroInt(d.Scaling.DesiredCount),
			MinCount:            nonZeroInt(d.Scaling.MinCount),
			MaxCount:            nonZeroInt(d.Scaling.MaxCount),
			TargetCPUPercent:    nonZeroInt(d.Scaling.TargetCPUPercent),
			TargetMemoryPercent: nonZeroInt(d.Scaling.TargetMemoryPercent),
		},
		Retention: &model.ManagedCollectorRetention{
			IdleScaleToZeroAfterHours: d.Retention.IdleScaleToZeroAfterHours,
			IdleDeleteAfterHours:      d.Retention.IdleDeleteAfterHours,
		},
		Transport: &model.ManagedCollectorTransport{
			Mode:           nonEmpty(string(d.TLS.Mode)),
			Hostname:       nonEmpty(d.TLS.Hostname),
			CertificateRef: nonEmpty(d.TLS.CertificateRef),
		},
		Ingress: &model.ManagedCollectorIngress{
			AdditionalCidrs: d.Ingress.AdditionalCIDRs,
		},
		ProviderOverrides: providerOverridesModel(d.Provider, d.Ingress),
	}
	out.NetworkID = nonEmpty(d.HostNetworkID)
	out.ImageRepositoryOverride = nonEmpty(d.ImageRepositoryOverride)
	out.EndpointOverride = nonEmpty(d.EndpointOverride)
	return out
}

func providerOverridesModel(provider map[string]any, ingress collectoringress.Config) model.ManagedCollectorProviderOverrides {
	return &model.AWSManagedCollectorProviderOverrides{
		Provider:                   ProviderAWS,
		SubnetIds:                  providerStrings(provider, "subnetIds"),
		SecurityGroupIds:           providerStrings(provider, "securityGroupIds"),
		ClusterRef:                 nonEmpty(providerString(provider, "clusterArn")),
		ExecutionRoleArn:           nonEmpty(providerString(provider, "executionRoleArn")),
		AssignPublicIP:             providerBool(provider, "assignPublicIp"),
		UseFargateSpot:             providerBool(provider, "useFargateSpot"),
		AdditionalSecurityGroupIds: ingress.AdditionalNetworkIdentityIDs,
		AllowNetworkCidrFallback:   ingress.AllowNetworkCIDRFallback,
		MinFallbackPrefixLength:    nonZeroInt(ingress.MinFallbackPrefixLength),
	}
}

// The shared settings model keeps provider configuration opaque, so this wire adapter is the only
// place that projects the AWS values into the agents-api provider override branch.
func providerString(provider map[string]any, key string) string {
	value, _ := provider[key].(string)
	return value
}

func providerBool(provider map[string]any, key string) bool {
	value, _ := provider[key].(bool)
	return value
}

func providerStrings(provider map[string]any, key string) []string {
	raw, ok := provider[key]
	if !ok {
		return nil
	}
	if values, ok := raw.([]string); ok {
		return append([]string(nil), values...)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

func currentModel(in ModelInput) *model.ManagedCollectorCurrent {
	out := &model.ManagedCollectorCurrent{
		Endpoint: &model.ManagedCollectorEndpoint{
			Grpc:           nonEmpty(in.EndpointGRPC),
			HTTP:           nonEmpty(in.EndpointHTTP),
			Source:         in.EndpointSource,
			TLSMode:        nonEmpty(string(in.TLSMode)),
			TLSTermination: nonEmpty(in.TLSTermination),
			IngressTier:    nonEmpty(string(in.IngressTier)),
		},
		Scaling: &model.ManagedCollectorScalingState{
			Mode:         in.ScalingMode,
			DesiredCount: in.DesiredCount,
			RunningCount: in.RunningCount,
			MinCount:     in.MinCount,
			MaxCount:     in.MaxCount,
		},
		ConfigHash:       nonEmpty(in.ConfigHash),
		ConfigPushedAt:   rfc3339Ptr(in.ConfigPushedAt),
		LicenseExpiresAt: rfc3339Ptr(in.LicenseExpiresAt),
		FirstReadyAt:     rfc3339Ptr(in.FirstReadyAt),
	}
	// The image block is present only once a digest is known. A tag without a digest is not an image
	// identity — a drift check against one can only ever report that the tag still matches (§6.5).
	if in.ImageDigest != "" {
		out.Image = &model.ManagedCollectorImage{
			Digest: in.ImageDigest,
			Tag:    nonEmpty(in.ImageTag),
			Arch:   in.ImageArch,
		}
	}
	return out
}

func authorityModel(in ModelInput) *model.ManagedCollectorAuthority {
	return &model.ManagedCollectorAuthority{
		Mode:        in.Mode,
		Origin:      in.Origin,
		Attribution: in.Attribution,
		Mutable:     in.Mutable,
		Deletable:   in.Deletable,
	}
}

// issueSeverity maps a condition's internal severity to the three the wire carries.
//
// Pending collapses into info rather than warning on purpose: it is the state of a Collector Odigos is
// still building, and reporting every new Collector as a warning for the minute it takes to create one
// would train a reader to ignore warnings.
func issueSeverity(s collectorstatus.Severity) string {
	switch s {
	case collectorstatus.SeverityError:
		return "error"
	case collectorstatus.SeverityDegraded:
		return "warning"
	default:
		return "info"
	}
}

// severityRank orders issues most severe first, which is the order the schema promises.
var severityRank = map[string]int{"error": 0, "warning": 1, "info": 2}

func issueModels(in ModelInput) []*model.ManagedCollectorIssue {
	out := make([]*model.ManagedCollectorIssue, 0, len(in.Problems)+len(in.ExtraIssues))
	for _, p := range in.Problems {
		out = append(out, IssueFromProblem(p))
	}
	out = append(out, in.ExtraIssues...)

	sort.SliceStable(out, func(i, j int) bool {
		return severityRank[out[i].Severity] < severityRank[out[j].Severity]
	})
	return out
}

// IssueFromProblem keeps a problem's wire severity when it is shown for context without changing
// Collector health. Workload routing conditions belong in ExtraIssues, not ModelInput.Problems.
func IssueFromProblem(p collectorstatus.Problem) *model.ManagedCollectorIssue {
	return &model.ManagedCollectorIssue{
		Code:     p.Code,
		Severity: issueSeverity(p.Severity),
		Message:  p.Message,
		// A pending condition clears as convergence finishes; a failure needs another attempt.
		Transient: p.Severity == collectorstatus.SeverityPending,
	}
}

func detailsModel(in ModelInput) model.ManagedCollectorDetails {
	if in.Provider != ProviderAWS {
		// Reporting nothing beats reporting an empty typed block: a client rendering an infrastructure
		// view would show a provider's worth of blank fields for a provider that has none of them.
		return nil
	}
	return &model.AWSManagedCollectorDetails{
		Provider:        in.Provider,
		WorkloadRef:     nonEmpty(in.WorkloadRef),
		RevisionRef:     nonEmpty(in.RevisionRef),
		ClusterRef:      nonEmpty(in.ClusterRef),
		LoadBalancerRef: nonEmpty(in.LoadBalancerRef),
		TargetGroupRefs: in.TargetGroupRefs,
		SecurityGroupID: nonEmpty(in.SecurityGroupID),
		SubnetIds:       in.SubnetIDs,
	}
}

// ConfigurationIssue builds the typed "fix this setting" condition, including the candidates a refusal
// was choosing between. Exported because the caller that detects an ambiguity — placement — is the only
// thing that knows what the candidates were.
func ConfigurationIssue(code, region, settingKey, message string, candidates []string, severity collectorstatus.Severity) *model.ManagedCollectorIssue {
	return &model.ManagedCollectorIssue{
		Code:     code,
		Severity: issueSeverity(severity),
		Message:  message,
		Details: &model.ManagedCollectorConfigurationIssueDetails{
			Code:       code,
			Region:     region,
			SettingKey: settingKey,
			Candidates: candidates,
		},
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nonZeroInt reports an unset numeric setting as absent rather than as zero. The settings reader
// collapses "not configured" to the zero value, and a min count of 0 on the wire would read as a
// deliberate floor of zero replicas.
func nonZeroInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func rfc3339(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	s := rfc3339(t)
	if s == "" {
		return nil
	}
	return &s
}
