// ECS Fargate instrumentation: injects the OpenTelemetry agent into a Fargate SERVICE's task definition
// using the same non-eBPF mechanism as OSS Odigos' "k8s-init-container" mount method (and its GCP Cloud
// Run adaptation): an agent-copier sidecar (from the busybox-based odigos-fargate-agents image, which
// bundles every language's env-driven agent under /instrumentations) stages the language agent into a
// shared task volume, and the app container is given the agent-activation env vars — sourced from the
// OSS Odigos distros module — resolved against /var/odigos, plus OTLP export env pointed at either the
// global destination or the eligible same-region managed Collector (Fargate has no node-local collector).
//
// ECS specifics vs the Cloud Run flavor:
//   - Task definitions are immutable: converging means DescribeTaskDefinition → mutate →
//     RegisterTaskDefinition (a new revision) → UpdateService (a rolling deployment). A customer deploy
//     registers a fresh, unmarked revision, so the reconcile re-instruments it — the idempotency gate
//     (alreadyInstrumented, managed-state comparison, never ARN equality) guarantees zero AWS writes once
//     converged.
//   - ECS has real run-to-completion containers, so the copier is simply Essential=false and the app
//     container DependsOn it with condition SUCCESS (no startup-probe/port-holding workaround).
//   - Only ECS-rolling-update services are instrumentable (CODE_DEPLOY/EXTERNAL controllers reject
//     task-definition changes via UpdateService), only Linux (the agents bundle is Linux-only), and only
//     SERVICES — a standalone task (RunTask/EventBridge) has no rollout path and reports Unsupported.
//
// eBPF is impossible on Fargate by design (no privileged containers / host PID), which is exactly why
// this env-driven path exists: EC2-backed ECS gets the eBPF cluster agent (compute-platform domain);
// Fargate gets this per-task sidecar.
package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// ecsInspector is the READ-ONLY half of Fargate instrumentation: resolving WHICH containers of a task
// definition Odigos instruments, with which agent, exporting where — plus recording that resolution as the
// service's container inventory. It performs no AWS writes.
//
// Both ECSFargateInstrumentor and ECSFargateObserver embed it, and that is what makes §7.2 structural rather
// than aspirational. Fargate's notion of "correctly instrumented" is the most elaborate of the four platforms
// (per-container language detection with relative confidence ranking, per-language agent recipes, per-language
// export transports and regional routing), so two independent implementations of it would not merely risk drifting — they would
// start out disagreeing. The mode that mutates and the mode that only reports ask exactly one question.
type ecsInspector struct {
	store *store.Store
	cache *awsclient.Cache
	// routes resolves the effective target for every service in one pass. AWS supplies the regional
	// implementation; nil preserves the connector-wide direct target for tests and providers that have
	// no managed-Collector surface.
	routes FargateRouteResolver
	// image is the odigos-fargate-agents image the sidecar stages the language agents from.
	image string
	// debug returns the live debug-telemetry toggle: when true, instrumented services get OTEL_LOG_LEVEL=debug.
	debug func() bool
	// images reads the container image config for language detection (CNB buildpack metadata labels plus
	// the image's own entrypoint/cmd/env). Optional: nil disables the image-config detection layer (the
	// task-definition heuristics still apply).
	images ImageInspector
	// recipes lazily loads the per-language injection recipes from the OSS distros module.
	recipes recipes
}

// FargateRouteResolver resolves a complete pass rather than one service at a time so regional cutover can
// admit one bounded batch across the fleet. The returned map is keyed by cluster ARN + "/" + service name.
// A service omitted from the map is treated as a resolver failure and is never mutated.
type FargateRouteResolver interface {
	ResolveFargateRoutes(context.Context, []store.ECSService) (map[string]domaindest.Routing, error)
}

func newECSInspector(st *store.Store, cache *awsclient.Cache, image string, debug func() bool, images ImageInspector, routes ...FargateRouteResolver) *ecsInspector {
	if debug == nil {
		debug = func() bool { return false }
	}
	var routeResolver FargateRouteResolver
	if len(routes) > 0 {
		routeResolver = routes[0]
	}
	return &ecsInspector{store: st, cache: cache, routes: routeResolver, image: image, debug: debug, images: images}
}

// ECSFargateInstrumentor reconciles Fargate service instrumentation against source intent. Each cycle it
// instruments enabled fargate-task sources (register an instrumented task-definition revision + roll the
// service to it) and rolls back services that are instrumented but no longer have an enabled source. It
// is idempotent: the live service + task definition are the source of truth, so a converged service is
// left untouched.
type ECSFargateInstrumentor struct {
	*ecsInspector
}

// NewECSFargate builds the Fargate instrumentor over the workload store, shared config cache, the
// odigos-fargate-agents image, a getter for the live debug-telemetry toggle, and an optional image
// inspector used to read language metadata from the app container's image.
func NewECSFargate(st *store.Store, cache *awsclient.Cache, image string, debug func() bool, images ImageInspector, routes ...FargateRouteResolver) *ECSFargateInstrumentor {
	return &ECSFargateInstrumentor{ecsInspector: newECSInspector(st, cache, image, debug, images, routes...)}
}

func fargateServiceRef(svc store.ECSService) string { return svc.ClusterARN + "/" + svc.ServiceName }

func (i *ecsInspector) resolveFargateRoutes(ctx context.Context, services []store.ECSService) (map[string]domaindest.Routing, error) {
	if i.routes != nil {
		return i.routes.ResolveFargateRoutes(ctx, services)
	}
	routing, err := i.store.OTLPRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]domaindest.Routing, len(services))
	for _, svc := range services {
		out[fargateServiceRef(svc)] = routing
	}
	return out, nil
}

// EnsurePrerequisite leaves destination selection to per-service routing. A service can use a regional
// Collector without a global terminal, and a missing route must not skip rollback for other services.
func (i *ECSFargateInstrumentor) EnsurePrerequisite(_ context.Context) error {
	return nil
}

// enabledFargateSource is the per-service instrumentation intent resolved from an enabled fargate-task source.
type enabledFargateSource struct {
	serviceName string // reported OTel service.name
	language    string // runtime language override, if the source set one
	version     string // runtime version override, if the source set one (major.minor for php/ruby)
	// containers narrows the overrides above to named containers. A task definition can pack
	// several applications in several languages, which the workload-level override cannot describe;
	// naming the container is how a customer resolves that.
	containers []domainsrc.ContainerOverride
}

// containerOverride returns the source's override for a named container, or nil.
func (s enabledFargateSource) containerOverride(name string) *domainsrc.ContainerOverride {
	for i := range s.containers {
		if s.containers[i].ContainerName == name {
			return &s.containers[i]
		}
	}
	return nil
}

// names reports whether the source explicitly names this container. An explicit instruction beats
// detection: naming a container instruments it even on a weak signal, which is how a customer
// resolves a container Odigos would otherwise skip.
func (s enabledFargateSource) names(name string) bool {
	ov := s.containerOverride(name)
	return ov != nil && ov.InstrumentationEnabled()
}

func (i *ecsInspector) enabledSources(ctx context.Context) (map[string]enabledFargateSource, error) {
	sources, err := i.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]enabledFargateSource)
	for _, s := range sources {
		if s.Kind == store.KindECSTask && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = fargateSourceIntent(s)
		}
	}
	return enabled, nil
}

// fargateSourceIntent maps one source row to the intent the instrumentor and the observer both read. Split out
// of enabledSources so the serviceName rule below is reachable from a test without a database — it is the rule
// a whole account's conformance verdict hangs on.
func fargateSourceIntent(s basestore.SourceRecord) enabledFargateSource {
	return enabledFargateSource{
		// The source's EXPLICIT reported name only — deliberately NOT ReportedServiceName(), whose fallback is
		// the SOURCE name. A Fargate source is named "<cluster>-<service>" so that two clusters running a
		// same-named service stay distinct rows; that disambiguator is an identity concern and was never the
		// OTel service.name. Using it as the default reported every IaC-instrumented service as serviceName
		// drift the moment it was marked: the CDK construct — and the CloudFormation/Terraform snippets in
		// USAGE.md §5 — set OTEL_SERVICE_NAME to the ECS service name, which is the only thing they CAN set. A
		// task definition cannot Ref the service that will run it, so "<cluster>-<service>" is not expressible
		// there at all; demanding it made manual mode structurally impossible to satisfy.
		//
		// Empty leaves the ECS service name to the fallback both consumers already carry (instrument() and
		// ecsOwnedDiff), so automatic and manual mode converge on one value — and a customer who DID set a
		// reported name still wins over both.
		serviceName: s.ServiceName,
		language:    s.Config.RuntimeOverride.Language,
		version:     s.Config.RuntimeOverride.Version,
		containers:  s.Config.ContainerOverrides,
	}
}

// standaloneUnsupportedMsg is the terminal error recorded on an enabled standalone-task source. Prefixed
// with baseapi.ErrPrefixUnsupported so the workload report classifies it as Unsupported.
var standaloneUnsupportedMsg = fmt.Sprintf(
	"%s: standalone ECS tasks (not managed by an ECS service) cannot be instrumented — there is no UpdateService rollout path; run the workload as an ECS service to instrument it",
	baseapi.ErrPrefixUnsupported)

func (i *ECSFargateInstrumentor) Reconcile(ctx context.Context) error {
	enabled, err := i.enabledSources(ctx)
	if err != nil {
		return err
	}
	services, err := i.store.ListECSServices(ctx)
	if err != nil {
		return err
	}

	// Resolve the whole fleet together. Regional routing uses this pass boundary to admit a bounded
	// cutover batch; resolving independently here would allow every service in a region to roll at once.
	enabledServices := make([]store.ECSService, 0, len(services))
	for _, svc := range services {
		if _, ok := enabled[fargateServiceRef(svc)]; ok {
			enabledServices = append(enabledServices, svc)
		}
	}
	routes, routeErr := i.resolveFargateRoutes(ctx, enabledServices)
	if routeErr != nil {
		log.Error("resolve fargate OTLP routes failed", "error", routeErr)
	}

	// Accumulate per-service failures and return them joined. The steady-state reconcile loop only logs
	// the returned error and carries on, but the shutdown drain propagates it so a service that failed to
	// roll back is never reported as cleanly drained.
	var errs []error
	matched := make(map[string]bool, len(enabled))
	for _, svc := range services {
		extID := fargateServiceRef(svc)
		src, want := enabled[extID]
		if want {
			matched[extID] = true
			routing, ok := routes[extID]
			if routeErr != nil || !ok {
				err := routeErr
				if err == nil {
					err = fmt.Errorf("no routing decision was returned for %s", extID)
				}
				if serr := i.store.SetECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName, err.Error()); serr != nil {
					log.Warn("set fargate routing error failed", "service", svc.ServiceName, "error", serr)
				}
				errs = append(errs, fmt.Errorf("resolve route for %s: %w", svc.ServiceName, err))
				continue
			}
			note := workloadRoutingNote(routing)
			// Publish reachability/readiness/fallback information even if ECS is currently settling and the
			// mutation is deferred. Keep the deployed route unchanged until convergence is confirmed below.
			if serr := i.store.SetECSServiceRouting(ctx, svc.ClusterARN, svc.ServiceName, svc.RoutingVia, note); serr != nil {
				log.Warn("set fargate routing note failed", "service", svc.ServiceName, "error", serr)
			}
			if routing.Outcome == domaindest.RoutingError || !routing.Found {
				err := errors.New(routing.Note)
				if serr := i.store.SetECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName, err.Error()); serr != nil {
					log.Warn("set fargate instrumentation error failed", "service", svc.ServiceName, "error", serr)
				}
				errs = append(errs, fmt.Errorf("instrument %s: %w", svc.ServiceName, err))
				continue
			}
			err := i.instrument(ctx, svc, src, routing)
			if err != nil {
				log.Error("instrument fargate service failed", "cluster", svc.ClusterARN, "service", svc.ServiceName, "region", svc.Region, "error", err)
				if serr := i.store.SetECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName, err.Error()); serr != nil {
					log.Warn("set fargate instrumentation error failed", "service", svc.ServiceName, "error", serr)
				}
				errs = append(errs, fmt.Errorf("instrument %s: %w", svc.ServiceName, err))
			} else if serr := i.store.ClearECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName); serr != nil {
				log.Warn("clear fargate instrumentation error failed", "service", svc.ServiceName, "error", serr)
			}
			i.recordCutover(ctx, svc, routing, err)
			continue
		}
		// Not enabled: only services the inventory flags as instrumented can need rollback (skipping the
		// rest avoids an N+1 DescribeServices across the account every cycle).
		if !svc.Instrumented {
			continue
		}
		if err := i.rollback(ctx, svc); err != nil {
			log.Error("rollback fargate service failed", "cluster", svc.ClusterARN, "service", svc.ServiceName, "region", svc.Region, "error", err)
			if serr := i.store.SetECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName, err.Error()); serr != nil {
				log.Warn("set fargate instrumentation error failed", "service", svc.ServiceName, "error", serr)
			}
			errs = append(errs, fmt.Errorf("rollback %s: %w", svc.ServiceName, err))
		} else if serr := i.store.ClearECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName); serr != nil {
			log.Warn("clear fargate instrumentation error failed", "service", svc.ServiceName, "error", serr)
		}
	}

	// Enabled sources that matched no service and whose external id is a task ARN are standalone tasks:
	// discoverable and selectable, but not instrumentable (no service to roll). Record the terminal
	// unsupported error on the task row so the workload report explains it.
	for extID := range enabled {
		if matched[extID] || !strings.Contains(extID, ":task/") {
			continue
		}
		if err := i.store.SetECSTaskInstrumentationError(ctx, extID, standaloneUnsupportedMsg); err != nil {
			log.Warn("set standalone task unsupported error failed", "task", extID, "error", err)
		}
	}
	return errors.Join(errs...)
}

func workloadRoutingNote(routing domaindest.Routing) *baseapi.WorkloadRoutingNote {
	if routing.Target.Profiles && routing.Outcome != domaindest.RoutingError {
		note := routing.Target.CollectionSummary()
		if routing.Note != "" {
			note += " " + routing.Note
		}
		return &baseapi.WorkloadRoutingNote{Code: "collected-signals", Message: note, Transient: false}
	}

	if routing.Outcome == domaindest.RoutingError || routing.Reason == domaindest.ReasonRouted || routing.Note == "" {
		return nil
	}
	return &baseapi.WorkloadRoutingNote{Code: string(routing.Reason), Message: routing.Note, Transient: true}
}

func (i *ECSFargateInstrumentor) instrument(ctx context.Context, svc store.ECSService, src enabledFargateSource, routing domaindest.Routing) error {
	// The ECS service name is the DEFAULT reported service.name, not a last resort — see enabledSources for
	// why the source's own "<cluster>-<service>" name is not it. Same resolution as the observer's
	// ecsOwnedDiff, so what this writes is exactly what that judges.
	serviceName := src.serviceName
	if serviceName == "" {
		serviceName = svc.ServiceName
	}
	client, err := i.ecsClient(ctx, svc.Region)
	if err != nil {
		return err
	}

	// The live service is the source of truth, not the inventory row.
	live, err := describeService(ctx, client, svc.ClusterARN, svc.ServiceName)
	if err != nil {
		return err
	}
	if live == nil {
		return nil // service gone/inactive; discovery will prune it
	}
	if dc := liveDeploymentController(live); dc != ecstypes.DeploymentControllerTypeEcs {
		return fmt.Errorf("%w: the service uses the %s deployment controller, which does not allow task-definition updates via UpdateService — only ECS rolling-update services can be instrumented", ErrUnsupportedInstrumentation, dc)
	}
	// A deployment still rolling out (ours or the customer's) means the service is mid-transition:
	// registering + rolling another revision now would race it. Defer to the next reconcile cycle —
	// the ECS analog of Lambda's updateInProgress gate.
	if rolloutInProgress(live) {
		log.Debug("fargate service deployment in progress; deferring instrumentation", "cluster", svc.ClusterARN, "service", svc.ServiceName)
		return nil
	}

	tdOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: live.TaskDefinition,
		Include:        []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
	})
	if err != nil {
		return err
	}
	td := tdOut.TaskDefinition
	if os := taskDefOSFamily(td); os != "" && os != string(ecstypes.OSFamilyLinux) {
		return fmt.Errorf("%w: task definition targets OS family %s; the Odigos Fargate agents bundle is Linux-only", ErrUnsupportedInstrumentation, os)
	}
	targets, excluded, err := i.selectTargets(ctx, td, src, svc, routing.Target)
	for idx := range targets {
		targets[idx].export.headers = routing.Headers
	}
	// Record the container inventory even when nothing could be targeted: that is precisely when an
	// operator needs to see each container and why it was skipped, and returning early would leave the
	// report showing nothing.
	i.recordContainers(ctx, td, src, svc, targets, excluded)
	if err != nil {
		return err
	}
	// Persist the workload-level runtime for the flat report fields. With several languages in one task
	// there is no single answer, so the aggregate says so and the container layer carries the real
	// per-container runtimes.
	lang, ver := aggregateRuntime(targets)
	i.recordRuntime(ctx, svc, lang, ver)
	for _, t := range targets {
		// A destination whose port belongs to the other OTLP protocol still "works" here — we set a
		// matching protocol — but it will fail at runtime as an opaque connection reset inside the
		// customer's task, far from anything that names the cause. Surface it at instrument time.
		if warn := portProtocolMismatch(t.export.endpoint, t.export.protocol); warn != "" {
			log.Warn("otlp endpoint/protocol mismatch", "cluster", svc.ClusterARN, "service", svc.ServiceName,
				"container", t.name, "language", t.recipe.lang, "warning", warn)
		}
	}

	// Already converged — mark instrumented and clear the in-flight phase, skipping the register+update
	// entirely so a steady-state service costs zero AWS writes per cycle.
	if alreadyInstrumented(td, targets, i.image, serviceName, svc.Region, i.debug()) {
		if err := i.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, true); err != nil {
			log.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
		}
		i.clearECSPhase(ctx, svc)
		if err := i.store.SetECSServiceRouting(ctx, svc.ClusterARN, svc.ServiceName, string(routing.Via), fargateSignalRoutingNote(routing, targets)); err != nil {
			log.Warn("set converged fargate route failed", "service", svc.ServiceName, "error", err)
		}
		return nil
	}

	if !svc.Instrumented {
		i.setECSPhase(ctx, svc, baseapi.PhaseVerifying)
	}
	input, err := applyInstrumentation(td, targets, tdOut.Tags, i.image, serviceName, svc.Region, i.debug())
	if err != nil {
		return err
	}

	i.setECSPhase(ctx, svc, baseapi.PhaseInstrumenting)
	log.Info("instrumenting fargate service", "cluster", svc.ClusterARN, "service", svc.ServiceName, "region", svc.Region,
		"containers", targetSummary(targets))
	reg, err := client.RegisterTaskDefinition(ctx, input)
	if err != nil {
		return fmt.Errorf("register instrumented task definition: %w", withPassRoleHint(err))
	}
	newARN := aws.ToString(reg.TaskDefinition.TaskDefinitionArn)

	// Cheap lost-update guard: ECS has no revision precondition on UpdateService, so re-read the service
	// and defer if a customer deploy landed between our describe and now — the next cycle re-instruments
	// on top of the customer's new revision (the just-registered revision simply goes unused).
	cur, err := describeService(ctx, client, svc.ClusterARN, svc.ServiceName)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	if aws.ToString(cur.TaskDefinition) != aws.ToString(live.TaskDefinition) {
		log.Debug("fargate service task definition changed mid-flight; deferring to next reconcile", "cluster", svc.ClusterARN, "service", svc.ServiceName)
		return nil
	}

	if _, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(svc.ClusterARN),
		Service:        aws.String(svc.ServiceName),
		TaskDefinition: aws.String(newARN),
	}); err != nil {
		return fmt.Errorf("roll service to instrumented task definition: %w", withPassRoleHint(err))
	}
	// Write the live flag through immediately so a reconcile before the next discovery cycle sees the
	// service as instrumented. The rollout settles asynchronously; the phase stays Instrumenting until a
	// later converged reconcile clears it.
	if err := i.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, true); err != nil {
		log.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
	}
	if err := i.store.SetECSServiceOdigosTaskDefinition(ctx, svc.ClusterARN, svc.ServiceName, newARN); err != nil {
		log.Warn("set fargate odigos task definition failed", "service", svc.ServiceName, "error", err)
	}
	if err := i.store.SetECSServiceRouting(ctx, svc.ClusterARN, svc.ServiceName, string(routing.Via), fargateSignalRoutingNote(routing, targets)); err != nil {
		log.Warn("set fargate route failed", "service", svc.ServiceName, "error", err)
	}
	return nil
}

func (i *ECSFargateInstrumentor) rollback(ctx context.Context, svc store.ECSService) error {
	client, err := i.ecsClient(ctx, svc.Region)
	if err != nil {
		return err
	}
	live, err := describeService(ctx, client, svc.ClusterARN, svc.ServiceName)
	if err != nil {
		return err
	}
	if live == nil {
		// Service gone — nothing to roll back; correct the stale flag.
		if err := i.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, false); err != nil {
			log.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
		}
		i.clearECSPhase(ctx, svc)
		i.clearECSRouting(ctx, svc)
		return nil
	}
	tdOut, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: live.TaskDefinition,
		Include:        []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
	})
	if err != nil {
		return err
	}
	// Ownership gate: only roll back instrumentation we can attribute to ourselves (the ownership
	// marker). A service the customer instrumented with their own OpenTelemetry setup — or whose current
	// revision came from a customer deploy that dropped our changes — is left completely untouched.
	input, owned := rollbackInstrumentation(tdOut.TaskDefinition, tdOut.Tags)
	if !owned {
		if err := i.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, false); err != nil {
			log.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
		}
		i.clearECSPhase(ctx, svc)
		i.clearECSRouting(ctx, svc)
		return nil
	}
	if rolloutInProgress(live) {
		log.Debug("fargate service deployment in progress; deferring rollback", "cluster", svc.ClusterARN, "service", svc.ServiceName)
		return nil
	}

	log.Info("rolling back fargate instrumentation", "cluster", svc.ClusterARN, "service", svc.ServiceName, "region", svc.Region)
	reg, err := client.RegisterTaskDefinition(ctx, input)
	if err != nil {
		return fmt.Errorf("register clean task definition: %w", withPassRoleHint(err))
	}
	if _, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(svc.ClusterARN),
		Service:        aws.String(svc.ServiceName),
		TaskDefinition: reg.TaskDefinition.TaskDefinitionArn,
	}); err != nil {
		return fmt.Errorf("roll service to clean task definition: %w", withPassRoleHint(err))
	}
	if err := i.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, false); err != nil {
		log.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
	}
	i.clearECSPhase(ctx, svc)
	i.clearECSRouting(ctx, svc)
	return nil
}

func (i *ecsInspector) clearECSRouting(ctx context.Context, svc store.ECSService) {
	if err := i.store.SetECSServiceRouting(ctx, svc.ClusterARN, svc.ServiceName, "", nil); err != nil {
		log.Warn("clear fargate routing state failed", "service", svc.ServiceName, "error", err)
	}
}

// selectTargets resolves which containers of a task definition to instrument, and how.
//
// Odigos instruments EVERY container it can, not one chosen application: a single task definition
// legitimately packs several independent apps in several languages, and picking one of six correct
// answers was never possible. Each survivor carries its own recipe and export wiring.
//
// Two gates keep that from over-reaching:
//
//   - Detection confidence. A container is targeted on a STRONG signal only (an override, a stored
//     decision, buildpack metadata, a runtime version env var, an interpreter entrypoint). A weak
//     substring match is not enough — an nginx container whose inline fastcgi config names index.php
//     must not be instrumented as PHP just because it mentions one. Naming a container in a source
//     override makes it a target regardless, since an explicit instruction beats detection.
//   - Container overrides. A container the source disabled is never targeted.
//
// Selection deliberately does NOT use port mappings: in proxy+app pairs the port-mapped container is
// the proxy, which has no agent.
//
// Returns the targets plus the reason each non-target container was skipped, so the report can
// explain every container rather than only the instrumented ones. An error means NOTHING could be
// instrumented — a single container failing (an unresolvable php version, an unsupported runtime)
// only excludes that container.
func (i *ecsInspector) selectTargets(ctx context.Context, td *ecstypes.TaskDefinition, src enabledFargateSource, svc store.ECSService, dest domaindest.OTLPTarget) ([]containerTarget, map[string]string, error) {
	candidates, err := candidateContainerIndexes(td)
	if err != nil {
		return nil, nil, err
	}
	excluded := make(map[string]string, len(td.ContainerDefinitions))
	var detected []string

	// Pass 1: resolve every candidate's runtime on its own signals.
	type candidate struct {
		idx   int
		name  string
		rt    resolvedRuntime
		named bool // the source names this container explicitly
	}
	var coverable []candidate

	for _, idx := range candidates {
		c := &td.ContainerDefinitions[idx]
		name := containerName(c)

		if ovDisabled(src, name) {
			excluded[name] = "disabled by a container override on the source"
			continue
		}
		// Detect on this container's OWN signals plus any override naming it specifically. The
		// WORKLOAD-level override is deliberately withheld here: it describes "the application", so
		// broadcasting it would make every container in the task look coverable and turn a proxy into
		// a target. It is applied afterwards, once the set is known and unambiguous.
		var ovLang, ovVer string
		if ov := src.containerOverride(name); ov != nil {
			ovLang, ovVer = ov.RuntimeOverride.Language, ov.RuntimeOverride.Version
		}
		if len(candidates) == 1 {
			if ovLang == "" {
				ovLang = src.language
			}
			if ovVer == "" {
				ovVer = src.version
			}
		}
		// An owned container remembers the language we injected into it, which is more precise than
		// the workload-level stored runtime once a task holds several languages — and more reliable,
		// since our own injected env can otherwise confuse re-detection.
		storedLang := containerManifestLanguage(c)
		rt := resolveECSRuntime(ctx, i.images, ovLang, ovVer, storedLang, "", c)

		if rt.language == "" {
			detected = append(detected, name+"=undetected")
			excluded[name] = uncoverableLanguageReason("")
			continue
		}
		detected = append(detected, name+"="+rt.language)
		if !CoverableLanguage(rt.language) {
			excluded[name] = uncoverableLanguageReason(rt.language)
			continue
		}
		coverable = append(coverable, candidate{idx: idx, name: name, rt: rt, named: src.names(name)})
	}

	// Pass 2: keep only the strongest evidence present in this task definition.
	//
	// The gate is RELATIVE, not an absolute threshold. Detection confidence is a comparison between
	// siblings, not a measure of truth: five containers identified only by their image reference
	// (openjdk:21, python:3.12) are all weak yet all correct, while nginx looking like PHP because its
	// inline fastcgi config names index.php is weak NEXT TO a container with a real PHP signal. An
	// absolute threshold would instrument neither group; ranking instruments the first and rejects the
	// second. A container the source names explicitly bypasses the gate entirely.
	var top detectionConfidence
	for _, c := range coverable {
		if !c.named && c.rt.confidence > top {
			top = c.rt.confidence
		}
	}

	var targets []containerTarget
	for _, c := range coverable {
		if !c.named && c.rt.confidence < top {
			excluded[c.name] = fmt.Sprintf("detected %s only from a weaker signal than another container in this task; name it in a container override to instrument it anyway", c.rt.language)
			continue
		}
		if len(coverable) == 1 {
			if ov := src.containerOverride(c.name); ov == nil || (ov.RuntimeOverride.Language == "" && ov.RuntimeOverride.Version == "") {
				c.rt = resolveECSRuntime(ctx, i.images, src.language, src.version, c.rt.language, c.rt.version, &td.ContainerDefinitions[c.idx])
			}
		}
		if c.rt.language == LangDotNet && c.rt.musl {
			excluded[c.name] = "the .NET Fargate profiler requires glibc; Alpine/musl images are unsupported"
			continue
		}
		recipe, err := i.recipes.recipeFor(c.rt.language, c.rt.version)
		if err != nil {
			if errors.Is(err, errNeedsRuntimeVersion) {
				excluded[c.name] = fmt.Sprintf("the %s agent is staged per runtime version and this container's major.minor version could not be determined — set a runtime override with a version (e.g. %q) for this container", c.rt.language, "8.3")
				continue
			}
			return nil, excluded, err
		}
		export, err := exportTargetFor(recipe, dest)
		if err != nil {
			// A destination that cannot carry this language's transport excludes the container, not
			// the whole task: its java sibling may export perfectly well.
			excluded[c.name] = err.Error()
			continue
		}
		targets = append(targets, containerTarget{idx: c.idx, name: c.name, runtime: c.rt, recipe: recipe, export: export})
	}

	if len(targets) == 0 {
		return nil, excluded, fmt.Errorf("%w: no container in task definition %s could be instrumented (%s) — set a runtime override, or name a container in a container override on the source",
			ErrUnsupportedInstrumentation, aws.ToString(td.Family), strings.Join(detected, ", "))
	}
	// The workload-level override names "the application", which is only unambiguous when exactly one
	// container is instrumented. Apply it to that one — correcting a mis-detected language or
	// supplying the version php/ruby need — and ignore it when several containers are targeted, where
	// a per-container override is the only way to say which one it meant.
	if len(targets) == 1 && (src.language != "" || src.version != "") {
		t := &targets[0]
		if ov := src.containerOverride(t.name); ov == nil || (ov.RuntimeOverride.Language == "" && ov.RuntimeOverride.Version == "") {
			if refined, err := i.retarget(ctx, td, *t, src.language, src.version, dest); err == nil {
				*t = refined
			} else {
				return nil, excluded, fmt.Errorf("apply runtime override for %s: %w", t.name, err)
			}
		}
	}
	return targets, excluded, nil
}

// retarget re-resolves one target with an explicit language/version override applied, rebuilding its
// recipe and export wiring to match.
func (i *ecsInspector) retarget(ctx context.Context, td *ecstypes.TaskDefinition, t containerTarget, language, version string, dest domaindest.OTLPTarget) (containerTarget, error) {
	c := &td.ContainerDefinitions[t.idx]
	rt := resolveECSRuntime(ctx, i.images, language, version, containerManifestLanguage(c), "", c)
	if !CoverableLanguage(rt.language) {
		return t, fmt.Errorf("override resolves to %q, which cannot be instrumented on Fargate", rt.language)
	}
	recipe, err := i.recipes.recipeFor(rt.language, rt.version)
	if err != nil {
		return t, err
	}
	export, err := exportTargetFor(recipe, dest)
	if err != nil {
		return t, err
	}
	return containerTarget{idx: t.idx, name: t.name, runtime: rt, recipe: recipe, export: export}, nil
}

// aggregateRuntime collapses the targets into the workload-level runtime the flat report fields
// carry. One language is reported as itself; several are reported as runtimeLanguageMultiple, because no
// single value is true and the container layer holds the real per-container answers.
func aggregateRuntime(targets []containerTarget) (language, version string) {
	if len(targets) == 0 {
		return "", ""
	}
	lang, ver := targets[0].recipe.lang, targets[0].runtime.version
	for _, t := range targets[1:] {
		if t.recipe.lang != lang {
			return runtimeLanguageMultiple, ""
		}
		if t.runtime.version != ver {
			ver = ""
		}
	}
	return lang, ver
}

// targetSummary renders the targets for a log line: "frontend=java, inventory=python".
func targetSummary(targets []containerTarget) string {
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		parts = append(parts, t.name+"="+t.recipe.lang)
	}
	return strings.Join(parts, ", ")
}

func ovDisabled(src enabledFargateSource, name string) bool {
	ov := src.containerOverride(name)
	return ov != nil && !ov.InstrumentationEnabled()
}

// recordRuntime persists the workload-level runtime the flat report fields carry — the language and version
// the central UI shows for the service. Written on change only, so a steady-state service costs no database
// write per cycle, and best-effort: a report that cannot name the runtime is worse than one that can, but
// neither is worth failing a pass over.
//
// It lives on the inspector because BOTH modes have to write it, and for a while only one did. The single
// writer was the reconciler's instrumentService, which the coordinator never runs for a manual-mode type
// (activeReconcilers) — so every workload the customer instruments themselves reported an unknown language in
// the central UI no matter how plainly its task definition declared one. The observer now writes it from the
// same resolution on both of its paths, which is the whole point of the two modes sharing this type.
func (i *ecsInspector) recordRuntime(ctx context.Context, svc store.ECSService, language, version string) {
	if language == svc.RuntimeLanguage && version == svc.RuntimeVersion {
		return
	}
	if err := i.store.SetECSServiceRuntime(ctx, svc.ClusterARN, svc.ServiceName, language, version); err != nil {
		log.Warn("set fargate runtime failed", "cluster", svc.ClusterARN, "service", svc.ServiceName, "error", err)
		return
	}
	log.Debug("recorded fargate workload runtime", "cluster", svc.ClusterARN, "service", svc.ServiceName,
		"language", language, "version", version)
}

// recordContainers persists the task definition's container inventory so the workload report can show
// the container layer — every container, instrumented or not, with the reason for each exclusion.
//
// Best-effort: a report that cannot show containers is worse than one that can, but neither is worth
// failing instrumentation over.
func (i *ecsInspector) recordContainers(ctx context.Context, td *ecstypes.TaskDefinition, src enabledFargateSource, svc store.ECSService, targets []containerTarget, excluded map[string]string) {
	if td == nil {
		return
	}
	byIdx := make(map[int]containerTarget, len(targets))
	for _, t := range targets {
		byIdx[t.idx] = t
	}
	// Per-container ownership read from the live task definition, which is the only precise answer. It used
	// to be the SERVICE-level instrumented flag applied to every target, which is wrong in both directions in
	// exactly the situations that matter: in a task where one container is owned and another was just added
	// as a target, the new one reported instrumented; and in manual (IaC) mode, where nothing writes the
	// service flag until the observer does, every container reported uninstrumented no matter what the
	// customer had applied.
	// Either marker: a container the CUSTOMER instrumented in their own definition is instrumented, and the
	// report is what tells them so. Restricting this to the ownership marker is exactly the manual-mode bug the
	// comment above describes, one layer down.
	owned := make(map[int]bool, len(td.ContainerDefinitions))
	for _, idx := range instrumentedContainerIndexes(td) {
		owned[idx] = true
	}
	out := make([]store.ECSContainer, 0, len(td.ContainerDefinitions))
	for idx := range td.ContainerDefinitions {
		c := &td.ContainerDefinitions[idx]
		name := containerName(c)
		row := store.ECSContainer{
			Name:          name,
			Image:         aws.ToString(c.Image),
			Essential:     c.Essential == nil || *c.Essential,
			OdigosManaged: name == sidecarContainerName,
		}
		if ov := src.containerOverride(name); ov != nil {
			row.OverrideLanguage = ov.RuntimeOverride.Language
			row.OverrideVersion = ov.RuntimeOverride.Version
		}
		// Essential and OdigosManaged stay on this side of the wire: the report has no field for
		// either, because a client cannot do anything with "essential: false" without knowing ECS's
		// rules. They are inputs to the reason below, which is what actually travels. That makes the
		// reason load-bearing — an ineligible container with a blank reason tells an operator nothing
		// at all — so every branch here ends with one set.
		switch t, targeted := byIdx[idx]; {
		case row.OdigosManaged:
			row.ExcludedReason = "Odigos agents sidecar"
		case targeted:
			row.RuntimeLanguage, row.RuntimeVersion = t.runtime.language, t.runtime.version
			row.Eligible = true
			row.Instrumented = owned[idx]
			for _, d := range FargateDistros() {
				if d.Language == t.recipe.lang {
					row.Distro = d.Name
					break
				}
			}
		case excluded[name] != "":
			row.ExcludedReason = excluded[name]
		case !row.Essential:
			// Never a candidate: ECS does not treat it as part of the application, so target selection
			// never looked at it and has no reason of its own to give.
			row.ExcludedReason = "non-essential container"
		default:
			// An essential container that selection neither targeted nor rejected. Target selection
			// gives every candidate it rejects a reason, so reaching here means it stopped early on a
			// hard error before considering this container. Say that rather than nothing.
			row.ExcludedReason = "instrumentation could not be evaluated for this container"
		}
		out = append(out, row)
	}
	if err := i.store.SetECSServiceContainers(ctx, svc.ClusterARN, svc.ServiceName, out); err != nil {
		log.Warn("recording fargate container inventory failed", "cluster", svc.ClusterARN, "service", svc.ServiceName, "error", err)
	}
}

// while the rest fall back to the other transport, since their SDKs honor OTEL_EXPORTER_OTLP_PROTOCOL.
// Endpoint and protocol are always set together — a mismatch between them (an HTTP request to a gRPC
// listener, or vice versa) fails at runtime as an opaque connection reset.
func exportTargetFor(recipe agentRecipe, dest domaindest.OTLPTarget) (ecsExportTarget, error) {
	t := ecsExportTarget{traces: dest.Traces, metrics: dest.Metrics, logs: dest.Logs}
	grpcURL := grpcEndpointURL(dest.Endpoint, dest.Insecure)
	httpURL := dest.HTTPEndpoint

	// Preferred transport, when the destination provides it.
	if recipe.otlpProtocol == otlpProtocolHTTP && httpURL != "" {
		t.endpoint, t.protocol = httpURL, otlpProtocolHTTP
		return t, nil
	}
	if recipe.otlpProtocol == otlpProtocolGRPC && grpcURL != "" {
		t.endpoint, t.protocol = grpcURL, otlpProtocolGRPC
		return t, nil
	}
	// Preferred transport unavailable: a pinned agent cannot speak the other one.
	if recipe.protocolFixed {
		return t, fmt.Errorf("the %s agent exports OTLP over %s only, but the destination provides no %s endpoint — configure one to instrument this service",
			recipe.lang, recipe.otlpProtocol, transportLabel(recipe.otlpProtocol))
	}
	switch {
	case httpURL != "":
		t.endpoint, t.protocol = httpURL, otlpProtocolHTTP
	case grpcURL != "":
		t.endpoint, t.protocol = grpcURL, otlpProtocolGRPC
	default:
		return t, fmt.Errorf("destination has no OTLP endpoint configured")
	}
	return t, nil
}

func transportLabel(protocol string) string {
	if protocol == otlpProtocolHTTP {
		return "OTLP/HTTP (OTLP_HTTP_ENDPOINT)"
	}
	return "OTLP/gRPC (OTLP_GRPC_ENDPOINT)"
}

// Standard OTLP receiver ports. A destination that pairs one with the other protocol is almost always a
// misconfiguration, and the runtime symptom (a connection reset mid-export) points nowhere near the cause.
const (
	otlpDefaultGRPCPort = "4317"
	otlpDefaultHTTPPort = "4318"
)

// portProtocolMismatch returns a warning when an endpoint's port is the well-known port of the OTHER OTLP
// protocol, or "" when the pairing looks right (including non-standard ports, which we cannot judge).
func portProtocolMismatch(endpoint, protocol string) string {
	port := endpointPort(endpoint)
	switch {
	case protocol == otlpProtocolHTTP && port == otlpDefaultGRPCPort:
		return "destination endpoint " + endpoint + " uses the standard OTLP/gRPC port " + otlpDefaultGRPCPort +
			" but this agent exports over http/protobuf; exports will fail as a connection reset — point it at the OTLP/HTTP port (" + otlpDefaultHTTPPort + ")"
	case protocol == otlpProtocolGRPC && port == otlpDefaultHTTPPort:
		return "destination endpoint " + endpoint + " uses the standard OTLP/HTTP port " + otlpDefaultHTTPPort +
			" but this agent exports over gRPC; exports will fail — point it at the OTLP/gRPC port (" + otlpDefaultGRPCPort + ")"
	}
	return ""
}

// endpointPort extracts the port from a scheme://host:port[/path] endpoint ("" when absent).
func endpointPort(endpoint string) string {
	rest := endpoint
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return ""
	}
	return rest[i+1:]
}

// describeService returns the live ECS service, or nil when it no longer exists / is inactive (a MISSING
// failure or INACTIVE status — the caller treats both as "gone"; discovery prunes the row).
func describeService(ctx context.Context, client *ecs.Client, clusterARN, serviceName string) (*ecstypes.Service, error) {
	out, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(clusterARN),
		Services: []string{serviceName},
	})
	if err != nil {
		return nil, err
	}
	for _, f := range out.Failures {
		if strings.EqualFold(aws.ToString(f.Reason), "MISSING") {
			return nil, nil
		}
		return nil, fmt.Errorf("describe service %s: reason=%s detail=%s", serviceName, aws.ToString(f.Reason), aws.ToString(f.Detail))
	}
	if len(out.Services) == 0 {
		return nil, nil
	}
	svc := out.Services[0]
	if strings.EqualFold(aws.ToString(svc.Status), "INACTIVE") {
		return nil, nil
	}
	return &svc, nil
}

// rolloutInProgress reports whether the service has a deployment still settling: the PRIMARY deployment
// rolling out, or an old deployment still draining (more than one deployment present).
func rolloutInProgress(svc *ecstypes.Service) bool {
	if len(svc.Deployments) > 1 {
		return true
	}
	for _, d := range svc.Deployments {
		if strings.EqualFold(aws.ToString(d.Status), "PRIMARY") && d.RolloutState == ecstypes.DeploymentRolloutStateInProgress {
			return true
		}
	}
	return false
}

// liveDeploymentController returns the service's deployment controller type (nil means the default ECS
// rolling-update controller).
func liveDeploymentController(svc *ecstypes.Service) ecstypes.DeploymentControllerType {
	if svc.DeploymentController == nil {
		return ecstypes.DeploymentControllerTypeEcs
	}
	return svc.DeploymentController.Type
}

// taskDefOSFamily returns the task definition's declared OS family ("" when unset, which means Linux).
func taskDefOSFamily(td *ecstypes.TaskDefinition) string {
	if td.RuntimePlatform == nil {
		return ""
	}
	return string(td.RuntimePlatform.OperatingSystemFamily)
}

// withPassRoleHint appends a remediation hint to AccessDenied errors from RegisterTaskDefinition /
// UpdateService: the non-obvious permission is iam:PassRole on the task definition's task/execution
// roles (Fargate task definitions virtually always carry an execution role for the image pull).
func withPassRoleHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		return err
	}
	return fmt.Errorf("%w (if the denial names iam:PassRole, grant the connector iam:PassRole on the task definition's task and execution roles — scope it with the iam:PassedToService=ecs-tasks.amazonaws.com condition)", err)
}

// setECSPhase records an in-flight instrumentation phase for live WorkloadReport progress. Best-effort.
func (i *ecsInspector) setECSPhase(ctx context.Context, svc store.ECSService, phase string) {
	if err := i.store.SetECSServiceInstrumentationPhase(ctx, svc.ClusterARN, svc.ServiceName, phase); err != nil {
		log.Warn("set fargate instrumentation phase failed", "service", svc.ServiceName, "phase", phase, "error", err)
	}
}

// clearECSPhase drops the in-flight phase once a service has converged. Best-effort like setECSPhase.
func (i *ecsInspector) clearECSPhase(ctx context.Context, svc store.ECSService) {
	if err := i.store.ClearECSServiceInstrumentationPhase(ctx, svc.ClusterARN, svc.ServiceName); err != nil {
		log.Warn("clear fargate instrumentation phase failed", "service", svc.ServiceName, "error", err)
	}
}

func (i *ecsInspector) ecsClient(ctx context.Context, region string) (*ecs.Client, error) {
	cfg, err := i.cache.Config(ctx, region)
	if err != nil {
		return nil, err
	}
	return ecs.NewFromConfig(cfg), nil
}
