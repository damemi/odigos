package odigosconfigconnectorextension

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"

	"github.com/odigos-io/odigos/collector/enterprise/extension/odigosconfigconnectorextension/internal/metadata"
	"github.com/odigos-io/odigos/common/collector"
)

var Type = metadata.Type

//go:generate mdatagen metadata.yaml

func NewFactory() extension.Factory {
	return extension.NewFactory(Type, func() component.Config { return &Config{} }, create, metadata.ExtensionStability)
}

func create(_ context.Context, _ extension.Settings, cfg component.Config) (extension.Extension, error) {
	compiled, err := cfg.(*Config).compile()
	if err != nil {
		return nil, err
	}
	return &managedExtension{config: compiled, callbacks: map[collector.WorkloadConfigCacheCallback]struct{}{}}, nil
}
