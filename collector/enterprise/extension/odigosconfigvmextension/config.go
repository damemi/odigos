package odigosconfigvmextension

import (
	"errors"

	"go.opentelemetry.io/collector/component"
)

type Config struct {
	// ActionsDir is the vm-agent actions directory (actions.d). It is watched for changes and the
	// enabled actions of every supported type in it are merged into the config served to processors.
	ActionsDir string `mapstructure:"actions_dir"`
}

var _ component.Config = (*Config)(nil)

func (c *Config) Validate() error {
	if c.ActionsDir == "" {
		return errors.New("actions_dir is required")
	}
	return nil
}
