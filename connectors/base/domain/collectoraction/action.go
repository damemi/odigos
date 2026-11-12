// Package collectoraction is the persisted, regional-Collector action vocabulary.
//
// Actions are deliberately owned by one Collector rather than by a connector as a
// whole. A connector can have Collectors in several regions and an action changes
// the telemetry path of exactly one of them.
package collectoraction

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

type Type string

const (
	TypeURLTemplatization     Type = "urlTemplatization"
	TypeProcessorAddition     Type = "processorAddition"
	TypeSpanMetrics           Type = "spanMetrics"
	TypeDbQueryTemplatization Type = "dbQueryTemplatization"
	TypeInferDbAttributes     Type = "inferDbAttributes"
	TypeExtractAttribute      Type = "extractAttribute"
	TypeDeleteAttribute       Type = "deleteAttribute"
	TypeRenameAttribute       Type = "renameAttribute"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func stringPtr(value string) *string { return &value }

// Action is the provider-neutral stored form. Config contains the JSON encoding
// of the single type-specific GraphQL config branch.
type Action struct {
	Region, Name, ActionName, Notes string
	Type                            Type
	Disabled                        bool
	Signals, Pipelines              []string
	OrderHint                       *int
	Config                          json.RawMessage
}

func (a Action) EffectiveOrderHint() int {
	if a.OrderHint != nil {
		return *a.OrderHint
	}
	switch a.Type {
	case TypeURLTemplatization:
		return 1
	default:
		return 0
	}
}

func Validate(a Action) error {
	if strings.TrimSpace(a.Region) == "" || strings.TrimSpace(a.Region) != a.Region {
		return fmt.Errorf("an action needs the region of its Collector")
	}
	if !namePattern.MatchString(a.Name) {
		return fmt.Errorf("action name %q may contain only letters, digits, '-' and '_'", a.Name)
	}
	if len(a.Signals) == 0 {
		return fmt.Errorf("action %q needs at least one signal", a.Name)
	}
	if len(a.Pipelines) != 0 {
		return fmt.Errorf("action %q cannot target internal Collector pipeline names", a.Name)
	}
	seen := map[string]bool{}
	for _, signal := range a.Signals {
		if signal != "TRACES" && signal != "METRICS" && signal != "LOGS" {
			return fmt.Errorf("action %q has unsupported signal %q", a.Name, signal)
		}
		if seen[signal] {
			return fmt.Errorf("action %q names signal %q more than once", a.Name, signal)
		}
		seen[signal] = true
	}
	if !json.Valid(a.Config) {
		return fmt.Errorf("action %q has invalid JSON configuration", a.Name)
	}
	switch a.Type {
	case TypeURLTemplatization:
		if !seen["TRACES"] || len(seen) != 1 {
			return fmt.Errorf("URL templatization action %q supports TRACES only", a.Name)
		}
		var cfg model.URLTemplatizationConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil || len(cfg.TemplatizationRulesGroups) == 0 {
			return fmt.Errorf("URL templatization action %q needs at least one rule group", a.Name)
		}
		for _, group := range cfg.TemplatizationRulesGroups {
			if group == nil || len(group.TemplatizationRules) == 0 {
				return fmt.Errorf("URL templatization action %q has an empty rule group", a.Name)
			}
			for _, rule := range group.TemplatizationRules {
				if rule == nil || strings.TrimSpace(rule.Template) == "" {
					return fmt.Errorf("URL templatization action %q has an empty template", a.Name)
				}
			}
		}
	case TypeProcessorAddition:
		var cfg model.ProcessorAdditionConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return fmt.Errorf("decode processor addition %q: %w", a.Name, err)
		}
		if _, ok := allowedProcessors[cfg.Type]; !ok {
			return fmt.Errorf("processor type %q is not available to a managed Collector", cfg.Type)
		}
		if cfg.ProcessorConfig == nil {
			return fmt.Errorf("processor addition %q needs a processorConfig object", a.Name)
		}
		if cfg.ProcessorName != nil && *cfg.ProcessorName != "" && !namePattern.MatchString(*cfg.ProcessorName) {
			return fmt.Errorf("processor instance name %q may contain only letters, digits, '-' and '_'", *cfg.ProcessorName)
		}
		if cfg.Type == "resource" && (cfg.ProcessorName == nil || *cfg.ProcessorName == "") {
			return fmt.Errorf("resource processor additions need a processorName so they do not replace the Collector's built-in resource processor")
		}
		for _, signal := range a.Signals {
			if !allowedProcessors[cfg.Type][signal] {
				return fmt.Errorf("processor type %q does not support %s", cfg.Type, signal)
			}
		}
	case TypeSpanMetrics:
		if !seen["TRACES"] || len(seen) != 1 {
			return fmt.Errorf("span metrics action %q supports TRACES only", a.Name)
		}
		var cfg model.SpanMetricsConfigInput
		if err := json.Unmarshal(a.Config, &cfg); err != nil {
			return fmt.Errorf("decode span metrics action %q: %w", a.Name, err)
		}
	case TypeDbQueryTemplatization, TypeInferDbAttributes, TypeExtractAttribute, TypeDeleteAttribute, TypeRenameAttribute:
		return validateAdditionalAction(a)
	default:
		return fmt.Errorf("action type %q is not supported by cloud connectors", a.Type)
	}
	return nil
}

// PII masking is intentionally absent: it is being refactored independently
// and must not be smuggled in through the generic processor action.
var allowedProcessors = map[string]map[string]bool{
	"attributes":             {"TRACES": true, "METRICS": true, "LOGS": true},
	"filter":                 {"TRACES": true, "METRICS": true, "LOGS": true},
	"transform":              {"TRACES": true, "METRICS": true, "LOGS": true},
	"resource":               {"TRACES": true, "METRICS": true, "LOGS": true},
	"groupbyattrs":           {"TRACES": true, "METRICS": true, "LOGS": true},
	"span":                   {"TRACES": true},
	"redaction":              {"TRACES": true, "LOGS": true},
	"probabilistic_sampler":  {"TRACES": true, "LOGS": true},
	"metricstransform":       {"METRICS": true},
	"cumulativetodelta":      {"METRICS": true},
	"deltatorate":            {"METRICS": true},
	"metricsgeneration":      {"METRICS": true},
	"odigosextractattribute": {"TRACES": true},
}

func baseTypes() []*model.ActionTypeOption {
	return []*model.ActionTypeOption{
		{
			Type: string(TypeURLTemplatization), DisplayName: "URL Templatization", Description: "Create stable routes from HTTP URLs.", AllowedSignals: []model.TelemetrySignal{model.TelemetrySignalTraces},
			Fields: []*model.ActionField{{Name: "urlTemplatizationRulesGroups", DisplayName: "Rule groups", ComponentType: "fieldGroup", ComponentProperties: `{"addLabel":"Add rule group","itemLabel":"Rule group","fields":[{"name":"templatizationRules","displayName":"Templates","componentType":"multiInput","componentProps":{"placeholder":"e.g. /users/{id}","itemWrapperKey":"template"}}]}`}},
		},
		{
			Type: string(TypeProcessorAddition), DisplayName: "Processor Addition", Description: "Add an approved OpenTelemetry processor to this Collector.", AllowedSignals: []model.TelemetrySignal{model.TelemetrySignalTraces, model.TelemetrySignalMetrics, model.TelemetrySignalLogs},
			Fields: []*model.ActionField{
				{Name: "processorType", DisplayName: "Processor type", ComponentType: "dropdown", ComponentProperties: `{"options":[{"id":"attributes","label":"Attributes"},{"id":"filter","label":"Filter"},{"id":"transform","label":"Transform"},{"id":"resource","label":"Resource"},{"id":"groupbyattrs","label":"Group by attributes"},{"id":"span","label":"Span"},{"id":"redaction","label":"Redaction"},{"id":"probabilistic_sampler","label":"Probabilistic sampler"},{"id":"metricstransform","label":"Metrics transform"},{"id":"cumulativetodelta","label":"Cumulative to delta"},{"id":"deltatorate","label":"Delta to rate"},{"id":"metricsgeneration","label":"Metrics generation"},{"id":"odigosextractattribute","label":"Extract attribute"}]}`, InitialValue: stringPtr("attributes")},
				{Name: "processorName", DisplayName: "Processor name", ComponentType: "input", ComponentProperties: `{"placeholder":"Instance name (required for resource)"}`},
				{Name: "processorConfig", DisplayName: "Processor configuration (JSON)", ComponentType: "textarea", ComponentProperties: `{"placeholder":"{}","required":true}`, InitialValue: stringPtr("{}")},
			},
		},
		{
			Type: string(TypeSpanMetrics), DisplayName: "Span Metrics", Description: "Generate metrics from traces received by this Collector.", AllowedSignals: []model.TelemetrySignal{model.TelemetrySignalTraces},
			Fields: []*model.ActionField{
				{Name: "spanMetricsDisabled", DisplayName: "Disable span metrics", ComponentType: "toggle", InitialValue: stringPtr("false")},
				{Name: "interval", DisplayName: "Interval", ComponentType: "input", ComponentProperties: `{"placeholder":"e.g. 15s"}`},
				{Name: "metricsExpiration", DisplayName: "Metrics expiration", ComponentType: "input", ComponentProperties: `{"placeholder":"e.g. 5m"}`},
				{Name: "additionalDimensions", DisplayName: "Additional dimensions", ComponentType: "multiInput", ComponentProperties: `{"placeholder":"e.g. service.namespace"}`},
				{Name: "histogramDisabled", DisplayName: "Disable latency histogram", ComponentType: "toggle", InitialValue: stringPtr("false")},
				{Name: "explicitHistogramBuckets", DisplayName: "Explicit histogram buckets", ComponentType: "multiInput", ComponentProperties: `{"placeholder":"e.g. 100ms"}`},
				{Name: "resourceMetricsKeyAttributes", DisplayName: "Resource metric key attributes", ComponentType: "multiInput", ComponentProperties: `{"placeholder":"e.g. service.name"}`},
				{Name: "includedProcessInDimensions", DisplayName: "Include process in dimensions", ComponentType: "toggle", InitialValue: stringPtr("false")},
				{Name: "excludedResourceAttributes", DisplayName: "Excluded resource attributes", ComponentType: "multiInput", ComponentProperties: `{"placeholder":"e.g. process.command_line"}`},
			},
		},
	}
}

func FromCreateInput(in model.CreateActionInput) (Action, error) {
	if in.Region == nil {
		return Action{}, fmt.Errorf("createAction: region is required for cloud connectors")
	}
	a := Action{Region: *in.Region, Name: in.Name, Type: Type(in.ActionType), Disabled: in.Disabled, OrderHint: in.OrderHint, Pipelines: append([]string(nil), in.Pipelines...)}
	if in.ActionName != nil {
		a.ActionName = *in.ActionName
	}
	if in.Notes != nil {
		a.Notes = *in.Notes
	}
	for _, s := range in.Signals {
		a.Signals = append(a.Signals, string(s))
	}
	value, err := inputConfig(a.Type, configBranches(in.URLTemplatization, in.ProcessorAddition, in.SpanMetrics, in.DbQueryTemplatization, in.ExtractAttribute, in.DeleteAttribute, in.RenameAttribute, in.PiiMasking), false)
	if err != nil {
		return Action{}, fmt.Errorf("createAction: %w", err)
	}
	config, err := json.Marshal(value)
	if err != nil {
		return Action{}, err
	}
	a.Config = config
	return a, Validate(a)
}

func ApplyUpdateInput(current Action, in model.UpdateActionInput) (Action, error) {
	if in.Region == nil || *in.Region != current.Region {
		return Action{}, fmt.Errorf("updateAction: region is required and cannot change")
	}
	next := current
	if in.ActionName != nil {
		next.ActionName = *in.ActionName
	}
	if in.Notes != nil {
		next.Notes = *in.Notes
	}
	if in.Disabled != nil {
		next.Disabled = *in.Disabled
	}
	if in.OrderHint != nil {
		next.OrderHint = in.OrderHint
	}
	if in.Signals != nil {
		next.Signals = nil
		for _, s := range in.Signals {
			next.Signals = append(next.Signals, string(s))
		}
	}
	if in.Pipelines != nil {
		next.Pipelines = append([]string(nil), in.Pipelines...)
	}
	typeChanged := in.ActionType != nil && Type(*in.ActionType) != current.Type
	if in.ActionType != nil {
		next.Type = Type(*in.ActionType)
	}
	value, err := inputConfig(next.Type, configBranches(in.URLTemplatization, in.ProcessorAddition, in.SpanMetrics, in.DbQueryTemplatization, in.ExtractAttribute, in.DeleteAttribute, in.RenameAttribute, in.PiiMasking), !typeChanged)
	if err != nil {
		return Action{}, fmt.Errorf("updateAction: %w", err)
	}
	if !typeChanged && in.SpanMetrics != nil {
		merged, err := patchSpanMetricsConfig(current.Config, in.SpanMetrics)
		if err != nil {
			return Action{}, err
		}
		value = &merged
	}
	if !typeChanged && in.DbQueryTemplatization != nil {
		var cfg model.DbQueryTemplatizationConfigInput
		if err := json.Unmarshal(current.Config, &cfg); err != nil {
			return Action{}, err
		}
		if in.DbQueryTemplatization.TemplatizeLiterals != nil {
			cfg.TemplatizeLiterals = in.DbQueryTemplatization.TemplatizeLiterals
		}
		if in.DbQueryTemplatization.RemovePostgresCastOperator != nil {
			cfg.RemovePostgresCastOperator = in.DbQueryTemplatization.RemovePostgresCastOperator
		}
		value = &cfg
	}
	if value != nil {
		config, err := json.Marshal(value)
		if err != nil {
			return Action{}, err
		}
		next.Config = config
	}
	return next, Validate(next)
}

// patchSpanMetricsConfig preserves omitted GraphQL input fields. Span Metrics
// uses a patch-shaped input (unlike URL templatization and processor addition,
// which replace their complete configuration), so replacing Config directly
// would silently erase existing dimensions or histogram settings on a partial
// API update.
func patchSpanMetricsConfig(raw json.RawMessage, patch *model.SpanMetricsConfigInput) (model.SpanMetricsConfigInput, error) {
	var current model.SpanMetricsConfigInput
	if err := json.Unmarshal(raw, &current); err != nil {
		return model.SpanMetricsConfigInput{}, fmt.Errorf("decode stored span metrics configuration: %w", err)
	}
	if patch.Disabled != nil {
		current.Disabled = patch.Disabled
	}
	if patch.Interval != nil {
		current.Interval = patch.Interval
	}
	if patch.MetricsExpiration != nil {
		current.MetricsExpiration = patch.MetricsExpiration
	}
	if patch.AdditionalDimensions != nil {
		current.AdditionalDimensions = append([]string(nil), patch.AdditionalDimensions...)
	}
	if patch.HistogramDisabled != nil {
		current.HistogramDisabled = patch.HistogramDisabled
	}
	if patch.ExplicitHistogramBuckets != nil {
		current.ExplicitHistogramBuckets = append([]string(nil), patch.ExplicitHistogramBuckets...)
	}
	if patch.ResourceMetricsKeyAttributes != nil {
		current.ResourceMetricsKeyAttributes = append([]string(nil), patch.ResourceMetricsKeyAttributes...)
	}
	if patch.IncludedProcessInDimensions != nil {
		current.IncludedProcessInDimensions = patch.IncludedProcessInDimensions
	}
	if patch.ExcludedResourceAttributes != nil {
		current.ExcludedResourceAttributes = append([]string(nil), patch.ExcludedResourceAttributes...)
	}
	return current, nil
}

func ToModel(a Action) (model.Action, error) {
	region := a.Region
	actionName, notes := a.ActionName, a.Notes
	base := func() (string, *string, *string, []model.TelemetrySignal, *int) {
		signals := make([]model.TelemetrySignal, 0, len(a.Signals))
		for _, s := range a.Signals {
			signals = append(signals, model.TelemetrySignal(s))
		}
		return region, &actionName, &notes, signals, a.OrderHint
	}
	region, actionNameP, notesP, signals, hint := base()
	switch a.Type {
	case TypeURLTemplatization:
		var config model.URLTemplatizationConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.URLTemplatizationAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeURLTemplatization, Config: &config}, nil
	case TypeProcessorAddition:
		var config model.ProcessorAdditionConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.ProcessorAdditionAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeProcessorAddition, Config: &config}, nil
	case TypeSpanMetrics:
		var config model.SpanMetricsConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.SpanMetricsAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeSpanMetrics, Config: &config}, nil
	case TypeDbQueryTemplatization:
		var config model.DbQueryTemplatizationConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.DbQueryTemplatizationAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeDbQueryTemplatization, Config: &config}, nil
	case TypeInferDbAttributes:
		return &model.InferDbAttributesAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeInferDbAttributes}, nil
	case TypeExtractAttribute:
		var config model.ExtractAttributeConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.ExtractAttributeAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeExtractAttribute, Config: &config}, nil
	case TypeDeleteAttribute:
		var config model.DeleteAttributeConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.DeleteAttributeAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeDeleteAttribute, Config: &config}, nil
	case TypeRenameAttribute:
		var config model.RenameAttributeConfig
		if err := json.Unmarshal(a.Config, &config); err != nil {
			return nil, err
		}
		return &model.RenameAttributeAction{Name: a.Name, Region: &region, ActionName: actionNameP, Notes: notesP, Disabled: a.Disabled, Signals: signals, Pipelines: a.Pipelines, OrderHint: hint, ActionType: model.ActionTypeRenameAttribute, Config: &config}, nil
	default:
		return nil, fmt.Errorf("unknown action type %q", a.Type)
	}
}

func Sorted(actions []Action) []Action {
	out := append([]Action(nil), actions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].EffectiveOrderHint() != out[j].EffectiveOrderHint() {
			return out[i].EffectiveOrderHint() < out[j].EffectiveOrderHint()
		}
		return out[i].Name < out[j].Name
	})
	return out
}
