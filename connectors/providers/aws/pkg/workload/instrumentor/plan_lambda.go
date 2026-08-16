package instrumentor

import (
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// Lambda's contribution to Wave 5: the instrumentation this connector would apply, expressed as anchored
// fragments a customer can put in their own Terraform / CloudFormation / CLI definition.
//
// Lambda is the one platform with no staged agent: there is no container to copy files from, so the agent
// arrives as a LAYER and is activated either by an exec wrapper (a normal RequestHandler function) or by the
// language's own loader variable (a function running a web server behind the AWS Lambda Web Adapter). The two
// are genuinely different instruction sets — the adapter owns AWS_LAMBDA_EXEC_WRAPPER, so setting the OTel
// wrapper on such a function breaks it — which is why there is a plan per (language, activation).
//
// Every entry mirrors instrumentationDiff, the predicate the observer judges a live function against. That is
// the whole point of building it from the injector's own constants: a customer who applies the plan must land
// exactly on Conformant, and any value here that the observer does not expect is a customer told their
// deployment drifted for following the instructions.

// PlaceholderLayerARN is the OpenTelemetry Lambda layer version ARN a customer attaches.
//
// It is a placeholder rather than a value because the ARN is per region, per runtime and per architecture, and
// — until Odigos publishes its own layers — the connector resolves it from a table or publishes one into the
// account itself. Neither is something a static artifact can name. The note that accompanies it says where to
// get the value, which is the part a customer cannot guess.
const PlaceholderLayerARN = "<OTEL_LAYER_ARN>"

// Lambda activation modes. They are not a stylistic choice: the mode is derived from the function's own
// layers (usesLambdaWebAdapter), so a customer does not pick one — they read the one that matches their
// function.
const (
	// ActivationHandler is a standard RequestHandler function: the OTel layer's exec wrapper instruments the
	// handler invocation.
	ActivationHandler = "handler"
	// ActivationWebServer is a function running a web server behind the AWS Lambda Web Adapter. The adapter
	// already owns AWS_LAMBDA_EXEC_WRAPPER, so the language agent is loaded through its own mechanism instead.
	ActivationWebServer = "web-server"
)

// DesiredLambdaPlan returns the instrumentation plan for one language and activation mode.
func DesiredLambdaPlan(lang, activation string) (plan.Plan, error) {
	if execWrapperForLanguage(lang) == "" {
		return plan.Plan{}, fmt.Errorf("no OpenTelemetry Lambda layer language %q", lang)
	}

	var env []plan.EnvVar
	switch activation {
	case ActivationHandler:
		env = desiredHandlerEnv(lang)
	case ActivationWebServer:
		var err error
		if env, err = desiredWebServerEnv(lang); err != nil {
			return plan.Plan{}, err
		}
	default:
		return plan.Plan{}, fmt.Errorf("unknown lambda activation %q (want %q or %q)", activation, ActivationHandler, ActivationWebServer)
	}

	p := plan.Plan{
		Type:       "aws.lambda",
		Language:   lang,
		Mode:       plan.ModeLayer,
		Activation: activation,
		Fragments: []plan.Fragment{
			{
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldLayers},
				Kind:   plan.KindLayer,
				Layer:  &plan.Layer{Placeholder: PlaceholderLayerARN},
			},
			{
				// Workload-scoped: a Lambda function's environment is the function's, not a container's.
				Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldEnv},
				Kind:   plan.KindEnv,
				Env:    env,
			},
		},
		Notes: lambdaPlanNotes(lang, activation),
		Placeholders: []plan.Placeholder{
			{
				Token: PlaceholderLayerARN,
				Description: "The OpenTelemetry Lambda layer version ARN for your function's region, runtime and architecture. " +
					"Attach it alongside your existing layers, and record the exact version you attached in ODIGOS_MANAGED.",
			},
			plan.ServiceNamePlaceholder,
			plan.OTLPEndpointPlaceholder,
			plan.OTLPInsecurePlaceholder,
		},
	}
	p.Sort()
	if err := p.Validate(); err != nil {
		return plan.Plan{}, err
	}
	return p, nil
}

// DesiredLambdaPlans is every (language, activation) pair the connector supports, in a stable order. Ruby has
// no verified web-server loader, so it appears once — the same asymmetry webServerLoader declares.
func DesiredLambdaPlans() ([]plan.Plan, error) {
	var out []plan.Plan
	for _, lang := range []string{"java", "nodejs", "python", "ruby"} {
		for _, act := range []string{ActivationHandler, ActivationWebServer} {
			if act == ActivationWebServer {
				if _, _, _, ok := webServerLoader(lang); !ok {
					continue
				}
			}
			p, err := DesiredLambdaPlan(lang, act)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// desiredHandlerEnv mirrors applyHandlerEnv + applyCollectorExport.
func desiredHandlerEnv(lang string) []plan.EnvVar {
	env := []plan.EnvVar{
		// First because it is the one a customer skips and the one whose absence makes everything else
		// invisible: with no marker the function is not attributable to Odigos at all, and the report says
		// "awaiting your deployment" however completely the rest is applied.
		{Name: store.EnvOdigosManaged, Value: lambdaMarkerValue(), Merge: plan.MergeSet},
		// The layer's own wrapper script, and it is LANGUAGE-SPECIFIC: /opt/otel-instrument for Python,
		// /opt/otel-handler for the rest. The wrong one makes the function fail at cold start with
		// "does not exist", so it is never a single hardcoded value.
		{Name: store.EnvExecWrapper, Value: execWrapperForLanguage(lang), Merge: plan.MergeSet},
	}
	return append(env, sharedLambdaEnv()...)
}

// desiredWebServerEnv mirrors applyWebServerEnv + applyCollectorExport: the language agent is loaded through
// its own mechanism and AWS_LAMBDA_EXEC_WRAPPER is deliberately left to the adapter.
func desiredWebServerEnv(lang string) ([]plan.EnvVar, error) {
	key, value, sep, ok := webServerLoader(lang)
	if !ok {
		return nil, fmt.Errorf("%w: no verified web-server loader for %s on Lambda", ErrUnsupportedInstrumentation, lang)
	}

	// PYTHONPATH is PREPENDED and the *_OPTIONS variables are APPENDED, which is addLoaderValue's rule and
	// not a detail that can be normalized away: the layer's OTel packages and its sitecustomize have to
	// resolve BEFORE the application's own site-packages, so appending would leave auto-instrumentation
	// loading the wrong copy — or nothing at all. The staged-agent platforms append the same variable for the
	// opposite reason (the customer's settings win there), so the direction genuinely differs per platform and
	// is carried explicitly rather than inferred.
	merge := plan.MergeAppend
	if sep == ":" {
		merge = plan.MergePrepend
	}

	env := []plan.EnvVar{
		{Name: store.EnvOdigosManaged, Value: lambdaMarkerValue(), Merge: plan.MergeSet},
		{Name: key, Value: value, Merge: merge, Separator: sep},
		// The adapter owns the exec wrapper. Setting the OTel handler wrapper on a web-server function is what
		// broke these functions before web-server mode existed, so the plan says to remove ours rather than
		// leaving it unmentioned: instrumentationDiff does not check the wrapper in this mode, but a function
		// carrying a stale Odigos wrapper from an earlier mis-instrumentation stays broken.
		{Name: store.EnvExecWrapper, Merge: plan.MergeRemove},
	}
	for k, v := range webServerExtraEnv(lang) {
		env = append(env, plan.EnvVar{Name: k, Value: v, Merge: plan.MergeSet})
	}
	return append(env, sharedLambdaEnv()...), nil
}

// sharedLambdaEnv is the export + identity configuration both activations set.
//
// Note what is absent: OTEL_EXPORTER_OTLP_ENDPOINT. The layer's auto-instrumentation exports to the in-Lambda
// collector on localhost, which forwards to the destination asynchronously — so the invocation is never
// blocked on a remote endpoint. Pointing the SDK straight at the destination is what older connector versions
// did, and it is why those two variables are removals here rather than omissions: instrumentationDiff reports
// their presence as legacyExportVars.
func sharedLambdaEnv() []plan.EnvVar {
	return []plan.EnvVar{
		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: EnvCollectorEndpoint, Value: plan.PlaceholderOTLPEndpoint, Merge: plan.MergeSet},
		{Name: EnvCollectorInsecure, Value: plan.PlaceholderOTLPInsecure, Merge: plan.MergeSet},
		{Name: EnvTracesExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvMetricsExporter, Value: exporterOTLP, Merge: plan.MergeSet},
		{Name: EnvLogsExporter, Value: exporterOTLP, Merge: plan.MergeSet},

		{Name: envLegacyOTLPEndpoint, Merge: plan.MergeRemove},
		{Name: envLegacyOTLPHeaders, Merge: plan.MergeRemove},
		// Compared against unset unless the connector's debug-telemetry toggle is on, so a function carrying
		// its own value is reported as drifted. See the note.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
}

// lambdaMarkerValue is the ownership marker a customer applies. Unlike the staged-agent platforms it records
// the LAYER rather than a language: the layer ARN is what desiredLayerARN reads to decide which version the
// function is supposed to carry.
//
// That read is the reason the marker must name the exact ARN the customer attached, and it is deliberate: with
// no recorded layer the observer falls back to resolving one for the function's region/runtime/architecture,
// so a customer who pinned a different version would be reported as drifted for a version they chose. The
// marker records what they actually applied, which makes drift mean "the function no longer carries what was
// applied" instead of "Odigos ships a newer layer".
func lambdaMarkerValue() string {
	return fmt.Sprintf(`{"v":%d,"layer":%q}`, managedManifestVersion, PlaceholderLayerARN)
}

func lambdaPlanNotes(lang, activation string) []string {
	notes := []string{
		fmt.Sprintf("Set %s exactly as shown, with the layer version ARN you actually attached. Odigos derives ownership from this variable alone — without it the function is reported as awaiting your deployment however completely the rest is applied — and the recorded ARN is the version it checks the function against, so pinning your own version is respected rather than reported as drift.",
			store.EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("%s is \"true\" only if the destination is plaintext; it has to match how the destination is configured on this connector.", EnvCollectorInsecure),
		fmt.Sprintf("Set %s / %s / %s to \"%s\" for any signal your destination does not accept, so the agent never produces it.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone),
		// This is the one value in the plan a customer genuinely cannot derive, and there is no published
		// Odigos layer to point at yet (see the artifact-distribution track), so say where it comes from.
		fmt.Sprintf("%s is per region, runtime and architecture. Use the OpenTelemetry Lambda layer ARN for your function's region and runtime, matching its architecture (x86_64 or arm64). Attach it alongside your existing layers — do not replace them.",
			PlaceholderLayerARN),
		fmt.Sprintf("Attach at most %d layers in total, the AWS limit, and do not attach two versions of the same layer — Odigos reports a duplicate as a layer-version conflict.", maxLambdaLayers),
		fmt.Sprintf("%s / %s must NOT be set. They point the SDK straight at the destination, which blocks the invocation on a remote endpoint; the layer exports to the in-Lambda collector on localhost instead, and the collector forwards asynchronously.",
			envLegacyOTLPEndpoint, envLegacyOTLPHeaders),
		fmt.Sprintf("Leave %s unset unless this connector has debug telemetry enabled, in which case set it to %q. It is compared exactly, so any other value is reported as drift.",
			EnvOtelLogLevel, otelLogLevelDebug),
	}

	switch activation {
	case ActivationHandler:
		notes = append(notes,
			fmt.Sprintf("This is for a normal Lambda RequestHandler function. %s is the layer's own wrapper script and is language-specific — %s for Python, %s for Node.js, Java and Ruby. The wrong one fails at cold start with \"does not exist\".",
				store.EnvExecWrapper, execWrapperInstrument, execWrapperHandler),
			fmt.Sprintf("If your function already sets %s to something of its own (a secrets or bootstrap wrapper, another vendor's), this plan does not apply: a function has only one exec wrapper, and taking it would disable your tooling. Odigos reports such a function as unsupported rather than instrumenting it.",
				store.EnvExecWrapper),
		)
		if lang == "java" {
			notes = append(notes, "Java: the OpenTelemetry layer can only wrap a standard class[::method] handler. A function whose handler is a script (run.sh, bootstrap) is a web-server function — use that plan instead.")
		}
	case ActivationWebServer:
		notes = append(notes,
			fmt.Sprintf("This is for a function running a web server behind the AWS Lambda Web Adapter. The adapter owns %s, so the language agent is loaded through its own variable instead — do not set the OTel wrapper here, and remove it if an earlier attempt left one.",
				store.EnvExecWrapper),
		)
		if lang == "python" {
			notes = append(notes, fmt.Sprintf("Python: %s is prepended, not appended — the layer's OpenTelemetry packages and its sitecustomize must resolve before your application's own, or auto-instrumentation loads nothing. The path also includes your application's dependency locations, because a web-server process gets no automatic /var/task on sys.path.",
				EnvPythonPath))
			notes = append(notes, fmt.Sprintf("Python: %s is pinned to %s because the Python layer bundles only the HTTP OTLP exporter; the SDK would otherwise default to gRPC and fail to load it.",
				EnvOtlpProtocol, otlpProtocolHTTP))
		}
	}
	return notes
}
