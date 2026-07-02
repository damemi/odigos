package instrumentationrules

// +kubebuilder:object:generate=true
// +kubebuilder:deepcopy-gen=true
type NetworkMetrics struct {
	// Enabled enables network flow and TCP stats metrics for scoped workloads.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

func NetworkMetricsEnabled(c *NetworkMetrics) bool {
	return c != nil && c.Enabled != nil && *c.Enabled
}
