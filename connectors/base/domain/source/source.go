package source

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-playground/validator/v10"
)

var validate = validator.New()

// RuntimeOverride specifies runtime language and version overrides for a source.
type RuntimeOverride struct {
	Language string `json:"runtime_language,omitempty" validate:"omitempty"`
	Version  string `json:"runtime_version,omitempty" validate:"omitempty"`
}

// DistroOverride opts a source into a specific instrumentation distro.
type DistroOverride struct {
	OtelDistroName string `json:"otel_distro_name,omitempty" validate:"omitempty"`
}

// SourceConfig is optional instrumentation configuration for a source.
type SourceConfig struct {
	RuntimeOverride RuntimeOverride `json:"runtime_override,omitempty" validate:"omitempty"`
	DistroOverride  DistroOverride  `json:"distro_override,omitempty" validate:"omitempty"`
}

// Source is connector-local instrumentation intent for a discovered cloud workload.
type Source struct {
	Name               string       `json:"name" validate:"required"`
	Kind               string       `json:"kind" validate:"required"`
	ServiceName        string       `json:"serviceName,omitempty"`
	Enabled            bool         `json:"enabled,omitempty"`
	ExternalResourceID string       `json:"externalResourceId,omitempty"`
	Region             string       `json:"region,omitempty"`
	Config             SourceConfig `json:"config,omitempty" validate:"omitempty"`
}

// ReportedServiceName returns the OTel service.name instrumentation should report for this source.
// ServiceName is the user-editable reported name; it falls back to Name when empty. Neither affects
// workload matching, which keys off Name.
func (s Source) ReportedServiceName() string {
	if s.ServiceName != "" {
		return s.ServiceName
	}
	return s.Name
}

// Validate runs field-level validation rules.
func (s *Source) Validate(supportedKinds []string) error {
	if err := validate.Struct(s); err != nil {
		return err
	}
	if !slices.Contains(supportedKinds, s.Kind) {
		return fmt.Errorf("unsupported source kind %q", s.Kind)
	}
	return nil
}

func (s Source) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Sprintf(`{"error":"%s"}`, err.Error())
	}
	return string(b)
}
