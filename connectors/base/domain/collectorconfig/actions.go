package collectorconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoraction"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// renderedActions is the extra configuration a region's action set contributes.
// Processor additions are inserted into entry pipelines; span metrics additionally
// introduces a single trace branch and forwards generated metrics through the
// normal destination pipelines, including their action and destination processors.
type renderedActions struct {
	processors      map[string]any
	extensions      map[string]any
	collectorConfig map[string]any
	connectors      map[string]any
	perSignal       map[string][]string
	processorOrder  map[string]int
	traceExporters  []string
	metricReceivers []string
	internal        map[string]pipeline
	spanMetrics     []string
}

func renderActions(actions []collectoraction.Action) (renderedActions, []collectorstatus.Problem) {
	out := renderedActions{processors: map[string]any{}, extensions: map[string]any{}, collectorConfig: map[string]any{}, connectors: map[string]any{}, perSignal: map[string][]string{}, processorOrder: map[string]int{}, internal: map[string]pipeline{}}
	var problems []collectorstatus.Problem
	var templates []string
	var templateHint int
	templatesSet := map[string]bool{}
	spanMetricsRendered := false
	for _, action := range collectoraction.Sorted(actions) {
		if action.Disabled {
			continue
		}
		if err := collectoraction.Validate(action); err != nil {
			problems = append(problems, collectorstatus.Problem{Code: "CollectorActionUnrenderable", Severity: collectorstatus.SeverityDegraded, Message: fmt.Sprintf("action %q is left out of the Collector configuration: %v", action.Name, err)})
			continue
		}
		switch action.Type {
		case collectoraction.TypeProcessorAddition:
			var cfg model.ProcessorAdditionConfigInput
			if err := json.Unmarshal(action.Config, &cfg); err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			name := cfg.Type
			if cfg.ProcessorName != nil && *cfg.ProcessorName != "" {
				name += "/" + *cfg.ProcessorName
			}
			if _, exists := out.processors[name]; exists {
				problems = append(problems, collectorstatus.Problem{Code: "CollectorActionComponentConflict", Severity: collectorstatus.SeverityDegraded, Message: fmt.Sprintf("action %q is left out of the Collector configuration because processor %q is already configured by another action", action.Name, name)})
				continue
			}
			out.processors[name] = escapeConfig(cfg.ProcessorConfig)
			out.processorOrder[name] = action.EffectiveOrderHint()
			for _, signal := range action.Signals {
				out.perSignal[strings.ToLower(signal)] = append(out.perSignal[strings.ToLower(signal)], name)
			}
		case collectoraction.TypeExtractAttribute, collectoraction.TypeDeleteAttribute, collectoraction.TypeRenameAttribute:
			processorType, cfg, err := collectoraction.AttributeProcessorConfig(action)
			if err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			name := processorType + "/action-" + action.Name
			if _, exists := out.processors[name]; exists {
				problems = append(problems, unrenderableAction(action.Name, fmt.Errorf("processor %q is already configured", name)))
				continue
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			var config map[string]any
			if err := json.Unmarshal(raw, &config); err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			out.processors[name] = escapeConfig(config)
			out.processorOrder[name] = action.EffectiveOrderHint()
			for _, signal := range action.Signals {
				out.perSignal[strings.ToLower(signal)] = append(out.perSignal[strings.ToLower(signal)], name)
			}
		case collectoraction.TypeDbQueryTemplatization, collectoraction.TypeInferDbAttributes:
			name := "odigossqlquery/odigos-db-actions"
			if _, exists := out.processors[name]; !exists {
				out.processors[name] = map[string]any{"odigos_config_extension": managedActionExtension}
				out.processorOrder[name] = action.EffectiveOrderHint()
				out.perSignal["traces"] = append(out.perSignal["traces"], name)
			}
			if action.EffectiveOrderHint() < out.processorOrder[name] {
				out.processorOrder[name] = action.EffectiveOrderHint()
			}
			if action.Type == collectoraction.TypeInferDbAttributes {
				out.collectorConfig["inferDbAttributes"] = map[string]any{}
			} else {
				var cfg model.DbQueryTemplatizationConfigInput
				if err := json.Unmarshal(action.Config, &cfg); err != nil {
					problems = append(problems, unrenderableAction(action.Name, err))
					continue
				}
				merged, _ := out.collectorConfig["dbQueryTemplatization"].(map[string]any)
				if merged == nil {
					merged = map[string]any{}
				}
				if cfg.TemplatizeLiterals != nil && *cfg.TemplatizeLiterals {
					merged["templatizeLiterals"] = true
				}
				if cfg.RemovePostgresCastOperator != nil && *cfg.RemovePostgresCastOperator {
					merged["removePostgresCastOperator"] = true
				}
				out.collectorConfig["dbQueryTemplatization"] = merged
			}
		case collectoraction.TypeURLTemplatization:
			var cfg model.URLTemplatizationConfigInput
			if err := json.Unmarshal(action.Config, &cfg); err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			if len(templates) == 0 || action.EffectiveOrderHint() < templateHint {
				templateHint = action.EffectiveOrderHint()
			}
			for _, group := range cfg.TemplatizationRulesGroups {
				for _, rule := range group.TemplatizationRules {
					if !templatesSet[rule.Template] {
						templatesSet[rule.Template] = true
						templates = append(templates, rule.Template)
					}
				}
			}
		case collectoraction.TypeSpanMetrics:
			// vm-agent has one spanmetrics connector. Rendering another one would
			// duplicate every generated metric, so use the first enabled action and
			// make the skipped definition visible in the Collector report.
			if spanMetricsRendered {
				problems = append(problems, collectorstatus.Problem{Code: "CollectorActionSpanMetricsConflict", Severity: collectorstatus.SeverityDegraded, Message: fmt.Sprintf("action %q is left out of the Collector configuration because only one enabled Span Metrics action is supported", action.Name)})
				continue
			}
			var cfg model.SpanMetricsConfigInput
			if err := json.Unmarshal(action.Config, &cfg); err != nil {
				problems = append(problems, unrenderableAction(action.Name, err))
				continue
			}
			if cfg.Disabled != nil && *cfg.Disabled {
				continue
			}
			spanMetricsName := "spanmetrics/action-" + action.Name
			forwardName := "forward/trace/spanmetrics-" + action.Name
			metricForwardName := "forward/metrics/spanmetrics-" + action.Name
			copyScopeName := "transform/spanmetrics-copy-scope-" + action.Name
			removeDimensionsName := "resource/spanmetrics-remove-dimensions-" + action.Name
			if _, exists := out.processors[copyScopeName]; exists {
				problems = append(problems, unrenderableAction(action.Name, fmt.Errorf("processor %q is already configured", copyScopeName)))
				continue
			}
			if _, exists := out.processors[removeDimensionsName]; exists {
				problems = append(problems, unrenderableAction(action.Name, fmt.Errorf("processor %q is already configured", removeDimensionsName)))
				continue
			}
			spanMetricsRendered = true
			out.connectors[spanMetricsName] = spanMetricsConnectorConfig(cfg)
			out.connectors[forwardName] = map[string]any{}
			out.connectors[metricForwardName] = map[string]any{}
			out.processors[copyScopeName] = map[string]any{"trace_statements": []any{map[string]any{
				"context":    "span",
				"statements": []string{"set(span.attributes[\"span.instrumentation.scope.name\"], instrumentation_scope.name)"},
			}}}
			out.processors[removeDimensionsName] = map[string]any{"attributes": spanMetricsExcludedResourceAttributes(cfg)}
			out.traceExporters = append(out.traceExporters, forwardName)
			out.metricReceivers = append(out.metricReceivers, metricForwardName)
			out.internal["traces/spanmetrics-"+action.Name] = pipeline{Receivers: []string{forwardName}, Processors: []string{copyScopeName, removeDimensionsName}, Exporters: []string{spanMetricsName}}
			out.internal["metrics/spanmetrics-"+action.Name] = pipeline{Receivers: []string{spanMetricsName}, Exporters: []string{metricForwardName}}
			out.spanMetrics = append(out.spanMetrics, action.Name)
		}
	}
	if len(templates) != 0 {
		sort.Strings(templates)
		name := "odigosurltemplate/odigos-url-templatization"
		out.processors[name] = map[string]any{"odigos_config_extension": managedActionExtension}
		out.collectorConfig["urlTemplatization"] = map[string]any{"templatizationRules": templates, "default": map[string]any{}}
		out.processorOrder[name] = templateHint
		out.perSignal["traces"] = append(out.perSignal["traces"], name)
	}
	if len(out.collectorConfig) != 0 {
		out.extensions[managedActionExtension] = map[string]any{"collector_config": escapeConfig(out.collectorConfig)}
	}
	for signal := range out.perSignal {
		sort.SliceStable(out.perSignal[signal], func(i, j int) bool {
			left, right := out.perSignal[signal][i], out.perSignal[signal][j]
			if out.processorOrder[left] != out.processorOrder[right] {
				return out.processorOrder[left] < out.processorOrder[right]
			}
			return left < right
		})
	}
	return out, problems
}

const managedActionExtension = "odigos_config_connector/managed-actions"

func unrenderableAction(name string, err error) collectorstatus.Problem {
	return collectorstatus.Problem{Code: "CollectorActionUnrenderable", Severity: collectorstatus.SeverityDegraded, Message: fmt.Sprintf("action %q is left out of the Collector configuration: %v", name, err)}
}

// withoutSpanMetrics removes the complete internal route for Span Metrics.
// A spanmetrics connector with no metrics destination would consume the trace
// and send its generated metrics to nop, which is worse than leaving the
// existing trace path untouched.
func (a *renderedActions) withoutSpanMetrics() {
	for _, name := range a.spanMetrics {
		spanMetricsName := "spanmetrics/action-" + name
		forwardName := "forward/trace/spanmetrics-" + name
		delete(a.connectors, spanMetricsName)
		delete(a.connectors, forwardName)
		delete(a.connectors, "forward/metrics/spanmetrics-"+name)
		delete(a.processors, "transform/spanmetrics-copy-scope-"+name)
		delete(a.processors, "resource/spanmetrics-remove-dimensions-"+name)
		delete(a.internal, "traces/spanmetrics-"+name)
		delete(a.internal, "metrics/spanmetrics-"+name)
	}
	a.traceExporters = nil
	a.metricReceivers = nil
	a.spanMetrics = nil
}

func spanMetricsConnectorConfig(cfg model.SpanMetricsConfigInput) map[string]any {
	histogram := map[string]any{}
	if cfg.HistogramDisabled != nil && *cfg.HistogramDisabled {
		histogram["disable"] = true
	} else if len(cfg.ExplicitHistogramBuckets) != 0 {
		histogram["explicit"] = map[string]any{"buckets": cfg.ExplicitHistogramBuckets}
	}
	out := map[string]any{
		"aggregation_temporality": "AGGREGATION_TEMPORALITY_CUMULATIVE",
		"exemplars":               map[string]any{"enabled": false},
		"histogram":               histogram,
		"events":                  map[string]any{"enabled": true, "dimensions": []any{map[string]any{"name": "exception.type"}, map[string]any{"name": "exception.message"}}},
	}
	if cfg.Interval != nil {
		out["metrics_flush_interval"] = *cfg.Interval
	}
	if cfg.MetricsExpiration != nil {
		out["metrics_expiration"] = *cfg.MetricsExpiration
	}
	dimensions := []any{}
	for _, name := range []string{"http.method", "http.request.method", "http.status_code", "http.response.status_code", "http.route", "span.instrumentation.scope.name"} {
		dimensions = append(dimensions, map[string]any{"name": name})
	}
	for _, name := range cfg.AdditionalDimensions {
		dimensions = append(dimensions, map[string]any{"name": name})
	}
	out["dimensions"] = dimensions
	if len(cfg.ResourceMetricsKeyAttributes) != 0 {
		out["resource_metrics_key_attributes"] = cfg.ResourceMetricsKeyAttributes
	}
	return out
}

func spanMetricsExcludedResourceAttributes(cfg model.SpanMetricsConfigInput) []any {
	excluded := append([]string(nil), cfg.ExcludedResourceAttributes...)
	if len(excluded) == 0 {
		excluded = append(excluded, "telemetry.sdk.name", "telemetry.sdk.version")
		if cfg.IncludedProcessInDimensions == nil || !*cfg.IncludedProcessInDimensions {
			excluded = append(excluded,
				"process.command", "process.command_args", "process.executable.name", "process.executable.path",
				"process.pid", "process.vpid", "process.parent_pid",
			)
		}
	}
	sort.Strings(excluded)
	attributes := make([]any, 0, len(excluded))
	last := ""
	for _, key := range excluded {
		if key == "" || key == last {
			continue
		}
		attributes = append(attributes, map[string]any{"key": key, "action": "delete"})
		last = key
	}
	return attributes
}

// escapeConfig recurses through arbitrary processor JSON before it reaches the
// Collector. ${env:...} and ${file:...} must remain literal telemetry values.
func escapeConfig(v any) any {
	switch x := v.(type) {
	case string:
		return escapeExpansion(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			out[key] = escapeConfig(value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = escapeConfig(value)
		}
		return out
	default:
		return v
	}
}
