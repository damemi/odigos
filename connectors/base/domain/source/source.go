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

// ContainerOverride narrows the source's overrides to one container of a container-shaped workload.
// Containers are identified by name — the only sub-workload key that survives a redeploy, unlike a
// task ARN or a PID. Containers not listed keep the workload-level behavior.
//
// This is what makes a multi-application task definition expressible: one ECS task can pack six
// apps in six languages, and a single workload-wide RuntimeOverride cannot describe any of them
// without misdescribing the other five.
type ContainerOverride struct {
	ContainerName   string          `json:"container_name" validate:"required"`
	RuntimeOverride RuntimeOverride `json:"runtime_override,omitempty" validate:"omitempty"`
	DistroOverride  DistroOverride  `json:"distro_override,omitempty" validate:"omitempty"`
	// Disabled excludes a container from instrumentation even when its runtime is supported.
	// False (the zero value) means the workload-level decision applies — see the wire type's doc
	// for why this is an opt-out rather than an `enabled` opt-in.
	Disabled bool `json:"disabled,omitempty"`
}

// InstrumentationEnabled reports whether this container should be instrumented.
func (c ContainerOverride) InstrumentationEnabled() bool { return !c.Disabled }

// SourceConfig is optional instrumentation configuration for a source.
type SourceConfig struct {
	RuntimeOverride RuntimeOverride `json:"runtime_override,omitempty" validate:"omitempty"`
	DistroOverride  DistroOverride  `json:"distro_override,omitempty" validate:"omitempty"`
	// ContainerOverrides applies only to container-shaped kinds. The wire carries it under a
	// per-kind branch (config.ecs) so a Lambda source cannot express it at all; it is flattened
	// here because one connector handles every kind and the kind is already on the Source.
	ContainerOverrides []ContainerOverride `json:"container_overrides,omitempty" validate:"omitempty,dive"`
}

// ContainerOverrideFor returns the override for a named container, or nil when none is configured.
func (c SourceConfig) ContainerOverrideFor(name string) *ContainerOverride {
	for i := range c.ContainerOverrides {
		if c.ContainerOverrides[i].ContainerName == name {
			return &c.ContainerOverrides[i]
		}
	}
	return nil
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
