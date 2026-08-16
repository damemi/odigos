package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// ECSFargateObserver is the READ-ONLY conformance observer for Fargate services: it answers "does the
// instrumentation actually on this service's task definition match what Odigos wants for it", and it is the
// only instrumentation code path the base runs for a manual (IaC) mode type.
//
// It embeds ecsInspector, so which containers are instrumentable, with which agent, exporting where is resolved
// by exactly the code the instrumentor uses, and the comparison itself is the instrumentor's own
// containerDiff/taskLevelDiff. What it does NOT embed is the write half: RegisterTaskDefinition and
// UpdateService live on ECSFargateInstrumentor, so the two AWS mutations §7.4 lists are not reachable from
// here — the gate is the absence of the code, not a flag someone can get wrong.
//
// The reviewable invariant is that this file makes exactly two kinds of AWS call, DescribeServices and
// DescribeTaskDefinition, both already granted by the fargate-task DISCOVERY permissions (Wave 3.1 added
// DescribeTaskDefinition there precisely so manual mode could see what the customer applied).
type ECSFargateObserver struct {
	*ecsInspector
	// now is the clock for the drift since-stamp, injectable so a test can assert the stamp is carried forward
	// across passes rather than refreshed.
	now func() time.Time
}

// NewECSFargateObserver builds the read-only Fargate observer over the same store, cache, agents image, debug
// toggle and image inspector the instrumentor uses, so both judge conformance against identical desired inputs.
func NewECSFargateObserver(st *store.Store, cache *awsclient.Cache, image string, debug func() bool, images ImageInspector) *ECSFargateObserver {
	return &ECSFargateObserver{ecsInspector: newECSInspector(st, cache, image, debug, images), now: time.Now}
}

// Observe implements baseapi.InstrumentationObserver.
//
// Scope is every service the customer MARKED, plus every service Odigos records as instrumented even without an
// enabled source. That second set is not optional: unlike Lambda — where discovery re-derives instrumentation
// from the same ListFunctions call it already makes — a Fargate service's ownership marker lives inside its task
// definition, one DescribeTaskDefinition away, which discovery deliberately does not pay across a whole
// account every cycle. So the flag is sticky, and without observing the previously-instrumented set a customer
// who removes instrumentation from their IaC would be told "instrumentation is still applied, go remove it"
// forever. Bounded by services Odigos has actually seen instrumented, not by account size.
func (o *ECSFargateObserver) Observe(ctx context.Context) error {
	enabled, err := o.enabledSources(ctx)
	if err != nil {
		return err
	}
	services, err := o.store.ListECSServices(ctx)
	if err != nil {
		return err
	}
	dest, haveDest, err := o.store.OTLPExportTarget(ctx)
	if err != nil {
		return fmt.Errorf("resolve otlp export target: %w", err)
	}

	byRegion := map[string][]store.ECSService{}
	matched := make(map[string]bool, len(enabled))
	for _, svc := range services {
		extID := svc.ClusterARN + "/" + svc.ServiceName
		_, want := enabled[extID]
		if want {
			matched[extID] = true
		}
		if want || svc.Instrumented {
			byRegion[svc.Region] = append(byRegion[svc.Region], svc)
		}
	}

	var errs []error
	for region, svcs := range byRegion {
		client, err := o.ecsClient(ctx, region)
		if err != nil {
			errs = append(errs, fmt.Errorf("ecs client for %s: %w", region, err))
			continue
		}
		for _, svc := range svcs {
			src, want := enabled[svc.ClusterARN+"/"+svc.ServiceName]
			var err error
			switch {
			case want && haveDest:
				err = o.observeService(ctx, client, svc, src, dest)
			default:
				// Either no intent, or intent with no destination yet. In both cases there is no desired
				// instrumentation to compare against — the destination endpoint is part of it — so all that can
				// be said is whether instrumentation is currently applied. A conformance verdict computed
				// against a zero destination would report every correctly instrumented service as drifted on
				// its export settings, which is worse than saying nothing.
				//
				// The flag itself still matters in both cases: it drives the report's "there is live
				// instrumentation left to remove" branch, and it is the only thing that tells a customer who
				// removed instrumentation from their IaC that Odigos noticed.
				err = o.refreshOwnership(ctx, client, svc)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("observe %s: %w", svc.ServiceName, err))
			}
		}
	}
	o.noteUnobservableStandaloneTasks(enabled, matched)
	return errors.Join(errs...)
}

// refreshOwnership updates only the instrumented flag for a service with no enabled source, from the ownership
// markers on the task definition it currently runs.
func (o *ECSFargateObserver) refreshOwnership(ctx context.Context, client *ecs.Client, svc store.ECSService) error {
	live, err := describeService(ctx, client, svc.ClusterARN, svc.ServiceName)
	if err != nil {
		return err
	}
	if live == nil {
		return nil // gone or inactive; discovery prunes the row
	}
	td, err := o.describeTaskDefinition(ctx, client, live)
	if err != nil {
		return err
	}
	owned := len(ownedContainerIndexes(td)) > 0
	if owned == svc.Instrumented {
		return nil
	}
	return o.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, owned)
}

// observeService observes one marked service and persists what it found.
func (o *ECSFargateObserver) observeService(ctx context.Context, client *ecs.Client, svc store.ECSService, src enabledFargateSource, dest domaindest.OTLPTarget) error {
	live, err := describeService(ctx, client, svc.ClusterARN, svc.ServiceName)
	if err != nil {
		return err
	}
	if live == nil {
		return nil
	}
	prev := svc.Observation

	// A deployment still rolling out — ours from a previous life, or the customer's, just landing — means the
	// live task definition and the running tasks disagree. Judging now would report a drift that is about to
	// resolve itself and, worse, would reset the drift clock of a genuinely broken service every time the
	// customer deployed. Record that it is settling, keep the last settled verdict, and judge on a later pass.
	// This is §7.3's rule at its source: drift is only ever declared after a successful, SETTLED read.
	if rolloutInProgress(live) {
		observeLog.Debug("skipping observation: a deployment is still rolling out",
			"cluster", svc.ClusterARN, "service", svc.ServiceName)
		return o.persist(ctx, svc, store.ECSObservation{
			Conformance: prev.Conformance,
			Missing:     prev.Missing,
			Error:       prev.Error,
			Settling:    true,
		})
	}

	td, err := o.describeTaskDefinition(ctx, client, live)
	if err != nil {
		return err
	}
	targets, excluded, selectErr := o.selectTargets(ctx, td, src, svc, dest)
	// Record the container inventory even when nothing could be targeted: that is exactly when an operator
	// needs to see each container and why it was skipped. In manual mode this is the whole container layer of
	// the report, which nothing populated before — the instrumentor recorded it, and the instrumentor never runs.
	o.recordContainers(ctx, td, src, svc, targets, excluded)

	ev := evaluateECSObservation(td, targets, selectErr, src, svc, o.image, o.debug())
	if ev.owned != svc.Instrumented {
		if err := o.store.SetECSServiceInstrumented(ctx, svc.ClusterARN, svc.ServiceName, ev.owned); err != nil {
			observeLog.Warn("set fargate instrumented flag failed", "service", svc.ServiceName, "error", err)
		}
	}
	if ev.terminal != "" {
		o.setTerminal(ctx, svc, ev.terminal)
	} else {
		o.clearTerminal(ctx, svc)
	}
	// An incomplete check preserves the last confirmed verdict rather than downgrading it, so the record the
	// evaluation asks for is completed from what was already stored.
	if ev.obs.Error != "" && ev.obs.Conformance == baseapi.ConformanceUnknown {
		ev.obs.Conformance, ev.obs.Missing = prev.Conformance, prev.Missing
	}
	return o.persist(ctx, svc, ev.obs)
}

// ecsEvaluation is the verdict of judging one settled read of a service, and the two side facts the caller has
// to persist alongside it.
type ecsEvaluation struct {
	obs store.ECSObservation
	// owned is whether ANY container carries the Odigos ownership marker — the service's instrumented flag.
	owned bool
	// terminal is an unsupported-prefixed message to record, or "" to clear whatever is recorded.
	terminal string
}

// evaluateECSObservation is the whole judgment of one read, kept pure so every branch below is testable without
// an AWS client or a database: which facts make a service conformant, drifted, unsupported, or unverifiable is
// the part that has to be right.
func evaluateECSObservation(td *ecstypes.TaskDefinition, targets []containerTarget, selectErr error,
	src enabledFargateSource, svc store.ECSService, image string, debug bool) ecsEvaluation {

	ownedIdx := ownedContainerIndexes(td)
	owned := len(ownedIdx) > 0

	// The one exclusion that is genuinely terminal in BOTH modes: the Odigos Fargate agents bundle is
	// Linux-only, so a Windows task definition cannot carry the agent however it is applied.
	//
	// The service's deployment controller is deliberately NOT checked, and that is the substantive difference
	// between the two modes. CODE_DEPLOY and EXTERNAL block UpdateService from changing a task definition —
	// they block ODIGOS's rollout, not instrumentation itself. A customer running blue/green deploys
	// instruments the task definition in their own pipeline and it works; inheriting the automatic-mode
	// exclusion would report a correctly-instrumented service as unsupported and tell them to stop using
	// CodeDeploy.
	if os := taskDefOSFamily(td); os != "" && os != string(ecstypes.OSFamilyLinux) {
		return ecsEvaluation{
			owned: owned,
			obs:   store.ECSObservation{Conformance: baseapi.ConformanceAbsent},
			terminal: fmt.Sprintf("%s: task definition targets OS family %s; the Odigos Fargate agents bundle is Linux-only",
				baseapi.ErrPrefixUnsupported, os),
		}
	}

	if !owned {
		// Nothing on this service is attributable to Odigos. Absent, not Partial: there is nothing applied, as
		// opposed to something applied incorrectly, and the report renders that as awaiting the customer.
		//
		// A selection failure here IS worth recording, because it explains why that wait will never resolve on
		// its own — no container Odigos can recognize, or a php/ruby container whose major.minor it cannot
		// determine. The message already names the remedy (a runtime override).
		ev := ecsEvaluation{owned: false, obs: store.ECSObservation{Conformance: baseapi.ConformanceAbsent}}
		if selectErr != nil {
			ev.terminal = selectErr.Error()
		}
		return ev
	}

	if selectErr != nil {
		// Instrumentation IS applied, but the desired state cannot be recomputed right now — most plausibly a
		// version-templated agent (php/ruby) whose major.minor would have come from an image-config read the
		// manual-mode policy does not grant. The caller preserves the last confirmed verdict and reports the
		// check as incomplete; declaring drift on the strength of not having been able to look would be
		// manufactured drift.
		return ecsEvaluation{
			owned: true,
			obs:   store.ECSObservation{Error: fmt.Sprintf("could not determine the expected instrumentation: %v", selectErr)},
		}
	}

	diff, incomplete := ecsOwnedDiff(td, ownedIdx, targets, src.serviceName, svc, image, debug)
	switch {
	case incomplete != "":
		return ecsEvaluation{owned: true, obs: store.ECSObservation{Error: incomplete}}
	case len(diff) == 0:
		return ecsEvaluation{owned: true, obs: store.ECSObservation{Conformance: baseapi.ConformanceConformant}}
	default:
		return ecsEvaluation{owned: true, obs: store.ECSObservation{Conformance: baseapi.ConformancePartial, Missing: diff}}
	}
}

// ecsOwnedDiff is the conformance diff for a service, judged over the containers Odigos OWNS plus the
// task-level agent staging they all depend on. It returns the differing components, or a reason the check could
// not be completed.
//
// Owned-only is the deliberate choice, and it is where manual mode departs from the mutating path's
// alreadyInstrumented. That one requires the owned set to EQUAL the target set, because for it a mismatch means
// "converge it", which costs one revision. Here a target that is not owned would mean telling a customer their
// deployment has drifted because Odigos detected a container they chose not to instrument — a coverage opinion
// rather than decay, resting on language detection whose image-config layer the manual-mode policy does not
// grant. Such a container is still reported: it appears in the container inventory with its detected language
// and eligibility, which is the honest way to surface "you could also instrument this".
//
// What IS judged is everything that can decay under the customer: each owned container's activation env, export
// wiring, mount and startup dependency, and the sidecar + volume that stage the agent files. A container with a
// perfect env and no sidecar exports nothing at all — the exact ECS analog of Lambda's detached layer, and the
// false green this pass exists to close.
func ecsOwnedDiff(td *ecstypes.TaskDefinition, ownedIdx []int, targets []containerTarget, reportedServiceName string, svc store.ECSService, image string, debug bool) (diff []string, incomplete string) {
	serviceName := reportedServiceName
	if serviceName == "" {
		serviceName = svc.ServiceName
	}
	byIdx := make(map[int]containerTarget, len(targets))
	for _, t := range targets {
		byIdx[t.idx] = t
	}
	seen := map[string]bool{}
	add := func(components []string) {
		for _, c := range components {
			if !seen[c] {
				seen[c] = true
				diff = append(diff, c)
			}
		}
	}
	var ownedTargets []containerTarget
	for _, idx := range ownedIdx {
		t, ok := byIdx[idx]
		if !ok {
			// Odigos owns this container but can no longer resolve what its instrumentation should be — the
			// customer swapped its image for a language Odigos does not cover, or its detection stopped
			// resolving. There is no desired state to compare against, so the check is incomplete rather than
			// failed; saying "drifted" would be a guess about a container whose target we could not build.
			return nil, fmt.Sprintf("could not determine the expected instrumentation for container %q, which carries Odigos instrumentation",
				containerName(&td.ContainerDefinitions[idx]))
		}
		ownedTargets = append(ownedTargets, t)
		add(containerDiff(&td.ContainerDefinitions[idx], t, serviceName, svc.Region, debug))
	}
	// The sidecar command stages the union of the AGENTS THE OWNED CONTAINERS NEED, so it is compared against
	// the owned set for the same reason the containers are: a language the customer chose not to instrument
	// must not make the sidecar look wrong.
	add(taskLevelDiff(td, ownedTargets, image))
	return diff, ""
}

// describeTaskDefinition reads the task definition a service currently runs. Tags are deliberately NOT
// requested: they exist to be round-tripped into RegisterTaskDefinition, and this path never registers anything.
func (o *ECSFargateObserver) describeTaskDefinition(ctx context.Context, client *ecs.Client, live *ecstypes.Service) (*ecstypes.TaskDefinition, error) {
	out, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: live.TaskDefinition})
	if err != nil {
		return nil, err
	}
	if out.TaskDefinition == nil {
		return nil, fmt.Errorf("describe task definition returned no definition")
	}
	return out.TaskDefinition, nil
}

// setTerminal / clearTerminal record a condition that makes instrumentation impossible for this service
// regardless of who applies it, using the same instrumentation_error channel the instrumentor uses so the
// report classifies it identically (an ErrPrefixUnsupported message becomes an Unsupported state).
//
// Two writers again, and again they cannot collide: the instrumentor owns this key in automatic mode, the
// observer in manual mode, and a type is never in both (coordinator.activeObservers). The observer rewrites or
// clears it on every pass, so unlike a one-shot write it has a clearing rule — a customer who fixes the
// condition sees it disappear.
func (o *ECSFargateObserver) setTerminal(ctx context.Context, svc store.ECSService, msg string) {
	if svc.InstrumentationError == msg {
		return
	}
	if err := o.store.SetECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName, msg); err != nil {
		observeLog.Warn("set fargate instrumentation error failed", "service", svc.ServiceName, "error", err)
	}
}

func (o *ECSFargateObserver) clearTerminal(ctx context.Context, svc store.ECSService) {
	if svc.InstrumentationError == "" {
		return
	}
	if err := o.store.ClearECSServiceInstrumentationError(ctx, svc.ClusterARN, svc.ServiceName); err != nil {
		observeLog.Warn("clear fargate instrumentation error failed", "service", svc.ServiceName, "error", err)
	}
}

// persist writes a verdict only when something moved, carrying the drift stamp forward otherwise. The stamp
// rule itself is the base's (api.ConformanceSince).
//
// Unlike Lambda this needs no fingerprints: the verdict plus its component list plus the settling and error
// flags ARE the change test, because any desired input that matters shows up as one of them changing.
func (o *ECSFargateObserver) persist(ctx context.Context, svc store.ECSService, obs store.ECSObservation) error {
	prev := svc.Observation
	unchanged := obs.Conformance == prev.Conformance &&
		obs.Error == prev.Error &&
		obs.Settling == prev.Settling &&
		sameComponents(obs.Missing, prev.Missing)
	if unchanged && !prev.Since.IsZero() {
		return nil
	}
	obs.Since = baseapi.ConformanceSince(prev.Conformance, prev.Since, obs.Conformance, o.now())
	return o.store.SetECSServiceObservation(ctx, svc.ClusterARN, svc.ServiceName, obs)
}

// sameComponents compares two component lists as ordered sequences. Both are produced by the same diff in the
// same order, so ordering is stable and an order change would itself be a real change.
func sameComponents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// noteUnobservableStandaloneTasks logs the marked standalone ECS tasks this pass cannot say anything about.
//
// A standalone task (RunTask/EventBridge, no service) is the one Fargate workload manual mode cannot verify, and
// the reason is identity rather than capability. Its task definition is perfectly instrumentable by the
// customer's own IaC — the automatic-mode exclusion is about Odigos having no UpdateService rollout path, which
// does not apply when the customer deploys it themselves. But a standalone task is keyed by task ARN, which is
// minted fresh on every run, so a conformance verdict would be attached to an identity that no longer exists by
// the time anyone reads it. Verifying these needs the durable thing — the task definition — to be a workload in
// its own right, which is a modelling decision, not a gap in this pass.
//
// Until then such a workload reports as awaiting the customer, which is not a false statement: Odigos genuinely
// is waiting, it just cannot tell when the deployment lands. Deliberately NOT written as an unsupported error
// the way automatic mode does — that message tells the customer to convert the workload into an ECS service to
// instrument it, and in manual mode that is untrue.
func (o *ECSFargateObserver) noteUnobservableStandaloneTasks(enabled map[string]enabledFargateSource, matched map[string]bool) {
	for extID := range enabled {
		if matched[extID] || !strings.Contains(extID, ":task/") {
			continue
		}
		observeLog.Debug("standalone ecs task is marked for instrumentation but cannot be verified: its identity is the task ARN, which changes on every run",
			"task", extID)
	}
}
