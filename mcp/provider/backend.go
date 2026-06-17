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
	// ListSources returns enabled Odigos Sources (k8s identity + canonical SourceSpec; disabled excluded).
	ListSources(ctx context.Context) ([]Source, error)
	// ListDestinations returns configured Destinations (k8s identity + canonical DestinationSpec).
	ListDestinations(ctx context.Context) ([]Destination, error)
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
