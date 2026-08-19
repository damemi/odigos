package instrumentor

import (
	"fmt"
	"strings"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
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
					"Attach it alongside your existing layers, and record the exact version you attached in " + store.EnvOdigosIaC + ".",
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
		{Name: store.EnvOdigosIaC, Value: lambdaIaCMarkerValue(), Merge: plan.MergeSet},
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
		{Name: store.EnvOdigosIaC, Value: lambdaIaCMarkerValue(), Merge: plan.MergeSet},
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
//
// Note also what became removals: the three OTEL_*_EXPORTER variables, and the ODIGOS_OTLP_* pair. "otlp" is
// already every SDK's default, so setting it restated the default three times per function; and the collector
// is now configured by one inline document rather than by two odigos-namespaced variables it had to indirect
// through. A customer who applied a previous version of this plan has both, so both are stated as removals
// rather than dropped — an unmentioned variable is one nobody is told to clean up.
func sharedLambdaEnv() []plan.EnvVar {
	return []plan.EnvVar{
		{Name: EnvServiceName, Value: plan.PlaceholderServiceName, Merge: plan.MergeSet},
		{Name: EnvCollectorConfigURI, Value: lambdaCollectorConfigTemplate(), Merge: plan.MergeSet},

		// Absent means "the SDK default", which is what an accepted signal wants. The note explains the one
		// case that needs a value: "none", for a signal the destination does not accept.
		{Name: EnvTracesExporter, Merge: plan.MergeRemove},
		{Name: EnvMetricsExporter, Merge: plan.MergeRemove},
		{Name: EnvLogsExporter, Merge: plan.MergeRemove},

		{Name: EnvCollectorEndpoint, Merge: plan.MergeRemove},
		{Name: EnvCollectorInsecure, Merge: plan.MergeRemove},
		{Name: envLegacyOTLPEndpoint, Merge: plan.MergeRemove},
		{Name: envLegacyOTLPHeaders, Merge: plan.MergeRemove},
		// Compared against unset unless the connector's debug-telemetry toggle is on, so a function carrying
		// its own value is reported as drifted. See the note.
		{Name: EnvOtelLogLevel, Merge: plan.MergeRemove},
	}
}

// lambdaCollectorConfigTemplate is the collector configuration a manual-mode customer applies, produced by
// the INJECTOR'S OWN renderer with placeholders standing in for the destination.
//
// Building it from renderCollectorConfig rather than writing the document out by hand is the whole point: the
// observer compares the live variable against a rendered config byte for byte, so a hand-maintained copy that
// drifted by one space would report every correctly-configured function as drifted, with a diff no customer
// could see. Rendering both sides through one function makes that impossible.
//
// Insecure is substituted after the fact because it is a bool on the target and cannot carry a placeholder
// through the renderer. The literal below is exactly what the renderer emits for Insecure=false.
//
// Headers are passed as nil, and that is a security property rather than an omission: this string is rendered
// into copy-paste artifacts — Terraform, CloudFormation, a shell script — that customers paste into shared
// repositories and support tickets. The destination's credential must never be baked into one. The plan's
// notes tell the reader to add their own `headers:` block instead, which is the only form in which a token
// should reach a document like this.
func lambdaCollectorConfigTemplate() string {
	cfg, err := renderCollectorConfig(domaindest.OTLPTarget{
		Endpoint: plan.PlaceholderOTLPEndpoint,
		Insecure: false,
		// All three pipelines are rendered because the plan is static and cannot know which signals a
		// particular destination accepts. The note tells the customer to drop the ones theirs does not.
		Traces: true, Metrics: true, Logs: true,
	}, nil)
	if err != nil {
		// Unreachable: the target above always has an endpoint and at least one signal. A panic here is a
		// programming error in the renderer, surfaced at package init rather than as a silent empty plan.
		panic(fmt.Sprintf("rendering the lambda collector config template: %v", err))
	}
	return strings.Replace(cfg, "tls: {insecure: false}", "tls: {insecure: "+plan.PlaceholderOTLPInsecure+"}", 1)
}

// lambdaIaCMarkerValue is the attribution marker a manual-mode customer applies — the IaC marker, NOT the
// ownership marker the connector writes when it instruments a function itself.
//
// Which of the two the plan names is the whole difference between the modes. The ownership marker's payload is
// a rollback snapshot of what the connector changed, and in manual mode there is no such thing: nothing
// converges the function, so nothing will ever roll it back, and asking a customer to hand-author a snapshot
// of a mutation that never happened produced a value the connector would then have had to honour. What a
// customer's deployment definition can honestly state is that Odigos instrumentation was applied here, from
// which artifact, carrying which layer — which is exactly this marker, and is all manual mode reads.
//
// Unlike the staged-agent platforms it records the LAYER rather than a language, because the layer ARN is what
// desiredLayerARN reads to decide which version the function is supposed to carry. That read is the reason the
// marker must name the exact ARN the customer attached: with no recorded layer the observer falls back to
// resolving one for the function's region/runtime/architecture, so a customer who pinned a different version
// would be reported as drifted for a version they chose. Recording what they actually applied makes drift mean
// "the function no longer carries what was applied" instead of "Odigos ships a newer layer".
// The payload is the MINIMUM a reader needs: the schema version and the layer. "source"/"version" are
// deliberately absent, because they are attribution an Odigos IaC module fills in about itself and a customer
// pasting a fragment by hand has nothing true to write there — and requiring them would put a literal in these
// instructions that every module then has to contradict.
func lambdaIaCMarkerValue() string {
	return fmt.Sprintf(`{"v":%d,"layer":%q}`, baseapi.IaCMarkerVersion, PlaceholderLayerARN)
}

func lambdaPlanNotes(lang, activation string) []string {
	notes := []string{
		fmt.Sprintf("Set %s exactly as shown, with the layer version ARN you actually attached. Odigos attributes the function to this variable — without it the function is reported as awaiting your deployment however completely the rest is applied — and the recorded ARN is the version it checks the function against, so pinning your own version is respected rather than reported as drift.",
			store.EnvOdigosIaC),
		fmt.Sprintf("Do NOT set %s. That variable is the connector's own record of a mutation it performed, including the snapshot it restores from on rollback, and it is written only when you hand instrumentation over to Odigos by enabling the instrumentation capability for this type. Setting it by hand claims a rollback that was never recorded.",
			store.EnvOdigosManaged),
		fmt.Sprintf("%s must match the workload's name in Odigos. A different value is reported as instrumentation drift, not as a naming preference.", EnvServiceName),
		fmt.Sprintf("%s carries the in-Lambda collector's ENTIRE configuration inline, so no other variable describes your destination. Substitute %s and %s inside it, and leave the rest byte-for-byte as shown — Odigos compares the whole value, so an extra space is reported as drift.",
			EnvCollectorConfigURI, plan.PlaceholderOTLPEndpoint, plan.PlaceholderOTLPInsecure),
		fmt.Sprintf("%s is \"true\" only if the destination is plaintext; it has to match how the destination is configured on this connector.", plan.PlaceholderOTLPInsecure),
		fmt.Sprintf("If your destination speaks OTLP/HTTP rather than gRPC, replace the whole `otlp:` exporter with `otlphttp: {endpoint: \"<your https URL>\"}` and name `otlphttp` in each pipeline's exporters. The gRPC exporter takes host:port and a TLS flag; the HTTP one takes a scheme-qualified URL and no TLS flag."),
		fmt.Sprintf("Delete the pipeline for any signal your destination does not accept, and set that signal's %s / %s / %s to \"%s\" so the agent never produces it. Leave the variable UNSET for a signal your destination does accept — \"%s\" is already the SDK default, and Odigos compares those variables against unset.",
			EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, exporterNone, exporterOTLP),
		fmt.Sprintf("%s and %s must NOT be set. Earlier versions of this instrumentation used them to point the collector at the destination; the configuration above replaces them, and a function still carrying them is reported as drifted.",
			EnvCollectorEndpoint, EnvCollectorInsecure),
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
			notes = append(notes, fmt.Sprintf("Java: the layer ships four wrapper scripts, one per handler interface, and %s above is the default for a plain RequestHandler. A function implementing RequestStreamHandler needs %s — %s BREAKS it, because the two interfaces have different signatures — and API Gateway proxy and SQS functions get richer spans from %s and %s. Odigos cannot tell which yours implements (the handler is a class name either way), so set the source's distro override to the script name you need and apply that value here instead.",
				execWrapperHandler, execWrapperStreamHandler, execWrapperHandler, execWrapperProxyHandler, execWrapperSqsHandler))
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
