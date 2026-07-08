package source

import (
	"github.com/odigos-io/agents-api/go/server/graph/model"
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
	var configModel *model.SourceConfig
	if ro != nil || distOverride != nil || svcOverride != nil {
		configModel = &model.SourceConfig{
			RuntimeOverride:     ro,
			DistroOverride:      distOverride,
			ServiceNameOverride: svcOverride,
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
		RuntimeOverride: runtimeOverrideFromInput(in.RuntimeOverride),
		DistroOverride:  distOverrideFromInput(in.DistroOverride),
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
