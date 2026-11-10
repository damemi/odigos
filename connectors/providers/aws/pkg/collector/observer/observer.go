// Package observer reads back what is actually deployed for an AWS managed Collector and records a
// verdict on it, in BOTH modes.
//
// It never writes to a customer's AWS account. That is the package's whole reason for existing
// separately from the deployer: in manual (IaC) mode this is the only interface a Collector is reached
// through, the infrastructure is the customer's, and Odigos configures and reports it without touching
// it. The client interfaces here hold nothing but Describes and Lists, and readonly_test.go fails if a
// mutating verb appears anywhere in the package.
//
// What it produces is a DIAGNOSIS rather than a health bit. is a table of
// failure modes, and every row of it exists to produce one specific sentence — "the task cannot pull
// ghcr.io/…@sha256:… and this subnet has no egress route to the registry" instead of "the Collector is
// unhealthy" — because an unspecific message is the failure a customer actually hits. The classification
// is pure (Diagnose, ClassifyStop, ClassifyAgentHealth) so each row is a value a test writes down.
//
// # Which rows this pass writes
//
// The row's status is shared with the OpAMP session, and the two see different planes: the session knows
// whether the Collector accepted its configuration and this package knows whether its infrastructure is
// serving, and neither can reconstruct the other's finding. So the division is by what each can see —
// this package ESCALATES and never clears while a session is live, and the session is the party that
// clears, because it is the one that can see the plane a "Ready" claims about.
//
// The infrastructure, endpoint, image and scaling columns are written HERE ONLY IN MANUAL MODE. In
// automatic mode the converge pass writes the same objects and additionally the ones this package cannot
// read — the ingress tier it resolved, the scalable target's bounds — so it owns those rows and this one
// leaves them alone. Two writers on a multi-column accessor is how half a row gets nulled.
package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var log = connlog.Subsystem("aws-collector-observer")

// Store is the slice of the managed-Collector store this package uses.
//
// Narrow on purpose, and every write in it is a record of something that was READ. There is no accessor
// here that deletes a row: a Collector that has disappeared from AWS is a finding to report, not a row
// to remove — the row is what carries the report, and the refs in it are half of teardown's authority
// over the objects that are still there.
type Store interface {
	GetManagedCollector(ctx context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error)
	UpsertManagedCollector(ctx context.Context, key basestore.CollectorKey, networkID, kind, mode, origin string, adoptedAt *time.Time) error
	RecordCollectorInfrastructure(ctx context.Context, key basestore.CollectorKey, rec basestore.CollectorRecord) error
	RecordCollectorEndpoint(ctx context.Context, key basestore.CollectorKey, grpc, http, source, tlsMode, tlsTermination, ingressTier string) error
	RecordCollectorImage(ctx context.Context, key basestore.CollectorKey, digest, tag, arch, licenseSecretRef string, licenseExpiresAt *time.Time) error
	RecordCollectorScaling(ctx context.Context, key basestore.CollectorKey, mode string, desired, running, minCount, maxCount *int, cpu, memory string) error
	SetCollectorStatus(ctx context.Context, key basestore.CollectorKey, status collectorstatus.Status, message string) error
}

// Planner supplies the placement decision for a pass.
//
// An interface rather than the discovery itself, because deciding WHICH networks need a Collector is
// placement's answer and it depends on the workload inventory — which this package has no
// business reading. The deployer needs the same decision, so whoever wires the two resolves it once.
type Planner interface {
	CollectorPlan(ctx context.Context) (placement.Plan, error)
}

// Clients supplies the read-only AWS clients, per region.
type Clients interface {
	ECS(ctx context.Context, region string) (ECSReader, error)
	LoadBalancers(ctx context.Context, region string) (ELBReader, error)
}

// Observer implements api.CollectorObserver for AWS.
type Observer struct {
	// Identity is this connector, as collectorownership reads it. Used to classify what was found, never
	// to decide whether to write — nothing here writes to AWS.
	Identity collectorownership.Identity
	// Kind is the collectors type (aws.fargate-collector).
	Kind string
	// Mode is the deployment mode for that type. It changes what a finding means and never what is read:
	// a manual-mode Collector is described exactly as thoroughly as an automatic one, because Odigos
	// owns its configuration either way.
	Mode string
	// OpAMPEndpoint is what this installation publishes now, for the drift check against the endpoint
	// each Collector was deployed with.
	OpAMPEndpoint string
	// Pin is the image this connector build expects, for the drift check against what is running.
	Pin collectorimage.Pin
	// HeartbeatInterval sizes the OpAMP staleness window. Read through collectorreport so this and the
	// customer-facing report cannot disagree about what "stale" means.
	HeartbeatInterval time.Duration

	// Settings reads the live tree. A function rather than a value because an updateConfig patch
	// has to land on the next pass with no restart.
	Settings func() collectorsettings.Settings
	// Regions lists enabled AWS regions for discovering customer-created services in manual mode.
	Regions func(context.Context) ([]string, error)

	Planner Planner
	Store   Store
	Clients Clients

	// Now is the clock, injectable for tests.
	Now func() time.Time

	// plan is the placement decision from this pass's CollectorNetworks call, so ObserveCollector can
	// report a network's placement refusal against its own row — a network with no usable subnet has
	// nowhere else to be reported.
	mu   sync.Mutex
	plan *placement.Plan
}

// Observer satisfies the read-only half of the provider Collector surface. Asserted here so wiring it
// is a compile error away rather than a runtime surprise.
var _ baseapi.CollectorObserver = (*Observer)(nil)

// CollectorNetworks lists the networks that should have a Collector.
//
// Placement's answer, unfiltered — including a network whose placement was REFUSED. A refusal has to be
// reported against a Collector row, and a network dropped from this list has no row to report it on,
// which turns "this VPC has no subnet a Collector could export from" into silence.
func (o *Observer) CollectorNetworks(ctx context.Context) ([]baseapi.CollectorNetwork, error) {
	plan, err := o.Planner.CollectorPlan(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving which networks need a Collector: %w", err)
	}
	o.mu.Lock()
	o.plan = &plan
	o.mu.Unlock()
	networks := plan.CollectorNetworks()
	if o.Mode != spec.ModeManual || o.Regions == nil {
		return networks, nil
	}
	return o.discoverExistingCollectors(ctx, networks), nil
}

// discoverExistingCollectors finds services created from the manual instructions before their region
// has been declared in the UI. It does not add them to placement, so discovery cannot deploy or route
// workloads into an unconfigured network. The service name and ownership tags must both match.
func (o *Observer) discoverExistingCollectors(ctx context.Context, planned []baseapi.CollectorNetwork) []baseapi.CollectorNetwork {
	regions, err := o.Regions(ctx)
	if err != nil {
		log.Warn("could not list regions while discovering customer Collectors", "error", err)
		return planned
	}
	known := make(map[string]bool, len(planned))
	for _, network := range planned {
		known[network.Region] = true
	}
	for _, region := range regions {
		if known[region] {
			continue
		}
		api, err := o.Clients.ECS(ctx, region)
		if err != nil {
			log.Warn("could not create ECS client while discovering customer Collectors", "region", region, "error", err)
			continue
		}
		var found []baseapi.CollectorNetwork
		var next *string
		for {
			page, err := api.ListClusters(ctx, &ecs.ListClustersInput{NextToken: next})
			if err != nil {
				log.Warn("could not list ECS clusters while discovering customer Collectors", "region", region, "error", err)
				found = nil
				break
			}
			for _, cluster := range page.ClusterArns {
				service, err := describeService(ctx, api, cluster, deployer.ServiceName(region))
				if err != nil {
					log.Warn("could not inspect a potential customer Collector", "region", region, "cluster", cluster, "error", err)
					continue
				}
				if service == nil || aws.ToString(service.Status) != "ACTIVE" {
					continue
				}
				tags := baseapi.ParseCollectorTags(ecsTagMap(service.Tags))
				if tags.Region != region || !strings.HasPrefix(tags.Network, "vpc-") ||
					!collectorownership.AttributionOf(tags, o.Identity).Mine() {
					continue
				}
				found = append(found, baseapi.CollectorNetwork{
					Region: region, NetworkID: tags.Network, ClusterRef: cluster, Kind: o.Kind,
				})
			}
			if page.NextToken == nil || aws.ToString(page.NextToken) == "" {
				break
			}
			next = page.NextToken
		}
		if len(found) == 1 {
			planned = append(planned, found[0])
		} else if len(found) > 1 {
			log.Warn("multiple customer Collectors found in one region; configure the intended cluster explicitly", "region", region)
		}
	}
	sort.Slice(planned, func(i, j int) bool { return planned[i].Region < planned[j].Region })
	return planned
}

// ObserveCollector reads one network's Collector and records what it found.
//
// Returns an error only for a failure that made the pass meaningless — an unavailable client, or a store
// that would not answer. Everything it FOUND, including what it could not read, is reported on the row
// instead: the coordinator logs an error from here and carries on, so an error is the weakest of the two
// channels and the row is the one a customer sees.
func (o *Observer) ObserveCollector(ctx context.Context, network baseapi.CollectorNetwork) error {
	key := basestore.CollectorKey{Region: network.Region}
	configured := o.settings()
	settings := configured.Region(network.Region).Network

	record, err := o.Store.GetManagedCollector(ctx, key)
	if err != nil && !errors.Is(err, basestore.ErrCollectorNotFound) {
		return fmt.Errorf("reading the recorded state of the Collector on %s: %w", network.NetworkID, err)
	}
	recorded := err == nil

	ecsAPI, err := o.Clients.ECS(ctx, network.Region)
	if err != nil {
		return fmt.Errorf("build an ECS client for %s: %w", network.Region, err)
	}
	elbAPI, err := o.Clients.LoadBalancers(ctx, network.Region)
	if err != nil {
		return fmt.Errorf("build an Elastic Load Balancing client for %s: %w", network.Region, err)
	}

	clusterRef := settings.ClusterRef
	if _, declared := configured.Regions[network.Region]; !declared && recorded && record.RuntimeRef != "" {
		clusterRef = record.RuntimeRef
	}
	if network.ClusterRef != "" {
		clusterRef = network.ClusterRef
	}
	observation := Observe(ctx, ecsAPI, elbAPI, ObserveInput{
		Region:     network.Region,
		NetworkID:  network.NetworkID,
		ClusterRef: clusterRef,
	})

	diagnosis := Diagnose(DiagnosisInput{
		Network:       network,
		Connector:     o.Identity.ConnectorName,
		Mode:          o.Mode,
		Settings:      settings,
		Placement:     append(o.placementProblems(network), o.manualSecurityGroupProblems(network, observation)...),
		Record:        record,
		Observation:   observation,
		Liveness:      collectorreport.LivenessOf(record.OpAMPLastSeenAt, o.HeartbeatInterval, o.now()),
		OpAMPEndpoint: o.OpAMPEndpoint,
		Pin:           o.Pin,
	})

	if err := o.record(ctx, key, network, record, recorded, observation, diagnosis); err != nil {
		return err
	}

	log.Debug("observed a collector",
		"region", network.Region, "network", network.NetworkID,
		"found", observation.ServiceFound, "running", observation.RunningCount,
		"desired", observation.DesiredCount, "problems", len(diagnosis.Problems),
		"status", string(diagnosis.Status))
	return nil
}

func (o *Observer) manualSecurityGroupProblems(network baseapi.CollectorNetwork, observation Observation) []collectorstatus.Problem {
	if o.Mode != spec.ModeManual || !observation.ServiceFound || len(observation.TargetGroups) == 0 {
		return nil
	}
	o.mu.Lock()
	plan := o.plan
	o.mu.Unlock()
	if plan == nil {
		return nil
	}
	unknown := false
	for _, groupID := range observation.SecurityGroups {
		switch plan.Topology.CollectorSecurityGroupAllowsSelfHealth(network.Region, network.NetworkID, groupID) {
		case placement.ReachabilityReachable:
			return nil
		case placement.ReachabilityUnknown:
			unknown = true
		}
	}
	if unknown || len(observation.SecurityGroups) == 0 {
		return nil
	}
	return []collectorstatus.Problem{{
		Code: ProblemSecurityGroupInvalid, Severity: collectorstatus.SeverityError,
		Message: "The customer-owned Collector security group must allow TCP 13133 from itself so the internal NLB can health-check ECS tasks.",
	}}
}

// record writes what this pass established.
func (o *Observer) record(ctx context.Context, key basestore.CollectorKey, network baseapi.CollectorNetwork,
	previous basestore.CollectorRecord, recorded bool, observation Observation, diagnosis Diagnosis) error {
	// Tags are the sole authority for origin. An absent service has no tags and is not a discovery: if
	// this is the first pass, leave row creation to the provisioning path, which records OriginOdigos
	// before it creates anything. Otherwise a connector restart can race provision and permanently mark
	// connector-created infrastructure as customer-owned, preventing a later teardown.
	if !recorded && !observation.ServiceFound {
		return nil
	}

	// Every other accessor updates an existing row and does nothing to a missing one. Once a service is
	// found, its tags establish ownership on INSERT. When a previously recorded service is temporarily
	// absent, retain the already-established origin rather than deriving ownership from empty tags.
	origin := previous.Origin
	if observation.ServiceFound {
		origin = string(collectorownership.OriginOf(baseapi.ParseCollectorTags(observation.Tags)))
	}
	if err := o.Store.UpsertManagedCollector(ctx, key, network.NetworkID, kindOr(network.Kind, o.Kind), o.Mode, origin, nil); err != nil {
		return err
	}

	if o.Mode == spec.ModeManual {
		if err := o.recordDiscovered(ctx, key, previous, observation, diagnosis); err != nil {
			return err
		}
	}

	if !diagnosis.Records() {
		// Nothing to say. Every finding was Info at worst and the configuration plane is live, so the
		// session's own verdict stands — see writesStatus.
		return nil
	}
	if !o.writesStatus(previous, diagnosis) {
		return nil
	}
	return o.Store.SetCollectorStatus(ctx, key, diagnosis.Status, diagnosis.Message)
}

// recordDiscovered writes the columns only a manual-mode observation can fill.
//
// Manual mode only, and the asymmetry is the point rather than a shortcut. Nothing else looks at a
// Collector the customer deployed, so without this its row would hold no endpoint — and no endpoint
// means no workload is ever pointed at it, which is the whole of. In automatic mode the converge
// pass writes these same columns from what it built, and additionally the two things this package cannot
// read: the ingress tier it resolved and the scalable target's bounds. Writing them from
// here would null those.
func (o *Observer) recordDiscovered(ctx context.Context, key basestore.CollectorKey,
	previous basestore.CollectorRecord, observation Observation, diagnosis Diagnosis) error {
	if observation.ServiceFound {
		// Only when a service was found, and this is not an optimisation. Writing empty refs over a
		// Collector whose describe came back empty would drop teardown's authority over the objects that
		// ARE still there — the load balancer, the security group — and leak them silently.
		if err := o.Store.RecordCollectorInfrastructure(ctx, key, basestore.CollectorRecord{
			WorkloadRef:   observation.ServiceARN,
			RevisionRef:   observation.RevisionARN,
			RuntimeRef:    observation.ClusterARN,
			FrontendRef:   observation.LoadBalancer.ARN,
			BackendRefs:   targetGroupARNs(observation.TargetGroups),
			IngressRef:    firstOr(observation.SecurityGroups),
			PlacementRefs: observation.Subnets,
		}); err != nil {
			return err
		}
		if err := o.Store.RecordCollectorImage(ctx, key,
			observation.Revision.Digest, observation.Revision.Tag, observation.Revision.Architecture,
			previous.LicenseSecretRef, previous.LicenseExpiresAt); err != nil {
			return err
		}
		desired, running := observation.DesiredCount, observation.RunningCount
		if err := o.Store.RecordCollectorScaling(ctx, key,
			// The counts are this pass's; the BOUNDS are carried through from the row untouched. This
			// accessor writes all seven fields together, so a caller that knows only some of them has to
			// re-write the rest or null them — and bounds nulled here would report a Collector as
			// unbounded when nothing changed but the observation.
			previous.ScalingMode, &desired, &running, previous.MinCount, previous.MaxCount,
			observation.Revision.CPU, observation.Revision.Memory); err != nil {
			return err
		}
	}

	if diagnosis.Transport.GRPCEndpoint == "" {
		// Nothing to publish yet. Recording an empty endpoint would un-publish a Collector that is
		// merely unreadable this pass, and every workload would be re-pointed away from a working one.
		return nil
	}
	return o.Store.RecordCollectorEndpoint(ctx, key,
		diagnosis.Transport.GRPCEndpoint, diagnosis.Transport.HTTPEndpoint, diagnosis.EndpointSource,
		string(diagnosis.Transport.Mode), string(diagnosis.Transport.Termination),
		// The ingress tier is 'none' and that is the honest value, not a placeholder: Odigos resolved no
		// ingress rule for this Collector because the network rules in front of it are the customer's.
		string(collectorIngressTierNone))
}

// collectorIngressTierNone is spelled out rather than imported as the ingress package's constant to keep
// the store's CHECK constraint and this value in one place in the reader's eye; they are the same string.
const collectorIngressTierNone = "none"

// writesStatus decides whether this pass's verdict replaces what is recorded.
//
// The rule is one sentence: the observer escalates, and it never clears a verdict it cannot re-derive.
//
// The reason is that two writers share this column and see different planes. The OpAMP session records
// the Collector's own rejection of a configuration, with the agent's validation error verbatim — and
// nothing this package reads can reconstruct that, so overwriting it with "the infrastructure looks
// fine" would delete the only copy of the most specific sentence anybody wrote about that Collector.
// Going the other way is safe: a finding worse than what is recorded is worth replacing it with, because
// a Collector whose tasks cannot start is not adequately described by a configuration complaint.
//
// When the session is NOT live there is no other writer, so this pass owns the column outright and may
// move the status in either direction — which is what lets a repaired Collector stop being reported as
// broken.
func (o *Observer) writesStatus(previous basestore.CollectorRecord, diagnosis Diagnosis) bool {
	if previous.Status == diagnosis.Status && previous.Message == diagnosis.Message {
		// Identical verdict. Skipped so a converged Collector costs no write per pass, the same property
		// every converge half in the deployer holds.
		return false
	}
	if collectorreport.LivenessOf(previous.OpAMPLastSeenAt, o.HeartbeatInterval, o.now()) !=
		collectorreport.LivenessLive {
		return true
	}
	return statusRank(diagnosis.Status) < statusRank(previous.Status)
}

// statusRank orders statuses by how much they demand attention, worst first. Only used to compare this
// pass's verdict with a recorded one; the customer-facing status is collectorreport's to decide.
func statusRank(status collectorstatus.Status) int {
	switch status {
	case collectorstatus.StatusError:
		return 0
	case collectorstatus.StatusDegraded:
		return 1
	case collectorstatus.StatusAwaitingCustomer:
		return 2
	case collectorstatus.StatusDeploying:
		return 3
	case collectorstatus.StatusPending:
		return 4
	case collectorstatus.StatusReady:
		return 5
	}
	// An unrecorded status is the weakest thing there is, so anything replaces it.
	return 6
}

// placementProblems returns this network's conditions from the pass's plan.
func (o *Observer) placementProblems(network baseapi.CollectorNetwork) []collectorstatus.Problem {
	o.mu.Lock()
	plan := o.plan
	o.mu.Unlock()
	if plan == nil {
		return nil
	}

	var problems []collectorstatus.Problem
	// The account-level conditions travel with every network: a Lambda with no VPC or a workload
	// whose network could not be resolved is not attributable to one Collector, and reporting it on none
	// of them is how it goes unseen.
	problems = append(problems, plan.Problems...)
	problems = append(problems, plan.Networks.Problems...)
	ref := collectorplacement.NetworkRef{Region: network.Region, NetworkID: network.NetworkID}
	if p, ok := plan.Placements[ref.Key()]; ok {
		problems = append(problems, p.Problems...)
	}
	return problems
}

func (o *Observer) settings() collectorsettings.Settings {
	if o.Settings == nil {
		return collectorsettings.Settings{}
	}
	return o.Settings()
}

func (o *Observer) now() time.Time {
	if o.Now == nil {
		return time.Now().UTC()
	}
	return o.Now()
}

func kindOr(kind, fallback string) string {
	if kind != "" {
		return kind
	}
	return fallback
}

func targetGroupARNs(groups []TargetGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g.ARN != "" {
			out = append(out, g.ARN)
		}
	}
	return out
}

func firstOr(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
