// Package provider is the platform-neutral SPI the shared MCP tools call: each
// host (enterprise k8s UI, VM agent) supplies a provider.Backend. Returned types
// reuse odigos's canonical v1alpha1 CRD specs, so MCP output can't drift from odigos.
package provider

import (
	"context"

	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

// Backend is the contract the shared MCP tools call (in-process; no network).
type Backend interface {
	// ListSources returns all Odigos Sources (k8s identity + canonical SourceSpec).
	// Disabled sources are included; instrumentation state is carried in
	// spec.disableInstrumentation so callers can see and filter on it.
	ListSources(ctx context.Context) ([]Source, error)
	// ListDestinations returns all configured Destinations (k8s identity + canonical
	// DestinationSpec): type, exported signals, and config.
	ListDestinations(ctx context.Context) ([]Destination, error)
	// ListActions returns all configured Actions (k8s identity + canonical ActionSpec):
	// the telemetry-processing step and whichever sub-config it populates.
	ListActions(ctx context.Context) ([]Action, error)
	// ListInstrumentationRules returns all configured instrumentation rules
	// (k8s identity + canonical InstrumentationRuleSpec).
	ListInstrumentationRules(ctx context.Context) ([]InstrumentationRule, error)
}

// Source is an Odigos Source: k8s identity + canonical v1alpha1.SourceSpec.
type Source struct {
	Namespace           string `json:"namespace,omitempty"`
	Name                string `json:"name"`
	v1alpha1.SourceSpec `json:",inline"`
}

// Destination is a telemetry backend: k8s identity + canonical v1alpha1.DestinationSpec.
type Destination struct {
	Namespace                string `json:"namespace,omitempty"`
	Name                     string `json:"name"`
	v1alpha1.DestinationSpec `json:",inline"`
}

// Action is a telemetry-processing action: k8s identity + canonical v1alpha1.ActionSpec.
type Action struct {
	Namespace           string `json:"namespace,omitempty"`
	Name                string `json:"name"`
	v1alpha1.ActionSpec `json:",inline"`
}

// InstrumentationRule is a rule: k8s identity + canonical v1alpha1.InstrumentationRuleSpec.
type InstrumentationRule struct {
	Namespace                        string `json:"namespace,omitempty"`
	Name                             string `json:"name"`
	v1alpha1.InstrumentationRuleSpec `json:",inline"`
}
