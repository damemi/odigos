package source

import (
	"fmt"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

// GraphQL SourceConfig per-kind branch names (config.ecs / config.docker). Kept in sync across
// validation, input flattening, and model echo.
const (
	configBranchECS    = "ecs"
	configBranchDocker = "docker"
)

// Workload kind prefixes / exact kinds that map onto the config branches above. Provider-prefixed
// kinds (aws.fargate-task, aws.ecs-*) share the ecs branch; docker is an exact kind match.
const (
	kindPrefixFargateTask = "aws.fargate-task"
	kindPrefixECS         = "aws.ecs"
	kindDocker            = "docker"
)

// SourceFromCreateInput builds a domain Source from GraphQL create input.
func SourceFromCreateInput(in model.CreateSourceInput) *Source {
	serviceName := stringValue(in.ServiceName)
	if ov, ok := serviceNameFromConfigInput(in.Config); ok {
		serviceName = ov
	}
	return &Source{
		Name:        in.Name,
		Kind:        in.Kind,
		ServiceName: serviceName,
		Region:      stringValue(in.Region),
		Enabled:     in.Enabled,
		Config:      sourceConfigFromInput(in.Config),
	}
}

func serviceNameFromConfigInput(in *model.SourceConfigInput) (string, bool) {
	if in == nil || in.ServiceNameOverride == nil {
		return "", false
	}
	return stringValue(in.ServiceNameOverride.ServiceName), true
}

// ApplyUpdateInput applies GraphQL update input onto the domain Source.
func (s *Source) ApplyUpdateInput(in model.UpdateSourceInput) {
	if s == nil {
		return
	}
	s.Name = in.Name
	if in.Kind != nil {
		s.Kind = *in.Kind
	}
	if in.ServiceName != nil {
		s.ServiceName = *in.ServiceName
	}
	if in.Region != nil {
		s.Region = *in.Region
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.Config != nil {
		s.Config = sourceConfigFromInput(in.Config)
		if ov, ok := serviceNameFromConfigInput(in.Config); ok {
			s.ServiceName = ov
		}
	}
}

func (s *Source) ToDeletePayload() *model.DeleteSourcePayload {
	if s == nil {
		return &model.DeleteSourcePayload{Deleted: false}
	}
	return &model.DeleteSourcePayload{Name: s.Name, Deleted: true}
}

// ToSourceModel converts a domain Source to the agents-api GraphQL model.
func (s *Source) ToSourceModel() *model.Source {
	if s == nil {
		return nil
	}
	var serviceName *string
	if s.ServiceName != "" {
		v := s.ServiceName
		serviceName = &v
	}
	ro := runtimeOverrideToModel(s.Config.RuntimeOverride)
	distOverride := distOverrideToModel(s.Config.DistroOverride)
	var svcOverride *model.ServiceNameOverride
	if s.ServiceName != "" {
		svcOverride = &model.ServiceNameOverride{ServiceName: stringPtrIfNotEmpty(s.ServiceName)}
	}
	ecsCfg, dockerCfg := containerOverridesToModel(s.Kind, s.Config.ContainerOverrides)
	var configModel *model.SourceConfig
	if ro != nil || distOverride != nil || svcOverride != nil || ecsCfg != nil || dockerCfg != nil {
		configModel = &model.SourceConfig{
			RuntimeOverride:     ro,
			DistroOverride:      distOverride,
			ServiceNameOverride: svcOverride,
			Ecs:                 ecsCfg,
			Docker:              dockerCfg,
		}
	}
	return &model.Source{
		Name:        s.Name,
		Kind:        s.Kind,
		ServiceName: serviceName,
		Region:      stringPtrIfNotEmpty(s.Region),
		Enabled:     s.Enabled,
		Config:      configModel,
	}
}

func sourceConfigFromInput(in *model.SourceConfigInput) SourceConfig {
	if in == nil {
		return SourceConfig{}
	}
	return SourceConfig{
		RuntimeOverride:    runtimeOverrideFromInput(in.RuntimeOverride),
		DistroOverride:     distOverrideFromInput(in.DistroOverride),
		ContainerOverrides: containerOverridesFromInput(in),
	}
}

// ValidateConfigInput checks that at most one kind-specific config branch is set and that it
// matches the source's kind. GraphQL can declare the branches but cannot express "exactly one, and
// it must agree with another field", so the rule the schema documents is enforced here — without
// it, a Lambda source would silently accept ECS container overrides that can never apply.
func ValidateConfigInput(kind string, in *model.SourceConfigInput) error {
	set := configBranchesSet(in)
	switch len(set) {
	case 0:
		return nil
	case 1:
		if !kindAcceptsConfigBranch(kind, set[0]) {
			return fmt.Errorf("source config branch %q does not apply to source kind %q", set[0], kind)
		}
		return nil
	default:
		return fmt.Errorf("source config sets %v; at most one kind-specific branch may be set", set)
	}
}

// kindAcceptsConfigBranch maps a connector workload kind onto the config branch it accepts. Kinds
// are provider-prefixed (aws.fargate-task), so the branch names a platform family rather than one
// exact kind: every ECS kind takes the ecs branch.
func kindAcceptsConfigBranch(kind, branch string) bool {
	switch branch {
	case configBranchECS:
		return strings.HasPrefix(kind, kindPrefixFargateTask) || strings.HasPrefix(kind, kindPrefixECS)
	case configBranchDocker:
		return kind == kindDocker
	default:
		return false
	}
}

// configBranchesSet names the kind-specific branches the caller actually filled in.
//
// A branch counts as set only when it CARRIES CONTENT, never merely by being present. Generated
// clients differ on how they serialize an unset optional input object: genqlient (which odictl uses)
// emits every one as `{}` rather than omitting it, so an "is it non-nil" test would see every branch
// set on every request and reject them all. An empty branch is a client artifact, not a customer
// choice, so it says nothing and is ignored.
func configBranchesSet(in *model.SourceConfigInput) []string {
	if in == nil {
		return nil
	}
	set := make([]string, 0, 2)
	if in.Ecs != nil && len(in.Ecs.ContainerOverrides) > 0 {
		set = append(set, configBranchECS)
	}
	if in.Docker != nil && len(in.Docker.ContainerOverrides) > 0 {
		set = append(set, configBranchDocker)
	}
	return set
}

// containerOverridesFromInput flattens whichever per-kind branch carries content. Validation rejects
// more than one, so reading them in order is unambiguous.
func containerOverridesFromInput(in *model.SourceConfigInput) []ContainerOverride {
	if in == nil {
		return nil
	}
	switch {
	case in.Ecs != nil && len(in.Ecs.ContainerOverrides) > 0:
		return containerOverrideList(in.Ecs.ContainerOverrides)
	case in.Docker != nil && len(in.Docker.ContainerOverrides) > 0:
		return containerOverrideList(in.Docker.ContainerOverrides)
	default:
		return nil
	}
}

func containerOverrideList(rows []*model.ContainerOverrideInput) []ContainerOverride {
	if len(rows) == 0 {
		return nil
	}
	out := make([]ContainerOverride, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, ContainerOverride{
			ContainerName:   r.ContainerName,
			RuntimeOverride: runtimeOverrideFromInput(r.RuntimeOverride),
			DistroOverride:  distOverrideFromInput(r.DistroOverride),
			// Absent means "no opinion", which is the same as not disabled — the wire field is an
			// opt-out precisely so this collapse is safe.
			Disabled: r.Disabled != nil && *r.Disabled,
		})
	}
	return out
}

// containerOverridesToModel echoes container overrides back under the branch matching the kind.
func containerOverridesToModel(kind string, rows []ContainerOverride) (*model.EcsSourceConfig, *model.DockerSourceConfig) {
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]*model.ContainerOverride, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, &model.ContainerOverride{
			ContainerName:   r.ContainerName,
			RuntimeOverride: runtimeOverrideToModel(r.RuntimeOverride),
			DistroOverride:  distOverrideToModel(r.DistroOverride),
			// Send the flag only when set, so an unremarkable container carries no field rather than
			// an explicit `disabled: false`.
			Disabled: boolPtrIfTrue(r.Disabled),
		})
	}
	switch {
	case kindAcceptsConfigBranch(kind, configBranchECS):
		return &model.EcsSourceConfig{ContainerOverrides: out}, nil
	case kindAcceptsConfigBranch(kind, configBranchDocker):
		return nil, &model.DockerSourceConfig{ContainerOverrides: out}
	default:
		return nil, nil
	}
}

func runtimeOverrideFromInput(in *model.RuntimeOverrideInput) RuntimeOverride {
	if in == nil {
		return RuntimeOverride{}
	}
	return RuntimeOverride{
		Language: stringValue(in.Language),
		Version:  stringValue(in.Version),
	}
}

func runtimeOverrideToModel(in RuntimeOverride) *model.RuntimeOverride {
	if in.Language == "" && in.Version == "" {
		return nil
	}
	return &model.RuntimeOverride{
		Language: stringPtrIfNotEmpty(in.Language),
		Version:  stringPtrIfNotEmpty(in.Version),
	}
}

func distOverrideFromInput(in *model.DistroOverrideInput) DistroOverride {
	if in == nil {
		return DistroOverride{}
	}
	return DistroOverride{OtelDistroName: stringValue(in.OtelDistroName)}
}

func distOverrideToModel(in DistroOverride) *model.DistroOverride {
	if in.OtelDistroName == "" {
		return nil
	}
	return &model.DistroOverride{OtelDistroName: stringPtrIfNotEmpty(in.OtelDistroName)}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func stringPtrIfNotEmpty(v string) *string {
	if v == "" {
		return nil
	}
	s := v
	return &s
}

// boolPtrIfTrue returns a pointer only for true, so a false flag travels as an absent field rather
// than an explicit negative.
func boolPtrIfTrue(v bool) *bool {
	if !v {
		return nil
	}
	b := true
	return &b
}
