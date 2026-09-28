package destination

import (
	"github.com/odigos-io/agents-api/go/server/graph/model"
)

// DestinationFromCreateInput builds a domain Destination from GraphQL create input.
func DestinationFromCreateInput(in model.CreateDestinationInput) Destination {
	dest := Destination{
		Name:    in.Name,
		Type:    in.Type,
		Region:  stringValue(in.Region),
		Config:  copyStringAnyMap(in.Config),
		Signals: signalsFromTelemetry(in.Signals),
	}
	if dest.Config == nil {
		dest.Config = map[string]any{}
	}
	return dest
}

// ApplyUpdateInput applies GraphQL update input onto the domain Destination.
func (d *Destination) ApplyUpdateInput(in model.UpdateDestinationInput) {
	if d == nil {
		return
	}
	if in.Type != nil {
		d.Type = *in.Type
	}
	if in.Region != nil {
		d.Region = *in.Region
	}
	if in.Config != nil {
		d.Config = copyStringAnyMap(in.Config)
	}
	if in.Signals != nil {
		d.Signals = signalsFromTelemetry(in.Signals)
	}
}

// ToDestinationModel converts a domain Destination to the agents-api GraphQL model.
func (d *Destination) ToDestinationModel() *model.Destination {
	if d == nil {
		return nil
	}
	cfg := copyStringAnyMap(d.Config)
	if cfg == nil {
		cfg = map[string]any{}
	}
	return &model.Destination{
		Name:    d.Name,
		Type:    d.Type,
		Region:  stringPtrIfNotEmpty(d.Region),
		Config:  cfg,
		Signals: telemetryFromSignals(d.Signals),
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func stringPtrIfNotEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// ToDeletePayload returns the GraphQL delete payload for a deleted destination.
func ToDeletePayload(name string, deleted bool) *model.DeleteDestinationPayload {
	return &model.DeleteDestinationPayload{Name: name, Deleted: deleted}
}

func copyStringAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func signalsFromTelemetry(sigs []model.TelemetrySignal) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, string(s))
	}
	return out
}

func telemetryFromSignals(sigs []string) []model.TelemetrySignal {
	out := make([]model.TelemetrySignal, 0, len(sigs))
	for _, s := range sigs {
		switch s {
		case "TRACES":
			out = append(out, model.TelemetrySignalTraces)
		case "METRICS":
			out = append(out, model.TelemetrySignalMetrics)
		case "LOGS":
			out = append(out, model.TelemetrySignalLogs)
		case "PROFILES":
			out = append(out, model.TelemetrySignalProfiles)
		}
	}
	return out
}
