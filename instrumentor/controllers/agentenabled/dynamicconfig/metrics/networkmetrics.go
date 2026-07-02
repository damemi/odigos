package metrics

import (
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/agentsignalconfig"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
)

func CalculateRuleNetworkMetrics(irls *[]odigosv1.InstrumentationRule) *instrumentationrules.NetworkMetrics {
	if irls == nil {
		return nil
	}

	var result *instrumentationrules.NetworkMetrics
	for _, irl := range *irls {
		result = mergeNetworkMetrics(result, irl.Spec.NetworkMetrics)
	}
	return result
}

func mergeNetworkMetrics(existing, incoming *instrumentationrules.NetworkMetrics) *instrumentationrules.NetworkMetrics {
	if incoming == nil || !instrumentationrules.NetworkMetricsEnabled(incoming) {
		return existing
	}
	enabled := true
	return &instrumentationrules.NetworkMetrics{Enabled: &enabled}
}

func ApplyRuleNetworkMetrics(agentMetrics **agentsignalconfig.AgentMetricsConfig, irls *[]odigosv1.InstrumentationRule) {
	ruleNetworkMetrics := CalculateRuleNetworkMetrics(irls)
	if ruleNetworkMetrics == nil {
		return
	}

	if *agentMetrics == nil {
		*agentMetrics = &agentsignalconfig.AgentMetricsConfig{}
	}

	(*agentMetrics).NetworkMetrics = ruleNetworkMetrics.DeepCopy()
}
