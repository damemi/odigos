package deployer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	awsingress "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/ingress"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/settings"
)

// Deployer is the api.CollectorDeployer for AWS: the loop that calls this package's Ensure* halves in
// order, records what they produced, and tears a Collector down when it is time.
//
// It exists because every half here is deliberately incapable of driving itself. `EnsureNetwork`
// converges secrets and knows nothing about a service; `EnsureCollectorService` names a cluster, a
// revision, subnets and target groups and creates none of them; `RecordCollectorInfrastructure` writes
// seven refs together and would null the other four if called with half of them. Somebody has to hold
// the order, and it is here — once, so there is one order rather than one per caller.
//
// Registered for a type only when the customer's capability puts it in
// spec.ActiveCollectorDeploymentTypes, which is automatic mode and nothing else: a manual-mode Collector
// reaches no method on this type at all (api.CollectorDeployer).
type Deployer struct {
	// Provisioner holds the connector's identity, its account, its licence and the read/write clients.
	Provisioner *Provisioner
	// Teardown holds the destructive clients, which no field of Provisioner can reach.
	Teardown TeardownClients
	Store    DeployerStore
	// Plans serves the placement decision this pass resolved. See placement.Resolver.
	Plans Plans

	// Kind is the collectors type, and Mode its resolved deployment mode.
	Kind string
	Mode string

	// Settings reads the live settings tree on every call, so an updateConfig patch lands on the next pass.
	Settings func() collectorsettings.Settings
	// OpAMPEndpoint is what a deployed Collector dials back to, and Pin the image this build deploys.
	OpAMPEndpoint string
	Pin           collectorimage.Pin
	// Desired renders the configuration this Collector would be pushed, for the cold-start bootstrap
	// secret. Optional: without it the bootstrap document is empty and a cold start during an
	// outage serves the inert bundled configuration, which is v1's documented behaviour rather than a
	// fault. It must be the SAME renderer the OpAMP server serves from, or a cold start comes up on a
	// configuration that was never published and the difference is invisible.
	Desired func(ctx context.Context, key basestore.CollectorKey) (collectorconfig.Document, error)

	// Now is the clock the retention policy runs on, injectable for tests.
	Now func() time.Time

	// shared memoizes EnsureShared for one pass, and unresolvedCycles counts consecutive passes that
	// resolved no sender security group. Both under mu.
	mu               sync.Mutex
	sharedGen        uint64
	shared           Shared
	sharedOK         bool
	unresolvedCycles map[string]int
}

// Plans is the placement decision this pass resolved, and which pass it is.
//
// A reader rather than a resolver: resolving costs a topology scan per region, and the observer has
// already paid it this pass — `CollectorObserver.CollectorNetworks` is what the coordinator builds its
// target list from, before it converges any of them. The generation is how this type knows a new pass
// has started, which `api.CollectorDeployer` gives it no other way to see.
type Plans interface {
	Plan() (placement.Plan, uint64, bool)
}

// DeployerStore is every row this loop writes, and the credentials teardown revokes.
//
// One interface rather than several, because one type implements it and splitting it would suggest the
// halves could be given to different objects — they cannot: RecordCollectorInfrastructure and
// UpsertManagedCollector write the same row, and teardown's authority is exactly what the converge
// recorded.
type DeployerStore interface {
	CredentialStore
	TeardownStore
	RecordCollectorInfrastructure(ctx context.Context, key basestore.CollectorKey, rec basestore.CollectorRecord) error
	RecordCollectorEndpoint(ctx context.Context, key basestore.CollectorKey, grpc, http, source, tlsMode, tlsTermination, ingressTier string) error
	RecordCollectorScaling(ctx context.Context, key basestore.CollectorKey, mode string, desired, running, minCount, maxCount *int, cpu, memory string) error
	RecordCollectorIdle(ctx context.Context, key basestore.CollectorKey, idleSince *time.Time) error
	SetCollectorStatus(ctx context.Context, key basestore.CollectorKey, status collectorstatus.Status, message string) error
}

// Deployer satisfies the mutating half of the provider Collector surface. Asserted here so wiring it is
// a compile error away rather than a runtime surprise.
var _ baseapi.CollectorDeployer = (*Deployer)(nil)

// ConvergeCollector brings one network's Collector to the state its configuration asks for.
//
// Two entirely different jobs, decided by whether the plan still places a Collector on this network:
//
//   - PLACED — converge it. The halves run in the order their inputs force: the connector's roles, then
//     this network's secrets and credential, then its ingress, then its cluster and revision, then the
//     service, then the replica policy. Each returns problems and refs rather than erroring out, so a
//     network that cannot be finished is reported and the next one is still converged.
//   - IDLE — its last enabled source was removed, so there is nothing to converge it TOWARDS. The
//     retention policy decides how long to keep it, and its default answer is for ever.
//
// An error means the pass could not be made meaningful — no plan, no client, no database. Everything it
// FOUND is reported on the row instead: the coordinator logs an error from here and carries on, so the
// row is the channel a customer actually reads.
func (d *Deployer) ConvergeCollector(ctx context.Context, network baseapi.CollectorNetwork) error {
	key := basestore.CollectorKey{Region: network.Region}

	plan, generation, ok := d.Plans.Plan()
	if !ok {
		// Reached outside a pass. Converging on a guess is the one thing this must not do: the subnets
		// come from the plan, and a Collector placed in subnets nobody chose may have no egress route to
		// the destination at all — a failure that looks exactly like a healthy Collector.
		err := errors.New("no placement plan has been resolved this pass, so no Collector is converged")
		d.recordConvergenceError(ctx, key, err)
		return err
	}

	if plan.IsIdle(network.Region, network.NetworkID) {
		return d.retire(ctx, key, network)
	}

	// The clock stops the moment the network is known not to be idle, and BEFORE anything that can
	// refuse. A network whose placement was rejected — no subnet with an egress route, a name conflict —
	// still has enabled sources, so it is not idle, and a clock left running there would delete a
	// Collector the customer is actively using because Odigos could not converge it.
	d.clearIdle(ctx, key)

	place, ok := plan.Placement(network.Region, network.NetworkID)
	if !ok || !place.Deployable() {
		// Refused placement, reported against this row by the observer, which reads the same plan. Not
		// re-reported here: two writers on one status column is how half a row gets nulled.
		return nil
	}

	settings := d.Settings()
	networkSettings := settings.Region(network.Region).Network

	shared, err := d.sharedFor(ctx, plan, settings, generation)
	if err != nil {
		d.recordConvergenceError(ctx, key, err)
		return err
	}

	state, err := d.convergeNetwork(ctx, key, network, plan, place, networkSettings, shared)
	if err != nil {
		state.problems = append(state.problems, convergenceProblem(err))
	}
	d.record(ctx, key, networkSettings, state)
	return err
}

// recordConvergenceError preserves a raw provider/store failure on the Collector row.
//
// Ensure steps normally return structured Problems for standing conditions. Errors are different: they
// mean the call itself could not be completed (AccessDenied, throttling, transport failure, database
// failure). Returning one only to the coordinator used to leave it in the connector log while the API
// kept reporting Pending with no issue. The row is the customer-facing channel, so retain the original
// error there under a stable problem code. A missing row is handled by escalate's existing safe no-op.
func (d *Deployer) recordConvergenceError(ctx context.Context, key basestore.CollectorKey, err error) {
	if err == nil {
		return
	}
	d.escalate(ctx, key, []collectorstatus.Problem{convergenceProblem(err)})
}

func convergenceProblem(err error) collectorstatus.Problem {
	return collectorstatus.Problem{
		Code:     ProblemConvergenceFailed,
		Severity: collectorstatus.SeverityError,
		Message:  fmt.Sprintf("Collector convergence failed: %v", err),
	}
}

// convergedState is what one pass produced, gathered so the recording happens in one place.
//
// One structure carries cumulative refs across the stages. Infrastructure is checkpointed after
// each stage; those writes replace one column group and must include every earlier ref.
type convergedState struct {
	prerequisites Prerequisites
	ingress       Ingress
	workload      Workload
	service       Service
	scaling       ScalingState
	subnets       []string
	problems      []collectorstatus.Problem
}

// convergeNetwork runs the halves in order and stops at the first one whose output the next needs.
func (d *Deployer) convergeNetwork(
	ctx context.Context,
	key basestore.CollectorKey,
	network baseapi.CollectorNetwork,
	plan placement.Plan,
	place placement.Placement,
	settings collectorsettings.Network,
	shared Shared,
) (convergedState, error) {
	state := convergedState{subnets: place.SubnetIDs}

	document, err := d.desiredConfig(ctx, key)
	if err != nil {
		// Reported, never fatal. Without a document the Collector still deploys and is still configured
		// over OpAMP; what it loses is the cold-start mitigation, and refusing a deploy over a
		// mitigation would be worse than the thing it mitigates.
		state.problems = append(state.problems, collectorstatus.Problem{
			Code:     ProblemBootstrapUnavailable,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("this Collector's configuration could not be rendered for its cold-start "+
				"secret (%v), so a restart while the control plane is unreachable serves the inert bundled "+
				"configuration and accepts no telemetry until it connects", err),
		})
	}

	state.prerequisites, err = d.Provisioner.EnsureNetwork(ctx, NetworkInput{
		Key:              key,
		HostNetworkID:    network.NetworkID,
		Kind:             network.Kind,
		Mode:             d.Mode,
		Shared:           shared,
		RetentionDays:    settings.LogRetentionDays,
		Config:           document,
		ExecutionRoleARN: settings.ExecutionRoleARN,
	})
	state.problems = append(state.problems, state.prerequisites.Problems...)
	if err != nil || !state.prerequisites.Ready() {
		return state, err
	}

	sources := plan.IngressSources(network.Region, network.NetworkID)
	state.ingress, err = d.Provisioner.EnsureIngress(ctx, IngressInput{
		Key:              key,
		Mode:             d.Mode,
		Shared:           shared,
		VPCID:            network.NetworkID,
		Subnets:          state.subnets,
		NetworkBlocks:    plan.NetworkBlocks(network.NetworkID),
		Sources:          sources,
		UnresolvedCycles: d.countUnresolved(network.NetworkID, sources),
		Config:           settings.Ingress,
		Transport:        settings.TLS,
	})
	state.problems = append(state.problems, state.ingress.Problems...)
	if recordErr := d.checkpointInfrastructure(ctx, key, state); recordErr != nil {
		return state, errors.Join(err, recordErr)
	}
	if err != nil || !state.ingress.Ready() {
		return state, err
	}

	state.workload, err = d.Provisioner.EnsureWorkload(ctx, WorkloadInput{
		Key:           key,
		HostNetworkID: network.NetworkID,
		Mode:          d.Mode,
		Prerequisites: state.prerequisites,
		Settings:      settings,
		OpAMPEndpoint: d.OpAMPEndpoint,
		Pin:           d.Pin,
	})
	state.problems = append(state.problems, state.workload.Problems...)
	if recordErr := d.checkpointInfrastructure(ctx, key, state); recordErr != nil {
		return state, errors.Join(err, recordErr)
	}
	if err != nil || !state.workload.Ready() {
		return state, err
	}

	// Read once, before either gated call. `Recorded` is half the deletion gate for the service converge
	// (whose adoption decision turns on it) and for the scaling converge (whose deregistration IS a
	// delete), and reading it twice would let one act on a row the other had already changed.
	recorded, err := d.recordedRefs(ctx, key)
	if err != nil {
		return state, err
	}
	// The row's WORKLOAD ref rather than a ref match, because on the pass that adopts a service there is
	// nothing recorded yet to match against: what authorizes the write is that this connector's own
	// state names a service for this network at all.
	serviceRecorded := recorded.WorkloadRef != ""

	state.service, err = d.Provisioner.EnsureCollectorService(ctx, ServiceStateInput{
		Key:           key,
		HostNetworkID: network.NetworkID,
		Mode:          d.Mode,
		Workload:      state.workload,
		Ingress:       state.ingress,
		Subnets:       state.subnets,
		Settings:      settings,
		Recorded:      serviceRecorded,
	})
	state.problems = append(state.problems, state.service.Problems...)
	if recordErr := d.checkpointInfrastructure(ctx, key, state); recordErr != nil {
		return state, errors.Join(err, recordErr)
	}
	if err != nil {
		return state, err
	}

	state.scaling, err = d.Provisioner.EnsureCollectorScaling(ctx, ScalingStateInput{
		Key:           key,
		HostNetworkID: network.NetworkID,
		Mode:          d.Mode,
		Workload:      state.workload,
		Service:       state.service,
		Settings:      settings,
		Recorded:      serviceRecorded,
	})
	state.problems = append(state.problems, state.scaling.Problems...)
	return state, err
}

// Checkpoint each completed stage before another AWS write can create more
// resources. Every call supplies the cumulative refs, since the store updates
// the infrastructure columns as one group.
func (d *Deployer) checkpointInfrastructure(ctx context.Context, key basestore.CollectorKey, state convergedState) error {
	if state.workload.Cluster.ARN == "" && state.ingress.SecurityGroupID == "" &&
		state.ingress.LoadBalancerARN == "" && state.service.ARN == "" {
		return nil
	}
	previous, err := d.recordedRefs(ctx, key)
	if err != nil {
		return err
	}
	current := InfrastructureRecord(key, state.workload, state.ingress, state.service, state.subnets)
	// A stage may stop before it observes a service or load balancer that was
	// recorded on an earlier pass. An empty result cannot revoke deletion
	// authority over that still-running object.
	if current.WorkloadRef == "" {
		current.WorkloadRef = previous.WorkloadRef
	}
	if current.RevisionRef == "" {
		current.RevisionRef = previous.RevisionRef
	}
	if current.RuntimeRef == "" {
		current.RuntimeRef = previous.RuntimeRef
	}
	if current.FrontendRef == "" {
		current.FrontendRef = previous.FrontendRef
	}
	for _, ref := range previous.BackendRefs {
		if !containsStringValue(current.BackendRefs, ref) {
			current.BackendRefs = append(current.BackendRefs, ref)
		}
	}
	if current.IngressRef == "" {
		current.IngressRef = previous.IngressRef
	}
	if len(current.PlacementRefs) == 0 {
		current.PlacementRefs = previous.PlacementRefs
	}
	if err := d.Store.RecordCollectorInfrastructure(ctx, key, current); err != nil {
		return fmt.Errorf("checkpoint Collector infrastructure in %s: %w", key.Region, err)
	}
	return nil
}

// desiredConfig renders the document the cold-start secret carries, or nothing.
func (d *Deployer) desiredConfig(ctx context.Context, key basestore.CollectorKey) (collectorconfig.Document, error) {
	if d.Desired == nil {
		return collectorconfig.Document{}, nil
	}
	return d.Desired(ctx, key)
}

// recordedRefs reads what this connector already recorded for a network, which is half the deletion
// gate and half the adoption decision.
//
// A row that does not exist yet is not an error: the first pass over a network creates it while
// installing the credential, and everything that reads it here treats absent as "nothing recorded",
// which refuses rather than permits.
func (d *Deployer) recordedRefs(ctx context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error) {
	record, err := d.Store.GetManagedCollector(ctx, key)
	if errors.Is(err, basestore.ErrCollectorNotFound) {
		return basestore.CollectorRecord{}, nil
	}
	if err != nil {
		return basestore.CollectorRecord{}, fmt.Errorf("reading the recorded state of the Collector in %s: %w",
			key.Region, err)
	}
	return record, nil
}

// sharedFor resolves connector-wide prerequisites once per placement generation.
func (d *Deployer) sharedFor(ctx context.Context, plan placement.Plan, settings collectorsettings.Settings, generation uint64) (Shared, error) {
	d.mu.Lock()
	if d.sharedOK && d.sharedGen == generation {
		shared := d.shared
		d.mu.Unlock()
		return shared, nil
	}
	d.mu.Unlock()

	regions := map[string]bool{}
	for _, n := range plan.DeployableNetworks() {
		regions[n.Region] = true
	}
	regionList := make([]string, 0, len(regions))
	for region := range regions {
		regionList = append(regionList, region)
	}

	shared, err := d.Provisioner.EnsureShared(ctx, SharedInput{
		Regions: regionList,
		Mode:    d.Mode,
	})
	if err != nil {
		return shared, fmt.Errorf("converging the connector's shared Collector identity: %w", err)
	}

	d.mu.Lock()
	d.shared, d.sharedGen, d.sharedOK = shared, generation, true
	d.mu.Unlock()
	return shared, nil
}

// countUnresolved tracks how many consecutive passes resolved NO sender security group for a network.
//
// The gate the network-CIDR fallback waits on. Every routed workload has a security group, so zero
// resolved means the inventory is incomplete rather than that the senders have none — and widening a
// Collector's ingress to a whole VPC on the strength of an incomplete read is the one mistake that
// cannot be walked back, because the telemetry that arrived through the widened rule was accepted.
//
// Counted here rather than in the ingress package because it is a fact about consecutive PASSES, and
// that package is pure. Reset rather than decremented on a resolved group: the gate asks "has discovery
// failed every time for three passes", and a decaying count would eventually open the fallback for a
// network whose discovery works on most of them.
func (d *Deployer) countUnresolved(networkID string, sources []awsingress.Source) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unresolvedCycles == nil {
		d.unresolvedCycles = map[string]int{}
	}
	for _, s := range sources {
		if len(s.SecurityGroupIDs) > 0 {
			d.unresolvedCycles[networkID] = 0
			return 0
		}
	}
	d.unresolvedCycles[networkID]++
	return d.unresolvedCycles[networkID]
}

// record writes what the pass produced, by column group.
//
// Infrastructure was checkpointed during convergence. These remaining status,
// endpoint and scaling writes are observational and do not grant delete authority.
func (d *Deployer) record(ctx context.Context, key basestore.CollectorKey,
	settings collectorsettings.Network, state convergedState) {
	if state.ingress.Transport.GRPCEndpoint != "" {
		if err := d.Store.RecordCollectorEndpoint(ctx, key,
			state.ingress.Transport.GRPCEndpoint, state.ingress.Transport.HTTPEndpoint,
			state.ingress.EndpointSource, string(state.ingress.Transport.Mode),
			string(state.ingress.Transport.Termination), string(state.ingress.Tier)); err != nil {
			log.Warn("could not record the Collector's endpoint",
				"region", key.Region, "error", err)
		}
	}
	if state.service.ARN != "" || state.scaling.Mode != "" {
		d.recordScaling(ctx, key, settings, state.service, state.scaling)
	}
	d.escalate(ctx, key, state.problems)
}

// recordScaling writes the mode, both counts, both bounds and the task size together, which is the only
// way that accessor may be called — three callers each filling their own half would null the others'.
func (d *Deployer) recordScaling(ctx context.Context, key basestore.CollectorKey,
	settings collectorsettings.Network, service Service, scaling ScalingState) {
	d.writeScaling(ctx, key, ObservedScaling(settings.Base(), service, scaling))
}

// recordParkedScaling records the policy retention put in force rather than the one configured. The row
// says what is GOVERNING the Collector, and while it is parked that is a manual zero.
func (d *Deployer) recordParkedScaling(ctx context.Context, key basestore.CollectorKey,
	settings collectorsettings.Network, parked collectorsettings.ResolvedScaling, service Service, scaling ScalingState) {
	d.writeScaling(ctx, key, ObservedScalingFor(parked, settings.Base(), service, scaling))
}

func (d *Deployer) writeScaling(ctx context.Context, key basestore.CollectorKey, observed ScalingObservation) {
	if err := d.Store.RecordCollectorScaling(ctx, key, observed.Mode,
		observed.DesiredCount, observed.RunningCount, observed.MinCount, observed.MaxCount,
		observed.CPU, observed.Memory); err != nil {
		log.Warn("could not record the Collector's scaling state",
			"region", key.Region, "error", err)
	}
}

// escalate records a converge failure on the row, and ONLY a failure.
//
// The status column has writers on three planes and they divide it by what each can see: the OpAMP
// session knows whether the Collector accepted its configuration and is the only party that may say
// Ready, the observer knows whether the infrastructure is serving, and this knows whether the converge
// could be performed at all — "no image is pinned in this connector build", "the service name is taken
// by something Odigos may not touch" — which neither of the others can discover.
//
// So this only ever makes the status worse, never better: it writes Error or Degraded and nothing else.
// A converge that succeeded says nothing, because "the writes went through" is not evidence the
// Collector is running, and claiming Ready from here would clear a finding the observer had just made.
func (d *Deployer) escalate(ctx context.Context, key basestore.CollectorKey, problems []collectorstatus.Problem) {
	status := collectorstatus.StatusFor(problems, "")
	if status != collectorstatus.StatusError && status != collectorstatus.StatusDegraded {
		return
	}
	if err := d.Store.SetCollectorStatus(ctx, key, status, worstMessage(problems)); err != nil &&
		!errors.Is(err, basestore.ErrCollectorNotFound) {
		log.Warn("could not record a Collector converge failure",
			"region", key.Region, "status", status, "error", err)
	}
}

// worstMessage is the one sentence a customer reads first: the first Error, or failing that the first
// Degraded, with a count of whatever else there was.
//
// One rather than all of them, because a status message that concatenates every condition is a message
// nobody finishes reading — and the specific cause is always the worst one. The count exists so a reader
// knows to look at the full report rather than believing this is everything.
func worstMessage(problems []collectorstatus.Problem) string {
	for _, want := range []collectorstatus.Severity{collectorstatus.SeverityError, collectorstatus.SeverityDegraded} {
		for i, p := range problems {
			if p.Severity != want {
				continue
			}
			if others := len(problems) - i - 1; others > 0 {
				return fmt.Sprintf("%s (and %d further condition(s))", p.Message, others)
			}
			return p.Message
		}
	}
	return ""
}

// retire applies the retention policy to a network whose last enabled source was removed.
//
// Nothing here converges: there is no configuration to converge a Collector towards on a network with no
// sources, and the only question left is how long to keep it. Both graces default to KEEP, so the
// overwhelmingly common outcome of this function is a report and no action at all.
//
// The clock starts on the FIRST pass that finds a network idle and is never restarted by a later one —
// see basestore.RecordCollectorIdle. A pass that started the clock therefore acts on nothing, which is
// what stops a connector restart, or one incomplete discovery cycle, from deleting a Collector.
func (d *Deployer) retire(ctx context.Context, key basestore.CollectorKey, network baseapi.CollectorNetwork) error {
	record, err := d.Store.GetManagedCollector(ctx, key)
	if errors.Is(err, basestore.ErrCollectorNotFound) {
		// Nothing recorded, so nothing to retire. Unreachable through Plan.Idle, which is built FROM the
		// recorded rows, and cheap to be right about anyway.
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the recorded state of the idle Collector in %s: %w", key.Region, err)
	}

	settings := d.Settings().Region(key.Region).Network
	retention := settings.Retention.Resolve()
	now := d.now()

	if record.IdleSince == nil {
		if err := d.Store.RecordCollectorIdle(ctx, key, &now); err != nil {
			return fmt.Errorf("starting the retention clock for the Collector in %s: %w", key.Region, err)
		}
		d.reportIdle(ctx, key, retention, nil)
		return nil
	}

	switch retention.Decide(*record.IdleSince, now) {
	case collectorsettings.RetentionDelete:
		log.Info("deleting an idle collector",
			"network", record.NetworkID, "region", key.Region,
			"idle_since", record.IdleSince, "grace", retention.DeleteAfter)
		return d.TeardownCollector(ctx, network)
	case collectorsettings.RetentionScaleToZero:
		return d.park(ctx, key, record, settings, retention)
	default:
		d.reportIdle(ctx, key, retention, record.IdleSince)
		return nil
	}
}

// RetireDisabledCollector reaches the same retention policy when a regional
// declaration is disabled. No provisioning path is entered.
func (d *Deployer) RetireDisabledCollector(ctx context.Context, network baseapi.CollectorNetwork) error {
	return d.retire(ctx, basestore.CollectorKey{Region: network.Region}, network)
}

// clearIdle stops the retention clock for a network that has sources again.
//
// Cleared rather than paused. The graces are about how long a Collector has been unused CONTINUOUSLY, so
// resuming a half-elapsed clock would let a network that is used every day accumulate its way to a
// deletion — and a Collector that has been carrying telemetry all week is the last thing that should be
// eligible for one.
//
// Called on every converge rather than only on the transition, because there is no transition to hook:
// nothing records that the previous pass found this network idle except the column itself, and the write
// is a no-op when it is already NULL.
func (d *Deployer) clearIdle(ctx context.Context, key basestore.CollectorKey) {
	if err := d.Store.RecordCollectorIdle(ctx, key, nil); err != nil &&
		!errors.Is(err, basestore.ErrCollectorNotFound) {
		log.Warn("could not clear the retention clock for a Collector that has sources again",
			"region", key.Region, "error", err)
	}
}

// park scales an idle Collector to zero tasks and leaves everything else standing.
//
// Two steps, and the order is the safety property: the scalable target is removed FIRST. Application
// Auto Scaling holds `desiredCount` in `auto` mode, so setting the count to zero while a target
// was still registered would be undone within a minute by a policy reacting to a CPU metric that no
// longer exists — the Collector would flap between zero and the minimum for as long as it stayed idle.
//
// Both steps run against refs from the ROW rather than from a placement decision, because an idle
// network has none. That is also why neither step can create anything: ParkService converges a service
// it finds and builds none, and EnsureScaling with a manual policy only ever removes a target.
func (d *Deployer) park(ctx context.Context, key basestore.CollectorKey, record basestore.CollectorRecord,
	settings collectorsettings.Network, retention collectorsettings.ResolvedRetention) error {
	if record.WorkloadRef == "" || record.RuntimeRef == "" {
		// Nothing to park. A row with no service is a Collector that was never built, and it is already
		// costing nothing.
		d.reportIdle(ctx, key, retention, record.IdleSince)
		return nil
	}
	// Resolved ONCE and handed over, never round-tripped through Settings. A parked policy put back into
	// a Scaling and re-resolved is lifted off zero by ReplicaFloor and reported as a correction — the
	// exact trap ResolvedScaling exists to prevent, and the reason the override below is a resolved value
	// rather than a configuration.
	parked := settings.Scaling.Resolve().Park()

	scaling, err := d.Provisioner.EnsureCollectorScaling(ctx, ScalingStateInput{
		Key:           key,
		HostNetworkID: record.NetworkID,
		Mode:          d.Mode,
		Workload:      Workload{Cluster: Cluster{ARN: record.RuntimeRef, Name: clusterNameOf(record.RuntimeRef)}},
		Service:       Service{ARN: record.WorkloadRef, Name: ServiceName(key.Region)},
		Settings:      settings,
		Resolved:      &parked,
		Recorded:      true,
	})
	if err != nil {
		return fmt.Errorf("removing the autoscaler from the idle Collector in %s: %w", key.Region, err)
	}

	api, err := d.Provisioner.Clients.ECS(ctx, key.Region)
	if err != nil {
		return fmt.Errorf("build an ECS client for %s: %w", key.Region, err)
	}
	service, problems, err := ParkService(ctx, api, ParkInput{
		Region:        key.Region,
		HostNetworkID: record.NetworkID,
		Identity:      d.Provisioner.Identity,
		Mode:          d.Mode,
		ClusterARN:    record.RuntimeRef,
		Recorded:      record.Recorded(record.WorkloadRef),
	})
	if err != nil {
		return fmt.Errorf("scaling the idle Collector in %s to zero: %w", key.Region, err)
	}

	d.recordParkedScaling(ctx, key, settings, parked, service, scaling)

	// Info, and said on every pass for as long as it applies: a Collector sitting at zero tasks that
	// nobody explained reads as a broken deployment, and whoever looks at it later was not there when it
	// was parked.
	problems = append(problems, scaling.Problems...)
	problems = append(problems, collectorstatus.Problem{
		Code:     ProblemRetentionParked,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("region %s has no enabled sources, so its Collector is scaled to zero and its "+
			"infrastructure in %s is kept: %s. Enabling a source in that region brings it back without "+
			"redeploying anything", key.Region, record.NetworkID, retention.Describe()),
	})
	d.escalate(ctx, key, append(problems, correctionProblems(retention)...))
	return nil
}

// reportIdle states what will happen to an idle Collector, and when.
func (d *Deployer) reportIdle(ctx context.Context, key basestore.CollectorKey,
	retention collectorsettings.ResolvedRetention, since *time.Time) {
	if retention.Keeps() {
		// The default, and it is not a condition: a Collector kept on a network with no sources is doing
		// exactly what it was configured to do. Reporting it as anything would put a standing finding on
		// every network a customer ever turned off.
		d.escalate(ctx, key, correctionProblems(retention))
		return
	}
	idle := "since this pass"
	if since != nil {
		idle = "since " + since.UTC().Format(time.RFC3339)
	}
	d.escalate(ctx, key, append([]collectorstatus.Problem{{
		Code:     ProblemRetentionParked,
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("region %s has had no enabled sources %s, and %s. Its Collector keeps running "+
			"and exporting until then", key.Region, idle, retention.Describe()),
	}}, correctionProblems(retention)...))
}

// correctionProblems reports a retention value that could not be used.
func correctionProblems(retention collectorsettings.ResolvedRetention) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	for _, correction := range retention.Corrections {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemRetentionPolicy,
			Severity: collectorstatus.SeverityDegraded,
			Message:  correction,
		})
	}
	return problems
}

// TeardownCollector removes what Odigos created for one network, credentials first.
//
// Reached from two places, and the only difference between them is WHEN: the shutdown drain calls it
// when the connector is deleted, and `retire` calls it when a network has been idle past its delete
// grace. Both go through the same per-object ownership gate, because idleness decides when a teardown
// runs and never what it is allowed to remove.
func (d *Deployer) TeardownCollector(ctx context.Context, network baseapi.CollectorNetwork) error {
	key := basestore.CollectorKey{Region: network.Region}
	settings := d.Settings().Region(network.Region).Network

	result, err := Teardown(ctx, d.Teardown, d.Store, TeardownInput{
		Key:                      key,
		NetworkID:                network.NetworkID,
		Mode:                     d.Mode,
		Reason:                   collectorcredential.ReasonTeardown,
		Identity:                 d.Provisioner.Identity,
		Partition:                Partition(network.Region),
		AccountID:                d.Provisioner.AccountID,
		CustomerSecurityGroupIDs: settings.SecurityGroupIDs,
	})

	// Only while the row is still there. A completed teardown deleted it, and every row writer refuses a
	// row that does not exist rather than inventing one — which is what keeps a teardown's own report
	// from resurrecting the Collector it just removed.
	if !result.RecordRemoved && !errors.Is(err, baseapi.ErrCollectorTeardownRetry) {
		d.escalate(ctx, key, result.Problems)
	}
	return err
}

// now is the retention clock, defaulting to the wall clock.
func (d *Deployer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}
