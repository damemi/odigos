package odigosenterpriseauthextension

import "go.opentelemetry.io/collector/component"

// Config has no user-tunable fields on purpose: the token comes from the ODIGOS_ONPREM_TOKEN
// environment variable, as it does for every other enterprise component.
type Config struct {
}

func (c *Config) Validate() error {
	return nil
}

var _ component.Config = (*Config)(nil)
