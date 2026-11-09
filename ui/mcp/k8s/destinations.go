package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/destinations"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// Destinations are export backends (Datadog, Honeycomb, Jaeger, …). This file
// owns the read surface + the tool registration; mutations live in
// destinations_write.go and the test-connection helper lives in
// destinations_test_connection.go.

func registerDestinationTools(s *server.MCPServer, deps Deps) {
	// Reads
	s.AddTool(
		mcp.NewTool("list_destination_types",
			readAnno(),
			mcp.WithDescription("List every destination type Odigos supports (Datadog, Honeycomb, Jaeger, etc.) with display name, category, and supported signals. Use to discover what backends can be configured."),
		),
		listDestinationTypesHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_destination_schema",
			readAnno(),
			mcp.WithDescription("Get the configuration schema for one destination type: required/optional fields, which are secrets, and which signals are supported. Always call this before create_destination."),
			mcp.WithString("type", mcp.Required(), mcp.Description("Destination type id (e.g. 'datadog'). Use list_destination_types to discover.")),
		),
		getDestinationSchemaHandler(),
	)
	s.AddTool(
		mcp.NewTool("list_destinations",
			readAnno(),
			mcp.WithDescription("List configured Destinations (telemetry backends). Returns type, name, signals, and disabled state."),
		),
		listDestinationsHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_destination",
			readAnno(),
			mcp.WithDescription("Return one Destination's full spec: type, data fields, disabled flag, signals, source selector, per-destination metrics settings. Use to see what an existing destination is doing before updating."),
			mcp.WithString("destination_name", mcp.Required(), mcp.Description("Destination resource name (from list_destinations).")),
		),
		getDestinationHandler(),
	)
	s.AddTool(
		mcp.NewTool("list_potential_destinations",
			readAnno(),
			mcp.WithDescription("Return destinations Odigos auto-discovered in the cluster from existing annotations (e.g. an in-cluster Datadog operator). These can be pre-filled into create_destination."),
		),
		listPotentialDestinationsHandler(),
	)

	// Writes — see destinations_write.go
	s.AddTool(
		mcp.NewTool("create_destination",
			writeAnno(),
			mcp.WithDescription("Create a new telemetry destination. Call get_destination_schema first to know required and secret fields. Secret fields are stored in a Kubernetes Secret owned by the Destination."),
			mcp.WithString("type", mcp.Required(), mcp.Description("Destination type id.")),
			mcp.WithString("destination_name", mcp.Required(), mcp.Description("Human-readable name for this destination instance.")),
			mcp.WithArray("signals", mcp.Required(),
				mcp.Description("Signals to export: any subset of [\"TRACES\",\"METRICS\",\"LOGS\"]."),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"TRACES", "METRICS", "LOGS"}}),
			),
			mcp.WithObject("fields", mcp.Required(),
				mcp.Description("name -> string value, matching get_destination_schema. Both plain and secret fields go here; the server splits them."),
				mcp.AdditionalProperties(map[string]any{"type": "string"}),
			),
			mcp.WithBoolean("disabled", mcp.Description("Optional: create the destination in disabled state.")),
			mcp.WithObject("source_selector",
				mcp.Description("Optional: {namespaces:[...], dataStreams:[...]} — restrict which sources export to this destination (OR semantics). Omit for 'all'."),
				mcp.AdditionalProperties(true),
			),
			mcp.WithObject("metrics_settings",
				mcp.Description("Optional: per-destination metrics toggles — any of {spanMetricsEnabled, hostMetricsEnabled, kubeletStatsEnabled, serviceGraphEnabled, odigosOwnMetricsEnabled, agentsTelemetryEnabled} (booleans)."),
				mcp.AdditionalProperties(map[string]any{"type": "boolean"}),
			),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createDestinationHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_destination",
			writeAnno(),
			mcp.WithDescription("Update an existing destination. Pass destination_name plus the fields to change. The 'fields' object replaces matching plain/secret fields; pass 'signals' to replace the signals list; pass 'disabled' to toggle without delete; pass 'source_selector' to limit which sources send here; pass 'metrics_settings' to override per-destination metrics flags. Unmentioned fields are left intact."),
			mcp.WithString("destination_name", mcp.Required(), mcp.Description("Destination resource name to update.")),
			mcp.WithString("new_label", mcp.Description("Optional: change the human-readable destination name.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional: pause/resume export.")),
			mcp.WithArray("signals",
				mcp.Description("Optional: replace the signals list."),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"TRACES", "METRICS", "LOGS"}}),
			),
			mcp.WithObject("fields",
				mcp.Description("Optional: partial map of field name -> string value. Plain fields go to Data; secret fields go to (or update) the owned Secret."),
				mcp.AdditionalProperties(map[string]any{"type": "string"}),
			),
			mcp.WithObject("source_selector",
				mcp.Description("Optional: {namespaces:[...], dataStreams:[...]} — limits which sources can export to this destination. Pass an empty object to clear the selector."),
				mcp.AdditionalProperties(true),
			),
			mcp.WithObject("metrics_settings",
				mcp.Description("Optional: per-destination metrics toggles — any of {spanMetricsEnabled, hostMetricsEnabled, kubeletStatsEnabled, serviceGraphEnabled, odigosOwnMetricsEnabled, agentsTelemetryEnabled} (booleans)."),
				mcp.AdditionalProperties(map[string]any{"type": "boolean"}),
			),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateDestinationHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_destination",
			writeAnno(),
			mcp.WithDescription("Delete a Destination by name. Telemetry stops flowing. The owned Secret is garbage-collected."),
			mcp.WithString("destination_name", mcp.Required(), mcp.Description("Destination resource name (from list_destinations).")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteDestinationHandler(),
	)
	// NOTE: test_destination_connection is intentionally omitted here — it
	// depends on frontend/services/test_connection, which is not yet in the
	// pinned OSS frontend. Re-add it once that package is merged upstream.
}

// ---- read handlers ---------------------------------------------------------

func listDestinationTypesHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := destinations.Load(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load destinations registry: %v", err)), nil
		}
		type row struct {
			Type        string `json:"type"`
			DisplayName string `json:"displayName"`
			Category    string `json:"category"`
			Traces      bool   `json:"traces"`
			Metrics     bool   `json:"metrics"`
			Logs        bool   `json:"logs"`
		}
		all := destinations.Get()
		out := make([]row, 0, len(all))
		for _, d := range all {
			out = append(out, row{
				Type: string(d.Metadata.Type), DisplayName: d.Metadata.DisplayName, Category: d.Metadata.Category,
				Traces: d.Spec.Signals.Traces.Supported, Metrics: d.Spec.Signals.Metrics.Supported, Logs: d.Spec.Signals.Logs.Supported,
			})
		}
		return withGuidance(out, fmt.Sprintf("%d destination types available.", len(out)), []NextStep{
			{When: "to see the fields for one type", Tool: "get_destination_schema"},
		})
	}
}

func getDestinationSchemaHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		destType, err := req.RequireString("type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := destinations.Load(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load destinations registry: %v", err)), nil
		}
		dest, ok := destinations.GetDestinationByType(destType)
		if !ok {
			return errorWithOptions(fmt.Sprintf("destination type %q not found", destType), destinationTypeNames())
		}
		type fieldOut struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Required    bool   `json:"required"`
			Secret      bool   `json:"secret"`
		}
		out := struct {
			Type        string     `json:"type"`
			DisplayName string     `json:"displayName"`
			Fields      []fieldOut `json:"fields"`
			Traces      bool       `json:"supportsTraces"`
			Metrics     bool       `json:"supportsMetrics"`
			Logs        bool       `json:"supportsLogs"`
		}{
			Type: string(dest.Metadata.Type), DisplayName: dest.Metadata.DisplayName,
			Traces: dest.Spec.Signals.Traces.Supported, Metrics: dest.Spec.Signals.Metrics.Supported, Logs: dest.Spec.Signals.Logs.Supported,
		}
		for _, f := range dest.Spec.Fields {
			required, _ := f.ComponentProps["required"].(bool)
			out.Fields = append(out.Fields, fieldOut{Name: f.Name, DisplayName: f.DisplayName, Required: required, Secret: f.Secret})
		}
		return withGuidance(out, "Use these field names verbatim in the 'fields' object you pass to create_destination or test_destination_connection. Secret fields are persisted to a separate Kubernetes Secret.", []NextStep{
			{When: "to validate credentials before creating", Tool: "test_destination_connection", Args: map[string]any{"type": destType}},
			{When: "to actually create", Tool: "create_destination", Args: map[string]any{"type": destType}},
		})
	}
}

func listDestinationsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		list, err := kube.DefaultClient.OdigosClient.Destinations(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list destinations: %v", err)), nil
		}
		type row struct {
			Name            string   `json:"name"`
			Type            string   `json:"type"`
			DestinationName string   `json:"destinationName"`
			Signals         []string `json:"signals"`
			Disabled        bool     `json:"disabled"`
		}
		out := make([]row, 0, len(list.Items))
		for _, d := range list.Items {
			signals := make([]string, 0, len(d.Spec.Signals))
			for _, sig := range d.Spec.Signals {
				signals = append(signals, string(sig))
			}
			disabled := false
			if d.Spec.Disabled != nil {
				disabled = *d.Spec.Disabled
			}
			out = append(out, row{Name: d.Name, Type: string(d.Spec.Type), DestinationName: d.Spec.DestinationName, Signals: signals, Disabled: disabled})
		}
		return withGuidance(out, fmt.Sprintf("%d destinations.", len(out)), []NextStep{
			{When: "to see live throughput per destination", Tool: "get_overview_metrics"},
		})
	}
}

func getDestinationHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("destination_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ns := env.GetCurrentNamespace()
		var d odigosv1.Destination
		if err := kube.CacheClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
			if apierrors.IsNotFound(err) {
				return mcp.NewToolResultError(fmt.Sprintf("destination %q not found", name)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("get destination: %v", err)), nil
		}
		return withGuidance(d, "Full Destination spec. The Data map holds plain config; secret values are NOT returned (they live in the owned Secret).", []NextStep{
			{When: "to change any field", Tool: "update_destination", Args: map[string]any{"destination_name": name}},
			{When: "to verify the credentials are still working", Tool: "test_destination_connection", Args: map[string]any{"type": string(d.Spec.Type)}},
		})
	}
}

func listPotentialDestinationsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		potential := services.PotentialDestinations(ctx)
		return withGuidance(potential, fmt.Sprintf("Found %d auto-discoverable destinations from cluster annotations.", len(potential)),
			[]NextStep{
				{When: "to create one with its detected fields", Tool: "create_destination"},
			})
	}
}

// ---- small helpers shared by reads + writes -------------------------------

func destinationTypeNames() []string {
	all := destinations.Get()
	out := make([]string, 0, len(all))
	for _, d := range all {
		out = append(out, string(d.Metadata.Type))
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
