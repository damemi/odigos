package api

import (
	"context"
	"github.com/odigos-io/odigos/connectors/base/domain/source"
)

// InstrumentationDistro is a concrete, provider-supported activation recipe.
type InstrumentationDistro struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Language    string `json:"language"`
	Version     string `json:"version"`
	IsDefault   bool   `json:"isDefault"`
}

// InstrumentationProvider keeps reporting and source validation on the provider's own recipe registry.
type InstrumentationProvider interface {
	DescribeInstrumentation(context.Context, *DiscoveredWorkload)
	ValidateSourceInstrumentation(context.Context, source.Source, *DiscoveredWorkload) error
}
