package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/util/retry"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/destinations"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// destinations_write.go holds the mutation handlers — create / update / delete
// — and their supporting helpers (secret patch, source-selector parsing,
// metrics-settings parsing, schema validation). Kept apart from destinations.go
// so the read-side stays small and the write contract is easy to audit.

func createDestinationHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		destType, err := req.RequireString("type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		destName, err := req.RequireString("destination_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		signalsRaw, err := req.RequireStringSlice("signals")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fields, err := requireStringMap(req, "fields")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)

		if err := destinations.Load(); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load destinations registry: %v", err)), nil
		}
		schema, ok := destinations.GetDestinationByType(destType)
		if !ok {
			return errorWithOptions(fmt.Sprintf("unknown destination type %q", destType), destinationTypeNames())
		}

		dataFields, secretFields, validationErrs := splitAndValidateDestinationFields(&schema, fields)
		if len(validationErrs) > 0 {
			return errorWithOptions(fmt.Sprintf("invalid fields: %v", validationErrs), nil)
		}

		signals := make([]common.ObservabilitySignal, 0, len(signalsRaw))
		for _, sig := range signalsRaw {
			signals = append(signals, common.ObservabilitySignal(sig))
		}

		ns := env.GetCurrentNamespace()

		if dryRun {
			return withGuidance(map[string]any{
				"action":             "create_destination",
				"destination_type":   destType,
				"destination_name":   destName,
				"signals":            signalsRaw,
				"plain_field_names":  keysOf(dataFields),
				"secret_field_names": keysOf(secretFields),
				"will_create_secret": len(secretFields) > 0,
			}, "Dry-run: schema validated. Apply with dry_run=false.", []NextStep{
				{When: "to actually apply", Tool: "create_destination", Args: map[string]any{"type": destType, "dry_run": false}},
				{When: "to verify credentials first", Tool: "test_destination_connection", Args: map[string]any{"type": destType}},
			})
		}

		// Idempotency: a retried create (network blip / at-least-once delivery) must
		// not produce a duplicate exporter. If a destination with the same display
		// name and type already exists, return it instead of creating another.
		if existing, err := kube.DefaultClient.OdigosClient.Destinations(ns).List(ctx, metav1.ListOptions{}); err == nil {
			for i := range existing.Items {
				e := &existing.Items[i]
				if e.Spec.DestinationName == destName && string(e.Spec.Type) == destType {
					return withGuidance(map[string]any{
						"action":           "exists",
						"destination_name": e.Name,
						"namespace":        e.Namespace,
					}, "A destination with this name and type already exists; returning it (idempotent create).", []NextStep{
						{When: "to change its settings", Tool: "update_destination", Args: map[string]any{"destination_name": e.Name}},
					})
				}
			}
		}

		var secretRef *corev1.LocalObjectReference
		if len(secretFields) > 0 {
			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos.io.dest." + destType + "-", Namespace: ns},
				StringData: secretFields,
			}
			created, err := kube.DefaultClient.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("create secret: %v", err)), nil
			}
			secretRef = &corev1.LocalObjectReference{Name: created.Name}
		}

		dest := &odigosv1.Destination{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos-destination-" + destType + "-", Namespace: ns},
			Spec: odigosv1.DestinationSpec{
				Type:            common.DestinationType(destType),
				DestinationName: destName,
				Data:            dataFields,
				SecretRef:       secretRef,
				Signals:         signals,
			},
		}
		if dp := boolArgPtr(req, "disabled"); dp != nil {
			dest.Spec.Disabled = dp
		}
		if sel, err := parseSourceSelectorArg(req, "source_selector"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		} else if sel != nil {
			dest.Spec.SourceSelector = sel
		}
		if ms, err := parseMetricsSettingsArg(req, "metrics_settings"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		} else if ms != nil {
			dest.Spec.MetricsSettings = ms
		}

		createdDest, err := kube.DefaultClient.OdigosClient.Destinations(ns).Create(ctx, dest, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create destination: %v", err)), nil
		}
		return withGuidance(map[string]any{
			"action":           "created",
			"destination_name": createdDest.Name,
			"namespace":        createdDest.Namespace,
			"secret_created":   secretRef != nil,
		}, "Destination created and the gateway pipeline will reload to start exporting.", []NextStep{
			{When: "to confirm telemetry is flowing", Tool: "get_overview_metrics"},
		})
	}
}

func updateDestinationHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("destination_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// dry_run defaults to true: the handler validates and reports the diff
		// (would_change) WITHOUT writing to the cluster. The caller re-invokes with
		// dry_run=false to actually apply — a guardrail so an agent previews a
		// mutation before committing it.
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()

		// done holds the terminal tool result (validation error, not-found, dry-run,
		// noop, or success). The Get→mutate→Update runs inside RetryOnConflict, so a
		// concurrent edit to the same destination (k8s optimistic-concurrency 409)
		// causes a re-fetch of the latest object and a re-apply instead of failing.
		var done *mcp.CallToolResult
		applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
			d, err := kube.DefaultClient.OdigosClient.Destinations(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					done = mcp.NewToolResultError(fmt.Sprintf("destination %q not found", name))
					return nil
				}
				return err
			}

			changed := map[string]any{}
			if v := stringArg(req, "new_label", ""); v != "" {
				d.Spec.DestinationName = v
				changed["destinationName"] = v
			}
			if dp := boolArgPtr(req, "disabled"); dp != nil {
				d.Spec.Disabled = dp
				changed["disabled"] = *dp
			}
			if sigs := stringSliceArg(req, "signals"); len(sigs) > 0 {
				d.Spec.Signals = d.Spec.Signals[:0]
				for _, s := range sigs {
					d.Spec.Signals = append(d.Spec.Signals, common.ObservabilitySignal(s))
				}
				changed["signals"] = sigs
			}

			// Field patches: schema-validate, split plain vs secret.
			if fields, err := requireStringMap(req, "fields"); err == nil && len(fields) > 0 {
				if err := destinations.Load(); err != nil {
					done = mcp.NewToolResultError(fmt.Sprintf("load destinations registry: %v", err))
					return nil
				}
				schema, ok := destinations.GetDestinationByType(string(d.Spec.Type))
				if !ok {
					done, _ = errorWithOptions(fmt.Sprintf("destination type %q not found", d.Spec.Type), destinationTypeNames())
					return nil
				}
				byName := map[string]destinations.Field{}
				for _, f := range schema.Spec.Fields {
					byName[f.Name] = f
				}
				if d.Spec.Data == nil {
					d.Spec.Data = map[string]string{}
				}
				updatedSecrets := map[string]string{}
				for fname, fval := range fields {
					f, ok := byName[fname]
					if !ok {
						done, _ = errorWithOptions(fmt.Sprintf("field %q not in schema", fname), nil)
						return nil
					}
					if f.Secret {
						updatedSecrets[fname] = fval
					} else {
						d.Spec.Data[fname] = fval
					}
				}
				changed["fields"] = fmt.Sprintf("%d updated", len(fields))
				if len(updatedSecrets) > 0 && !dryRun {
					if err := patchOwnedSecret(ctx, ns, d, updatedSecrets); err != nil {
						done = mcp.NewToolResultError(err.Error())
						return nil
					}
				}
			}

			if sel, err := parseSourceSelectorArg(req, "source_selector"); err != nil {
				done = mcp.NewToolResultError(err.Error())
				return nil
			} else if sel != nil {
				d.Spec.SourceSelector = sel
				changed["sourceSelector"] = "replaced"
			}
			if ms, err := parseMetricsSettingsArg(req, "metrics_settings"); err != nil {
				done = mcp.NewToolResultError(err.Error())
				return nil
			} else if ms != nil {
				d.Spec.MetricsSettings = ms
				changed["metricsSettings"] = "replaced"
			}

			if len(changed) == 0 {
				done, _ = withGuidance(map[string]any{"action": "noop"}, "Nothing to change.", nil)
				return nil
			}
			if dryRun {
				done, _ = withGuidance(map[string]any{"action": "update_destination", "destination_name": name, "would_change": changed}, "Dry-run.", nil)
				return nil
			}
			updated, err := kube.DefaultClient.OdigosClient.Destinations(ns).Update(ctx, d, metav1.UpdateOptions{})
			if err != nil {
				return err // conflict → RetryOnConflict re-fetches the latest and re-applies
			}
			done, _ = withGuidance(map[string]any{"action": "updated", "destination_name": updated.Name}, "Destination updated; gateway pipeline reloads.", nil)
			return nil
		})
		if applyErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update destination: %v", applyErr)), nil
		}
		return done, nil
	}
}

func deleteDestinationHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("destination_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()

		dest, err := kube.DefaultClient.OdigosClient.Destinations(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return withGuidance(map[string]any{"action": "noop"}, "Destination not found — nothing to delete.", nil)
			}
			return mcp.NewToolResultError(fmt.Sprintf("get destination: %v", err)), nil
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_destination", "destination_name": dest.Name},
				"Dry-run: would delete; telemetry stops flowing to this backend.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.Destinations(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete destination: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "destination_name": dest.Name}, "Deleted.", nil)
	}
}

// ---- helpers ---------------------------------------------------------------

// splitAndValidateDestinationFields validates the field map against the
// destination's schema and partitions it into plain vs secret.
func splitAndValidateDestinationFields(schema *destinations.Destination, fields map[string]string) (data, secret map[string]string, errs []string) {
	data = map[string]string{}
	secret = map[string]string{}
	byName := make(map[string]destinations.Field, len(schema.Spec.Fields))
	for _, f := range schema.Spec.Fields {
		byName[f.Name] = f
	}
	// Required fields must be present and non-empty.
	for _, f := range schema.Spec.Fields {
		required, _ := f.ComponentProps["required"].(bool)
		if !required {
			continue
		}
		if v, ok := fields[f.Name]; !ok || v == "" {
			errs = append(errs, fmt.Sprintf("required field %q is missing", f.Name))
		}
	}
	for name, val := range fields {
		if val == "" {
			continue
		}
		f, ok := byName[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("field %q is not in schema for destination type %s", name, schema.Metadata.Type))
			continue
		}
		if f.Secret {
			secret[name] = val
		} else {
			data[name] = val
		}
	}
	return data, secret, errs
}

// patchOwnedSecret merges new key/values into the Secret owned by the
// Destination. Creates the Secret if missing and links it back via SecretRef.
func patchOwnedSecret(ctx context.Context, ns string, d *odigosv1.Destination, updated map[string]string) error {
	if d.Spec.SecretRef == nil {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos.io.dest." + string(d.Spec.Type) + "-", Namespace: ns},
			StringData: updated,
		}
		created, err := kube.DefaultClient.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret: %v", err)
		}
		d.Spec.SecretRef = &corev1.LocalObjectReference{Name: created.Name}
		return nil
	}
	existing, err := kube.DefaultClient.CoreV1().Secrets(ns).Get(ctx, d.Spec.SecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get owned secret: %v", err)
	}
	if existing.StringData == nil {
		existing.StringData = map[string]string{}
	}
	for k, v := range updated {
		existing.StringData[k] = v
	}
	if _, err := kube.DefaultClient.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update owned secret: %v", err)
	}
	return nil
}

// parseSourceSelectorArg returns the typed SourceSelector or nil if not provided.
func parseSourceSelectorArg(req mcp.CallToolRequest, key string) (*odigosv1.SourceSelector, error) {
	var sel odigosv1.SourceSelector
	ok, err := decodeArg(req, key, &sel)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &sel, nil
}

// parseMetricsSettingsArg accepts a boolean map and translates to the typed
// pointer-bool struct (so unset fields stay unset rather than going to false).
func parseMetricsSettingsArg(req mcp.CallToolRequest, key string) (*odigosv1.DestinationMetricsSettings, error) {
	a := getToolRequestArgument(req)
	if a == nil {
		return nil, nil
	}
	raw, ok := a[key]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be an object of bool flags", key)
	}
	out := &odigosv1.DestinationMetricsSettings{}
	asPtr := func(name string) *bool {
		if v, ok := m[name]; ok {
			if b, ok := v.(bool); ok {
				return &b
			}
		}
		return nil
	}
	out.CollectSpanMetrics = asPtr("spanMetricsEnabled")
	out.CollectHostMetrics = asPtr("hostMetricsEnabled")
	out.CollectKubeletStats = asPtr("kubeletStatsEnabled")
	out.CollectServiceGraph = asPtr("serviceGraphEnabled")
	out.CollectOdigosOwnMetrics = asPtr("odigosOwnMetricsEnabled")
	out.CollectAgentsTelemetry = asPtr("agentsTelemetryEnabled")
	return out, nil
}
