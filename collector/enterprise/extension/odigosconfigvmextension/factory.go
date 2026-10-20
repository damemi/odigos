package odigosconfigvmextension

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"

	"github.com/odigos-io/odigos-enterprise/collector/extension/odigosconfigvmextension/internal/metadata"
)

// Type is the extension's component type, for use with component.NewID.
var Type = metadata.Type

//go:generate mdatagen metadata.yaml

func NewFactory() extension.Factory {
	return extension.NewFactory(
		metadata.Type,
		createDefaultConfig,
		create,
		metadata.ExtensionStability,
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func create(_ context.Context, set extension.Settings, cfg component.Config) (extension.Extension, error) {
	return newOdigosConfigVM(set.Logger, cfg.(*Config).ActionsDir), nil
}
