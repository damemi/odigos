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

// autopilotGroupByTraceWait returns the longest time the gateway groupbytrace
// holds a trace: the larger of syncWait and asyncWait, each falling back to its default.
func autopilotGroupByTraceWait(t *common.TraceAggregationConfiguration) string {
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

// addAutopilotExporters enables the autopilot profiles exporter when
// autopilot is on and the gateway profiles pipeline exists. Profile stacks
// land in Redis; insights correlates them into call-trie structure in Postgres.
// groupByTraceWaitDuration is how long the gateway groupbytrace holds a trace (e.g. "30s");
// the profiles exporter uses 2× that as the Redis whole-trace key TTL.
func addAutopilotExporters(c *config.Config, odigosNs string, autopilot *common.AutopilotConfiguration, groupByTraceWaitDuration string) error {
	if !common.AutopilotActive(autopilot) {
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
	c.Exporters[pipeline.AutopilotProfilesExporter] = profilesExp
	profilesPipeline.Exporters = append(profilesPipeline.Exporters, pipeline.AutopilotProfilesExporter)
	c.Service.Pipelines[gatewayProfilesPipeline] = profilesPipeline

	return nil
}
