package collectoraction

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/stretchr/testify/require"
)

func TestProcessorAdditionKeepsItsConfiguredInstanceName(t *testing.T) {
	instance := "tenant-enrichment"
	a, err := FromCreateInput(model.CreateActionInput{
		Region:     ptr("us-east-1"),
		Name:       "add-tenant",
		ActionType: model.ActionTypeProcessorAddition,
		Signals:    []model.TelemetrySignal{model.TelemetrySignalTraces},
		ProcessorAddition: &model.ProcessorAdditionConfigInput{
			Type: "attributes", ProcessorName: &instance, ProcessorConfig: map[string]any{},
		},
	})
	require.NoError(t, err)
	var config model.ProcessorAdditionConfigInput
	require.NoError(t, json.Unmarshal(a.Config, &config))
	require.Equal(t, &instance, config.ProcessorName)
}

func TestActionRejectsConfigFromTheWrongBranch(t *testing.T) {
	_, err := FromCreateInput(model.CreateActionInput{
		Region:            ptr("us-east-1"),
		Name:              "bad-action",
		ActionType:        model.ActionTypeSpanMetrics,
		Signals:           []model.TelemetrySignal{model.TelemetrySignalTraces},
		ProcessorAddition: &model.ProcessorAdditionConfigInput{Type: "attributes", ProcessorConfig: map[string]any{}},
	})
	require.ErrorContains(t, err, "config must match actionType")
}

func TestSpanMetricsUpdatePreservesOmittedConfig(t *testing.T) {
	interval, changedInterval := "15s", "30s"
	current, err := FromCreateInput(model.CreateActionInput{
		Region:     ptr("us-east-1"),
		Name:       "latency",
		ActionType: model.ActionTypeSpanMetrics,
		Signals:    []model.TelemetrySignal{model.TelemetrySignalTraces},
		SpanMetrics: &model.SpanMetricsConfigInput{
			Interval: &interval, AdditionalDimensions: []string{"service.namespace"}, ExplicitHistogramBuckets: []string{"10ms", "100ms"},
		},
	})
	require.NoError(t, err)
	next, err := ApplyUpdateInput(current, model.UpdateActionInput{
		Name: "latency", Region: ptr("us-east-1"), SpanMetrics: &model.SpanMetricsConfigInput{Interval: &changedInterval},
	})
	require.NoError(t, err)
	var config model.SpanMetricsConfigInput
	require.NoError(t, json.Unmarshal(next.Config, &config))
	require.Equal(t, &changedInterval, config.Interval)
	require.Equal(t, []string{"service.namespace"}, config.AdditionalDimensions)
	require.Equal(t, []string{"10ms", "100ms"}, config.ExplicitHistogramBuckets)
}

func ptr(s string) *string { return &s }

func TestMetadataOnlyUpdatesPreserveAllActionConfigurations(t *testing.T) {
	configs := map[Type]string{
		TypeURLTemplatization: `{"templatizationRulesGroups":[{"templatizationRules":[{"template":"/users/{id}"}]}]}`,
		TypeProcessorAddition: `{"type":"attributes","processorConfig":{"actions":[{"key":"AAA","value":"BBB","action":"insert"}]}}`,
		TypeSpanMetrics:       `{"interval":"30s","additionalDimensions":["service.namespace"]}`,
	}
	for typ, config := range configs {
		t.Run(string(typ), func(t *testing.T) {
			current := Action{Region: "us-east-1", Name: "action", Type: typ, Signals: []string{"TRACES"}, Config: json.RawMessage(config)}
			disabled := true
			next, err := ApplyUpdateInput(current, model.UpdateActionInput{Name: current.Name, Region: &current.Region, Disabled: &disabled})
			require.NoError(t, err)
			require.True(t, next.Disabled)
			require.JSONEq(t, config, string(next.Config))
		})
	}
}

func TestResourceProcessorMustNotReplaceBuiltInResourceProcessor(t *testing.T) {
	a := Action{Region: "us-east-1", Name: "resource-action", Type: TypeProcessorAddition, Signals: []string{"TRACES"}, Config: json.RawMessage(`{"type":"resource","processorConfig":{"attributes":[]}}`)}
	require.ErrorContains(t, Validate(a), "processorName")
	a.Config = json.RawMessage(`{"type":"resource","processorName":"custom","processorConfig":{"attributes":[]}}`)
	require.NoError(t, Validate(a))
}
