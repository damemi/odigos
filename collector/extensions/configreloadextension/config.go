package configreloadextension

import (
	"go.opentelemetry.io/collector/component"
)

type Config struct {
	// File is the path to the collector's config file.
	File string
}

var _ component.Config = (*Config)(nil)
