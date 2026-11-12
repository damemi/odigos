package odigosinterrogationprofilesexporter

//go:generate mdatagen metadata.yaml

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/xexporter"

	"github.com/odigos-io/odigos/collector/exporters/odigosinterrogationprofilesexporter/internal/metadata"
)

// Type is the exporter's component type, for use with component.NewID.
var Type = metadata.Type

func NewFactory() exporter.Factory {
	return xexporter.NewFactory(
		metadata.Type,
		createDefaultConfig,
		xexporter.WithProfiles(createProfilesExporter, metadata.ProfilesStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		GroupByTraceWaitDuration: defaultGroupByTraceWaitDuration,
		FlushInterval:            defaultFlushInterval,
		FlushThreshold:           defaultFlushThreshold,
	}
}

func createProfilesExporter(
	_ context.Context,
	set exporter.Settings,
	cfg component.Config,
) (xexporter.Profiles, error) {
	return newExporter(set.Logger, cfg.(*Config)), nil
}
