package clustercollector

import (
	"time"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/config"
	"github.com/odigos-io/odigos/instrumentor/controllers/pipeline"
)

const gatewayProfilesPipeline = "profiles"

// defaultGroupByTraceWait matches the odigosgroupbytrace syncWait/asyncWait defaults.
const defaultGroupByTraceWait = 30 * time.Second

// interrogationGroupByTraceWait returns the longest time the gateway groupbytrace
// holds a trace: the larger of syncWait and asyncWait, each falling back to its default.
func interrogationGroupByTraceWait(t *common.TraceAggregationConfiguration) string {
	waits := []*string{nil, nil}
	if t != nil {
		waits = []*string{t.SyncWait, t.AsyncWait}
	}
	longest := time.Duration(0)
	for _, w := range waits {
		d := defaultGroupByTraceWait
		if w != nil && *w != "" {
			if parsed, err := time.ParseDuration(*w); err == nil {
				d = parsed
			}
		}
		longest = max(longest, d)
	}
	return longest.String()
}

// addInterrogationExporters enables the interrogation profiles exporter when
// interrogation is on and the gateway profiles pipeline exists. Profile stacks
// land in Redis; insights correlates them into call-trie structure in ClickHouse.
// groupByTraceWaitDuration is how long the gateway groupbytrace holds a trace (e.g. "30s");
// the profiles exporter uses 2× that as the Redis whole-trace key TTL.
func addInterrogationExporters(c *config.Config, odigosNs string, interrogation *common.InterrogationConfiguration, groupByTraceWaitDuration string) error {
	if !common.InterrogationActive(interrogation) {
		return nil
	}

	profilesPipeline, hasProfiles := c.Service.Pipelines[gatewayProfilesPipeline]
	if !hasProfiles {
		return nil
	}

	if c.Exporters == nil {
		c.Exporters = config.GenericMap{}
	}

	profilesExp := config.GenericMap{
		"redis_endpoint": k8sconsts.OdigosCacheEndpoint(odigosNs),
	}
	if groupByTraceWaitDuration != "" {
		profilesExp["group_by_trace_wait_duration"] = groupByTraceWaitDuration
	}
	c.Exporters[pipeline.InterrogationProfilesExporter] = profilesExp
	profilesPipeline.Exporters = append(profilesPipeline.Exporters, pipeline.InterrogationProfilesExporter)
	c.Service.Pipelines[gatewayProfilesPipeline] = profilesPipeline

	return nil
}
