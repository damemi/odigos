// Package collectorconfig renders the complete OpenTelemetry configuration a managed Collector runs,
// and the hash that decides whether it needs pushing.
//
// Pure — no I/O, no provider SDK types, no clock. The same Input renders the same bytes, because the
// hash of those bytes is what tells the connector a push is needed: a document that varied between
// renders would push on every reconcile, and steady state is meant to be zero pushes.
//
// The Supervisor merges this document first and its own snippets after (collector/managed/supervisor.yaml):
// `$REMOTE_CONFIG`, then own-telemetry, then the OpAMP extension, then guardrails.yaml. So nothing here
// may write `service.telemetry` — the Supervisor normalizes and rewrites that whole subtree for its own
// metrics — and nothing here can remove `odigos_enterprise_auth`, which guardrails.yaml puts back last.
package collectorconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/odigos-io/odigos/connectors/base/domain/collectoraction"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos/connectors/base/domain/destination"
)

// Component names as they appear in the rendered document.
const (
	receiverOTLP          = "otlp"
	processorMemoryLimit  = "memory_limiter"
	processorResourceDet  = "resourcedetection"
	processorResource     = "resource"
	processorBatch        = "batch"
	exporterNop           = "nop"
	extensionHealthCheck  = "health_check"
	defaultResourceDetect = "env"
)

// batch tuning. Larger than the per-workload Lambda values because this is a shared gateway rather than
// one process's sidecar: it aggregates many workloads, so batches fill on size long before the timeout.
const (
	batchSendBatchSize = 8192
	batchTimeout       = "200ms"
)

// resourceDetectionTimeout bounds the platform metadata lookup. Bounded because the detector runs at
// startup and an unanswered metadata endpoint would otherwise hold the Collector short of ready, which
// on a load balancer means a target that never passes its health check.
const resourceDetectionTimeout = "2s"

// Input is everything the rendered document depends on. Deliberately a plain value: two equal Inputs
// must render identical bytes.
//
// There is no network id here because a Collector is identified by region. Its host network reaches this
// function only through values that actually change the document — the regional TLS settings below
// and the destination set — and a field that did not change the output would make the hash claim a
// difference where the Collector has none.
type Input struct {
	// Destinations are the ones this Collector owns (domain/collectordestination) — not the connector's
	// workload destinations, which say where workloads send and are configured independently. Empty is
	// legal and renders an export-less document rather than nothing: unlike workload instrumentation, a
	// Collector is not gated on having a destination, and one that is running and reachable is
	// what lets a destination be added later as a configuration push instead of a redeployment.
	Destinations []destination.Destination
	// Actions are the enabled or disabled definitions owned by this regional
	// Collector. They are rendered here, rather than in the OpAMP server, so the
	// remote and cold-start configurations are identical.
	Actions []collectoraction.Action
	// Region the Collector runs in, inserted as cloud.region on telemetry that does not carry one.
	// Empty omits the resource processor entirely.
	Region string
	// MemoryMiB is the memory its workload was allocated. memory_limiter is derived from it, which is
	// why a size change is a configuration push as well as a workload change.
	MemoryMiB int
	// TLSMode decides whether the RECEIVERS terminate TLS. Only collectoringress.TLSCollector does;
	// in every other mode something in front terminates and the receivers are plaintext.
	TLSMode collectoringress.TLSMode
	// CertificateFile / PrivateKeyFile are the mounted paths, required by TLSCollector and ignored
	// otherwise.
	CertificateFile string
	PrivateKeyFile  string
	// ResourceDetectors are the resourcedetection detectors for this platform (e.g. "ecs", "gcp",
	// "azure"). Supplied by the caller because the detector names are provider knowledge and this
	// package holds none. "env" is always included.
	ResourceDetectors []string
}

// Document is a rendered configuration and its identity.
type Document struct {
	// YAML is the document to push. Empty when rendering was refused.
	YAML string
	// Hash is the SHA-256 of YAML. Stored per Collector; a push happens only when it differs, so
	// steady state is zero pushes.
	Hash string
}

// Render builds the Collector's desired configuration.
//
// A SeverityError problem means nothing was rendered and the returned Document is empty — refusing is
// safe, because the Collector keeps running on its last good configuration and the Supervisor's
// automatic_config_rollback never has to be exercised.
func Render(in Input) (Document, []collectorstatus.Problem) {
	memory, problems := DeriveMemoryLimits(in.MemoryMiB)
	if collectorstatus.HasError(problems) {
		return Document{}, problems
	}

	receivers, receiverProblems := renderReceivers(in)
	problems = append(problems, receiverProblems...)
	if collectorstatus.HasError(problems) {
		return Document{}, problems
	}

	dests, destProblems := renderDestinations(in.Destinations)
	problems = append(problems, destProblems...)
	if collectorstatus.HasError(problems) {
		return Document{}, problems
	}

	actions, actionProblems := renderActions(in.Actions)
	problems = append(problems, actionProblems...)
	if !hasMetricsDestination(dests) && len(actions.spanMetrics) != 0 {
		for _, name := range actions.spanMetrics {
			problems = append(problems, collectorstatus.Problem{
				Code:     "CollectorActionNoMetricsDestination",
				Severity: collectorstatus.SeverityDegraded,
				Message:  fmt.Sprintf("Span Metrics action %q is waiting for an active metrics destination; it is not applied so traces continue to their existing destinations", name),
			})
		}
		actions.withoutSpanMetrics()
	}
	pipelines := renderPipelines(in, dests, actions)
	if len(pipelines) == 0 {
		// Unreachable — no rendered destination falls back to nop on every signal — and checked anyway:
		// a Collector with no pipeline accepts telemetry on its receivers and drops it silently, which
		// is indistinguishable from a healthy Collector whose destination is broken.
		return Document{}, append(problems, collectorstatus.Problem{
			Code:     "CollectorNoPipelines",
			Severity: collectorstatus.SeverityError,
			Message: "refusing to render a Collector configuration with no pipeline: it would accept telemetry " +
				"on its receivers and drop all of it while reporting healthy",
		})
	}

	doc := document{
		Receivers:  mergeComponents(receivers, dests.receivers),
		Processors: mergeComponents(mergeComponents(renderProcessors(in, memory), actions.processors), dests.processors),
		Exporters:  renderExporters(dests),
		Extensions: mergeComponents(map[string]any{
			// The load balancer's target health check. Bound wide because the check arrives from
			// the load balancer, not from inside the task.
			extensionHealthCheck: map[string]any{
				"endpoint": listenOn(collectoringress.PortHealth),
			},
		}, mergeComponents(dests.extensions, actions.extensions)),
		Connectors: mergeComponents(actions.connectors, dests.connectors),
		Service: service{
			Extensions: append([]string{extensionHealthCheck}, dests.serviceExtensions...),
			Pipelines:  pipelines,
		},
	}
	if len(actions.extensions) != 0 {
		doc.Service.Extensions = append(doc.Service.Extensions, managedActionExtension)
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return Document{}, append(problems, collectorstatus.Problem{
			Code:     "CollectorConfigUnrenderable",
			Severity: collectorstatus.SeverityError,
			Message:  fmt.Sprintf("could not render the Collector configuration: %v", err),
		})
	}

	sum := sha256.Sum256(out)
	return Document{YAML: string(out), Hash: hex.EncodeToString(sum[:])}, problems
}

// document is the rendered file. Struct order is the document's order; every nested collection is a map,
// which gopkg.in/yaml.v3 emits with sorted keys — so the bytes depend on the input and nothing else.
type document struct {
	Receivers  map[string]any `yaml:"receivers"`
	Processors map[string]any `yaml:"processors"`
	Exporters  map[string]any `yaml:"exporters"`
	Connectors map[string]any `yaml:"connectors,omitempty"`
	Extensions map[string]any `yaml:"extensions"`
	Service    service        `yaml:"service"`
}

// service deliberately has no telemetry field. The Supervisor rewrites service::telemetry for its own
// metrics after merging this document, so anything written there is either overwritten or fought over.
type service struct {
	Extensions []string            `yaml:"extensions"`
	Pipelines  map[string]pipeline `yaml:"pipelines"`
}

type pipeline struct {
	Receivers  []string `yaml:"receivers"`
	Processors []string `yaml:"processors"`
	Exporters  []string `yaml:"exporters"`
}

func listenOn(port int) string { return fmt.Sprintf("0.0.0.0:%d", port) }

// renderReceivers builds the OTLP receiver on both transports.
//
// Bound to 0.0.0.0, unlike the in-Lambda collector's localhost: this one exists to be reached from other
// workloads on the network. What may reach it is the security group, which is the authorization boundary
// in v1 — there is no receiver-side authentication to configure here.
func renderReceivers(in Input) (map[string]any, []collectorstatus.Problem) {
	grpc := map[string]any{"endpoint": listenOn(collectoringress.PortOTLPGRPC)}
	http := map[string]any{"endpoint": listenOn(collectoringress.PortOTLPHTTP)}

	if in.TLSMode == collectoringress.TLSCollector {
		if in.CertificateFile == "" || in.PrivateKeyFile == "" {
			return nil, []collectorstatus.Problem{{
				Code:     "CollectorTLSMaterialMissing",
				Severity: collectorstatus.SeverityError,
				Message: "tls.mode is \"collector\", so the Collector terminates TLS itself, but no certificate " +
					"and key paths were given. Rendering plaintext receivers instead would publish a TLS endpoint " +
					"that answers in the clear",
			}}
		}
		tls := map[string]any{"cert_file": in.CertificateFile, "key_file": in.PrivateKeyFile}
		grpc["tls"] = tls
		http["tls"] = tls
	}

	return map[string]any{
		receiverOTLP: map[string]any{
			"protocols": map[string]any{"grpc": grpc, "http": http},
		},
	}, nil
}

// renderProcessors builds the processor set. The pipeline ORDER is in processorNames; this only defines
// them.
func renderProcessors(in Input, memory MemoryLimits) map[string]any {
	processors := map[string]any{
		processorMemoryLimit: map[string]any{
			"check_interval":  CheckInterval,
			"limit_mib":       memory.LimitMiB,
			"spike_limit_mib": memory.SpikeLimitMiB,
		},
		processorResourceDet: map[string]any{
			"detectors": detectors(in.ResourceDetectors),
			"timeout":   resourceDetectionTimeout,
			// The single most important value in this document for a gateway Collector. It defaults to
			// TRUE upstream, which would stamp this Collector's own region, host and task identity over
			// the identity every workload sent — so telemetry from a hundred services would arrive
			// describing one Collector. False keeps what the workload sent and fills only what is absent.
			"override": false,
		},
		processorBatch: map[string]any{
			"send_batch_size": batchSendBatchSize,
			"timeout":         batchTimeout,
		},
	}

	if in.Region != "" {
		processors[processorResource] = map[string]any{
			"attributes": []any{
				// insert, never upsert: the same rule as resourcedetection's override above. Odigos knows
				// the region it deployed into, and uses it only where the telemetry did not say.
				map[string]any{"key": "cloud.region", "value": in.Region, "action": "insert"},
			},
		}
	}
	return processors
}

// processorNames is the pipeline order, and the order is the contract:
//
//   - memory_limiter FIRST, so an export backlog is shed at the door rather than after it has been
//     charged to the same budget everything else allocates from.
//   - resourcedetection and resource before batch, so attributes are attached to spans individually
//     rather than to whatever batch they landed in.
//   - batch LAST, immediately before the exporters, which is what it exists to feed.
func processorNames(in Input) []string {
	names := []string{processorMemoryLimit, processorResourceDet}
	if in.Region != "" {
		names = append(names, processorResource)
	}
	return append(names, processorBatch)
}

// detectors always includes env, so OTEL_RESOURCE_ATTRIBUTES on the Collector's workload is honored
// whatever the platform offers, and dedupes so a caller passing "env" does not declare it twice.
func detectors(configured []string) []string {
	out := []string{defaultResourceDetect}
	seen := map[string]bool{defaultResourceDetect: true}
	for _, d := range configured {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// renderExporters is the destinations' exporters, or nop when there are none. No destination is not an
// error: the Collector runs, its receivers accept, its health check passes and its load balancer targets
// go healthy — so adding a destination later is a configuration push rather than a redeployment. nop is
// what the Supervisor's own first-boot fallback uses for the same reason.
func renderExporters(dests renderedDestinations) map[string]any {
	if len(dests.entry) == 0 {
		return map[string]any{exporterNop: map[string]any{}}
	}
	return dests.exporters
}

// renderPipelines builds the service pipelines: every entry pipeline a destination's configurer returned,
// fed by the OTLP receiver and run through the Collector's own processors; every internal one exactly as
// its configurer wrote it; and, with no destination at all, one nop pipeline per signal.
//
// A configurer declares a pipeline only for a signal its destination takes, and that is the point: a
// signal a destination cannot take would be received, batched, and fail on export — repeatedly, at
// volume, against a backend that will never accept it.
func renderPipelines(in Input, dests renderedDestinations, actions renderedActions) map[string]pipeline {
	own := processorNames(in)
	pipelines := map[string]pipeline{}
	if len(dests.entry) == 0 {
		for _, signal := range []string{"traces", "metrics", "logs"} {
			pipelines[signal] = pipeline{
				Receivers:  []string{receiverOTLP},
				Processors: withActionProcessors(own, actions.perSignal[signal]),
				Exporters:  []string{exporterNop},
			}
		}
	} else {
		for name, p := range dests.entry {
			receivers := []string{receiverOTLP}
			for _, r := range p.Receivers {
				if r != receiverOTLP {
					receivers = append(receivers, r)
				}
			}
			signal := strings.SplitN(name, "/", 2)[0]
			if signal == "metrics" {
				receivers = append(receivers, actions.metricReceivers...)
			}
			pipelines[name] = pipeline{Receivers: receivers, Processors: withDestinationProcessors(withActionProcessors(own, actions.perSignal[signal]), p.Processors), Exporters: p.Exporters}
		}
		for name, p := range dests.internal {
			pipelines[name] = pipeline{Receivers: p.Receivers, Processors: p.Processors, Exporters: p.Exporters}
		}
	}
	if len(actions.traceExporters) != 0 {
		// Receive each span once, independently of how many trace destinations
		// consume it. Attaching this exporter to every destination duplicates
		// generated metrics, and misses traces with a metrics-only destination.
		pipelines["traces/spanmetrics-input"] = pipeline{
			Receivers:  []string{receiverOTLP},
			Processors: withActionProcessors(own, actions.perSignal["traces"]),
			Exporters:  actions.traceExporters,
		}
	}
	for name, p := range actions.internal {
		pipelines[name] = p
	}
	return pipelines
}

func hasMetricsDestination(dests renderedDestinations) bool {
	for name, p := range dests.entry {
		if strings.HasPrefix(name, "metrics/") && len(p.Exporters) != 0 {
			return true
		}
	}
	return false
}

func withActionProcessors(own, actions []string) []string {
	if len(actions) == 0 {
		return own
	}
	out := make([]string, 0, len(own)+len(actions))
	out = append(out, own[:len(own)-1]...)
	out = append(out, actions...)
	return append(out, own[len(own)-1])
}

// withDestinationProcessors places a destination's own processors after the Collector's and before
// batch, which stays last (see processorNames).
func withDestinationProcessors(own, destination []string) []string {
	if len(destination) == 0 {
		return own
	}
	out := make([]string, 0, len(own)+len(destination))
	out = append(out, own[:len(own)-1]...)
	out = append(out, destination...)
	return append(out, own[len(own)-1])
}

// mergeComponents adds destination-declared components to the Collector's own. The Collector's win a
// name clash: they are what its receivers, health check and pipeline order depend on.
func mergeComponents(own, fromDestinations map[string]any) map[string]any {
	for k, v := range fromDestinations {
		if _, taken := own[k]; !taken {
			own[k] = v
		}
	}
	return own
}
