package collectorconfig

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoraction"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func action(t *testing.T, name string, typ collectoraction.Type, signals []string, config any) collectoraction.Action {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	return collectoraction.Action{Region: "us-east-1", Name: name, Type: typ, Signals: signals, Config: raw}
}

func TestActionsRenderIntoOnlyTheirCollectorPipelines(t *testing.T) {
	in := input(func(in *Input) {
		in.Actions = []collectoraction.Action{
			action(t, "add-tenant", collectoraction.TypeProcessorAddition, []string{"TRACES"}, map[string]any{
				"type": "attributes", "processorName": "tenant-enrichment", "processorConfig": map[string]any{"actions": []any{map[string]any{"key": "tenant", "value": "${env:SECRET}", "action": "insert"}}},
			}),
			action(t, "routes-a", collectoraction.TypeURLTemplatization, []string{"TRACES"}, map[string]any{"templatizationRulesGroups": []any{map[string]any{"templatizationRules": []any{map[string]any{"template": "/users/{id}"}}}}}),
			action(t, "routes-b", collectoraction.TypeURLTemplatization, []string{"TRACES"}, map[string]any{"templatizationRulesGroups": []any{map[string]any{"templatizationRules": []any{map[string]any{"template": "/users/{id}"}, map[string]any{"template": "/orders/{id}"}}}}}),
			action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{"interval": "30s"}),
		}
	})
	parsed := parse(t, render(t, in).YAML)
	require.Contains(t, parsed["extensions"], "odigos_config_connector/managed-actions")
	require.NotContains(t, parsed["extensions"], "odigos_config_vm/managed-actions")
	require.Contains(t, parsed["service"].(map[string]any)["extensions"], "odigos_config_connector/managed-actions")
	processors := parsed["processors"].(map[string]any)
	require.Contains(t, processors, "attributes/tenant-enrichment")
	require.Contains(t, processors, "odigosurltemplate/odigos-url-templatization")
	attributes := processors["attributes/tenant-enrichment"].(map[string]any)
	actions := attributes["actions"].([]any)
	require.Equal(t, "$${env:SECRET}", actions[0].(map[string]any)["value"])
	require.Equal(t, managedActionExtension, processors["odigosurltemplate/odigos-url-templatization"].(map[string]any)["odigos_config_extension"])
	inline := parsed["extensions"].(map[string]any)[managedActionExtension].(map[string]any)["collector_config"].(map[string]any)
	templates := inline["urlTemplatization"].(map[string]any)["templatizationRules"].([]any)
	require.Equal(t, []any{"/orders/{id}", "/users/{id}"}, templates)
	connectors := parsed["connectors"].(map[string]any)
	require.Contains(t, connectors, "spanmetrics/action-latency")
	require.Contains(t, connectors, "forward/trace/spanmetrics-latency")
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	trace := pipelines["traces/generic-customer-backend"].(map[string]any)
	require.Equal(t, []any{"memory_limiter", "resourcedetection", "resource", "attributes/tenant-enrichment", "odigosurltemplate/odigos-url-templatization", "batch"}, trace["processors"])
	require.NotContains(t, trace["exporters"], "forward/trace/spanmetrics-latency")
	spanInput := pipelines["traces/spanmetrics-input"].(map[string]any)
	require.Equal(t, []any{"otlp"}, spanInput["receivers"])
	require.Equal(t, trace["processors"], spanInput["processors"])
	require.Equal(t, []any{"forward/trace/spanmetrics-latency"}, spanInput["exporters"])
	metric := pipelines["metrics/spanmetrics-latency"].(map[string]any)
	require.Equal(t, []any{"spanmetrics/action-latency"}, metric["receivers"])
	require.Equal(t, []any{"forward/metrics/spanmetrics-latency"}, metric["exporters"])
	metricDestination := pipelines["metrics/generic-customer-backend"].(map[string]any)
	require.Contains(t, metricDestination["receivers"], "forward/metrics/spanmetrics-latency")
	spanTrace := pipelines["traces/spanmetrics-latency"].(map[string]any)
	require.Equal(t, []any{"transform/spanmetrics-copy-scope-latency", "resource/spanmetrics-remove-dimensions-latency"}, spanTrace["processors"])
	spanMetrics := connectors["spanmetrics/action-latency"].(map[string]any)
	require.Equal(t, true, spanMetrics["events"].(map[string]any)["enabled"])
}

func TestDbAndAttributeActionsRenderVmEquivalentConfiguration(t *testing.T) {
	in := input(func(in *Input) {
		in.Actions = []collectoraction.Action{
			action(t, "sql-literals", collectoraction.TypeDbQueryTemplatization, []string{"TRACES"}, map[string]any{"templatizeLiterals": true}),
			action(t, "sql-casts", collectoraction.TypeDbQueryTemplatization, []string{"TRACES"}, map[string]any{"removePostgresCastOperator": true}),
			action(t, "infer", collectoraction.TypeInferDbAttributes, []string{"TRACES"}, map[string]any{}),
			action(t, "extract", collectoraction.TypeExtractAttribute, []string{"TRACES"}, map[string]any{"extractions": []any{map[string]any{"targetAttributeName": "tenant", "lookupKey": "tenants", "dataFormat": "resource_path"}}}),
			action(t, "delete", collectoraction.TypeDeleteAttribute, []string{"TRACES", "METRICS", "LOGS"}, map[string]any{"attributeNamesToDelete": []string{"user.email"}}),
			action(t, "rename", collectoraction.TypeRenameAttribute, []string{"TRACES"}, map[string]any{"renames": []any{map[string]any{"from": "old", "to": "new"}}}),
		}
	})
	parsed := parse(t, render(t, in).YAML)
	inline := parsed["extensions"].(map[string]any)[managedActionExtension].(map[string]any)["collector_config"].(map[string]any)
	require.Equal(t, map[string]any{"templatizeLiterals": true, "removePostgresCastOperator": true}, inline["dbQueryTemplatization"])
	require.Equal(t, map[string]any{}, inline["inferDbAttributes"])
	require.NotContains(t, inline, "piiMasking")
	processors := parsed["processors"].(map[string]any)
	extract := processors["odigosextractattribute/action-extract"].(map[string]any)["extractions"].([]any)[0].(map[string]any)
	require.Equal(t, map[string]any{"target_attribute_name": "tenant", "lookup_key": "tenants", "data_format": "resource_path"}, extract)
	deletes := processors["transform/action-delete"].(map[string]any)
	require.Len(t, deletes["trace_statements"], 4)
	require.Len(t, deletes["metric_statements"], 3)
	require.Len(t, deletes["log_statements"], 3)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	trace := pipelines["traces/generic-customer-backend"].(map[string]any)["processors"]
	require.Contains(t, trace, "odigossqlquery/odigos-db-actions")
	require.Contains(t, trace, "odigosextractattribute/action-extract")
	require.Contains(t, trace, "transform/action-rename")
	metrics := pipelines["metrics/generic-customer-backend"].(map[string]any)["processors"]
	require.Contains(t, metrics, "transform/action-delete")
	require.NotContains(t, metrics, "odigossqlquery/odigos-db-actions")
	require.NotContains(t, metrics, "odigosextractattribute/action-extract")
	assertCompiledIn(t, readManifest(t), "", render(t, in).YAML)
	for i := range in.Actions {
		in.Actions[i].Disabled = true
	}
	disabled := render(t, in).YAML
	require.NotContains(t, disabled, managedActionExtension)
	require.NotContains(t, disabled, "action-delete")
}

func TestSpanMetricsHonorsDimensionConfiguration(t *testing.T) {
	includeProcess := true
	in := input(func(in *Input) {
		in.Actions = []collectoraction.Action{action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{
			"additionalDimensions":        []string{"service.namespace"},
			"includedProcessInDimensions": includeProcess,
			"excludedResourceAttributes":  []string{"service.instance.id", "service.instance.id"},
			"explicitHistogramBuckets":    []string{"10ms", "100ms"},
		})}
	})
	parsed := parse(t, render(t, in).YAML)
	processors := parsed["processors"].(map[string]any)
	remove := processors["resource/spanmetrics-remove-dimensions-latency"].(map[string]any)["attributes"].([]any)
	require.Equal(t, []any{map[string]any{"key": "service.instance.id", "action": "delete"}}, remove)
	connector := parsed["connectors"].(map[string]any)["spanmetrics/action-latency"].(map[string]any)
	require.Equal(t, []any{map[string]any{"name": "http.method"}, map[string]any{"name": "http.request.method"}, map[string]any{"name": "http.status_code"}, map[string]any{"name": "http.response.status_code"}, map[string]any{"name": "http.route"}, map[string]any{"name": "span.instrumentation.scope.name"}, map[string]any{"name": "service.namespace"}}, connector["dimensions"])
	require.Equal(t, []any{"10ms", "100ms"}, connector["histogram"].(map[string]any)["explicit"].(map[string]any)["buckets"])
}

func TestActionProcessorsHonorOrderHints(t *testing.T) {
	later, earlier := 20, -10
	in := input(func(in *Input) {
		in.Actions = []collectoraction.Action{
			action(t, "later", collectoraction.TypeProcessorAddition, []string{"TRACES"}, map[string]any{"type": "attributes", "processorName": "later", "processorConfig": map[string]any{}}),
			action(t, "routes", collectoraction.TypeURLTemplatization, []string{"TRACES"}, map[string]any{"templatizationRulesGroups": []any{map[string]any{"templatizationRules": []any{map[string]any{"template": "/orders/{id}"}}}}}),
			action(t, "earlier", collectoraction.TypeProcessorAddition, []string{"TRACES"}, map[string]any{"type": "attributes", "processorName": "earlier", "processorConfig": map[string]any{}}),
		}
		in.Actions[0].OrderHint = &later
		in.Actions[1].OrderHint = &later
		in.Actions[2].OrderHint = &earlier
	})
	parsed := parse(t, render(t, in).YAML)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	trace := pipelines["traces/generic-customer-backend"].(map[string]any)
	require.Equal(t, []any{"memory_limiter", "resourcedetection", "resource", "attributes/earlier", "attributes/later", "odigosurltemplate/odigos-url-templatization", "batch"}, trace["processors"])
}

func TestSpanMetricsWaitsForAMetricsDestination(t *testing.T) {
	in := input(func(in *Input) {
		in.Destinations[0].Signals = []string{"TRACES"}
		in.Actions = []collectoraction.Action{action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{})}
	})
	doc, problems := Render(in)
	require.False(t, collectorstatus.HasError(problems))
	require.Contains(t, problems[0].Code, "NoMetricsDestination")
	parsed := parse(t, doc.YAML)
	_, exists := parsed["connectors"]
	require.False(t, exists)
	trace := parsed["service"].(map[string]any)["pipelines"].(map[string]any)["traces/generic-customer-backend"].(map[string]any)
	require.NotContains(t, trace["exporters"], "forward/trace/spanmetrics-latency")
}

func TestSpanMetricsExportsToEveryMetricsDestination(t *testing.T) {
	in := input(func(in *Input) {
		in.Destinations = append(in.Destinations, destination.Destination{
			Name: "second-backend", Type: "otlp", Config: map[string]any{"OTLP_GRPC_ENDPOINT": "second.vendor.example.com:4317"}, Signals: []string{"METRICS"},
		})
		in.Actions = []collectoraction.Action{action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{})}
	})
	parsed := parse(t, render(t, in).YAML)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	metric := pipelines["metrics/spanmetrics-latency"].(map[string]any)
	require.Equal(t, []any{"forward/metrics/spanmetrics-latency"}, metric["exporters"])
	for _, name := range []string{"metrics/generic-customer-backend", "metrics/generic-second-backend"} {
		require.Contains(t, pipelines[name].(map[string]any)["receivers"], "forward/metrics/spanmetrics-latency")
	}
}

func TestSpanMetricsWithOTLPHTTPDestinationHasBothForwardConnectorEnds(t *testing.T) {
	const actionName = "bf83b8a186aba19b-6998f8e-4baa383-4cbf83b8-a186aba19b86998f8ebaa383"
	in := input(func(in *Input) {
		in.Destinations = []destination.Destination{{
			Name: "otlphttp", Type: "otlphttp", Signals: []string{"TRACES", "METRICS", "LOGS"},
			Config: map[string]any{"OTLP_HTTP_ENDPOINT": "http://127.0.0.1:14318"},
		}}
		in.Actions = []collectoraction.Action{action(t, actionName, collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{
			"interval": "2s", "histogramDisabled": false, "metricsExpiration": "10m", "includedProcessInDimensions": true,
		})}
	})
	parsed := parse(t, render(t, in).YAML)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	forward := "forward/metrics/spanmetrics-" + actionName
	require.Contains(t, pipelines["metrics/spanmetrics-"+actionName].(map[string]any)["exporters"], forward)
	require.Contains(t, pipelines["metrics/otlphttp-otlphttp"].(map[string]any)["receivers"], forward)
	require.Contains(t, pipelines["traces/spanmetrics-input"].(map[string]any)["exporters"], "forward/trace/spanmetrics-"+actionName)
}

func TestSpanMetricsConsumesEachSpanOnceWithMultipleTraceDestinations(t *testing.T) {
	in := input(func(in *Input) {
		in.Destinations = append(in.Destinations, honeycombDestination("second-traces", "test-key"))
		in.Actions = []collectoraction.Action{action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{})}
	})
	parsed := parse(t, render(t, in).YAML)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	count := 0
	for _, p := range pipelines {
		for _, exporter := range p.(map[string]any)["exporters"].([]any) {
			if exporter == "forward/trace/spanmetrics-latency" {
				count++
			}
		}
	}
	require.Equal(t, 1, count, "multiple trace destinations must not multiply span metrics")
}

func TestSpanMetricsWithMetricsOnlyDestinationAndMetricActions(t *testing.T) {
	in := input(func(in *Input) {
		in.Destinations[0].Signals = []string{"METRICS"}
		in.Actions = []collectoraction.Action{
			action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{}),
			action(t, "enrich", collectoraction.TypeProcessorAddition, []string{"METRICS"}, map[string]any{"type": "attributes", "processorName": "enrich", "processorConfig": map[string]any{}}),
		}
	})
	parsed := parse(t, render(t, in).YAML)
	pipelines := parsed["service"].(map[string]any)["pipelines"].(map[string]any)
	require.Equal(t, []any{"otlp"}, pipelines["traces/spanmetrics-input"].(map[string]any)["receivers"])
	metric := pipelines["metrics/generic-customer-backend"].(map[string]any)
	require.Contains(t, metric["receivers"], "forward/metrics/spanmetrics-latency")
	require.Contains(t, metric["processors"], "attributes/enrich")
}
