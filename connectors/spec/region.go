package spec

import "fmt"

// BootstrapRegionConfig describes the initial API region, independently of discovery scope.
type BootstrapRegionConfig struct {
	DefaultRegion    string   `yaml:"defaultRegion"`
	SupportedRegions []string `yaml:"supportedRegions"`
}

func (c BootstrapRegionConfig) Resolve(region string) (string, error) {
	if region == "" {
		region = c.DefaultRegion
	}
	for _, supported := range c.SupportedRegions {
		if region == supported {
			return region, nil
		}
	}
	return "", fmt.Errorf("unsupported bootstrap region %q", region)
}
