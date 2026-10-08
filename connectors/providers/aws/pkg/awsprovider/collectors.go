package awsprovider

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/opamp"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/observer"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

// The managed-Collector surface, and the thing to understand about it is that ONE object is registered
// twice through two interfaces, and the split is the mutation gate.
//
// `CollectorObservers` is wired for every collectors type the customer enabled, in both modes, because
// Odigos owns a Collector's configuration whether or not it deployed it. `CollectorDeployers` is wired
// only for a type in spec.ActiveCollectorDeploymentTypes — automatic mode — so a manual-mode Collector
// reaches no mutating interface anywhere in the runtime, including during the shutdown drain. That is
// stronger than a mode check inside a deployer, because there is no code path to forget the check in.

// collectorType is the one collectors type this provider implements. Named rather than repeated so the
// observer, the deployer and the capability lookup cannot disagree about a spelling.
const collectorType = "aws.fargate-collector"

var (
	_ baseapi.CollectorProvider = (*Provider)(nil)
	_ deployer.Plans            = (*placement.Resolver)(nil)
	_ observer.Planner          = (*placement.Resolver)(nil)
	// opamp.Desired is asserted here rather than left to a type switch at startup, because both places
	// that look for it degrade QUIETLY when it is absent: base.Run logs a warning and starts no OpAMP
	// server, and the coordinator refuses to wire any deployer. A signature that drifted would turn the
	// whole managed-Collector feature off with two log lines and no failure.
	_ opamp.Desired = (*Provider)(nil)
)

// CollectorObservers implements baseapi.CollectorProvider: read-only observation, in both modes.
func (p *Provider) CollectorObservers() map[string]baseapi.CollectorObserver {
	c := p.collectors()
	if c == nil {
		return nil
	}
	return map[string]baseapi.CollectorObserver{collectorType: c.observer}
}

// CollectorDeployers implements baseapi.CollectorProvider: convergence and teardown, automatic mode only.
//
// Returned for the type unconditionally; the coordinator is what intersects this with the customer's
// capability, and it is deliberately the party that decides — a provider that decided for itself would
// be re-implementing the mode gate in a second place.
func (p *Provider) CollectorDeployers() map[string]baseapi.CollectorDeployer {
	c := p.collectors()
	if c == nil {
		return nil
	}
	return map[string]baseapi.CollectorDeployer{collectorType: c.deployer}
}

// DesiredConfig implements opamp.Desired: what this Collector should be running.
//
// Rendered from this Collector's own destinations and this network's settings, and rendered HERE rather
// than in the deployer so there is exactly one renderer. The OpAMP server pushes what this returns and
// the deployer writes what this returns into the cold-start bootstrap secret; two renderers would
// mean a Collector that comes up during an outage serving a configuration that was never published, and
// nothing about it would look wrong.
func (p *Provider) DesiredConfig(ctx context.Context, key basestore.CollectorKey) (collectorconfig.Document, error) {
	settings := collectorsettings.Read(p.cfgMgr.Effective()).Region(key.Region).Network

	memory, err := settings.MemoryMiB()
	if err != nil {
		// Refused rather than guessed. memory_limiter is derived from the allocation, and a
		// Collector whose limiter was sized from an invented number either drops telemetry it had room
		// for or is OOM-killed by the platform.
		return collectorconfig.Document{}, fmt.Errorf("sizing the Collector in %s: %w", key.Region, err)
	}

	// The Collector's own destinations, not the workload destinations: those say where workloads send
	// (possibly to this Collector), and are configured independently of where it sends on.
	destinations, err := p.bstore.ListCollectorDestinations(ctx, key.Region)
	if err != nil {
		return collectorconfig.Document{}, fmt.Errorf("listing destinations for the Collector in %s: %w",
			key.Region, err)
	}

	document, problems := collectorconfig.Render(collectorconfig.Input{
		// Empty is legal and renders an export-less document rather than nothing: a Collector is not
		// gated on having a destination, which is what lets one be added later as a configuration push
		// instead of a redeployment.
		Destinations: destinations,
		Region:       key.Region,
		MemoryMiB:    memory,
		TLSMode:      settings.TLS.Mode,
		// "ecs" alone, plus the "env" this package always adds. The detector names are provider
		// knowledge and collectorconfig holds none.
		ResourceDetectors: []string{"ecs"},
	})
	if collectorstatus.HasError(problems) {
		return collectorconfig.Document{}, fmt.Errorf("rendering the Collector configuration for %s: %s",
			key.Region, problems[0].Message)
	}
	// Rendered anyway, and said out loud: a destination left out of the document (one that stopped
	// rendering after an upgrade) is otherwise indistinguishable from one that exports and is ignored.
	for _, problem := range problems {
		awslog.Warn("Collector configuration rendered with a problem",
			"region", key.Region, "code", problem.Code, "message", problem.Message)
	}
	return document, nil
}

// collectors builds the observer, the deployer and the plan resolver they share, once.
//
// Once because the resolver is stateful in the way that matters: it resolves the placement decision on
// the observer's CollectorNetworks call and serves it to the deployer for the rest of the pass, so two
// resolvers would mean the observer reporting one plan and the deployer converging another.
//
// Nil when the connector has no base store, which is the shape a provider built for a test has.
func (p *Provider) collectors() *collectorSurface {
	p.collectorOnce.Do(func() {
		if p.bstore == nil {
			return
		}
		if !p.cfg.PlatformIDFromCR {
			// No CR UID, so there is no unique connector identity to tag a customer's objects with.
			//
			// Refused rather than run with the ConnectorName fallback, which base/config documents as
			// "safe for routing and dangerous for identity": `odigos-connector-id` would carry a name two
			// clusters can share, so one cluster's connector would read the other's Collectors as its own
			// — and the ownership tag is the licence to DELETE them. A local run therefore gets no managed
			// Collector at all, which costs a developer a feature and cannot cost anyone their
			// infrastructure.
			awslog.Warn("managed Collectors are disabled: this connector has no CR UID, so its objects "+
				"could not be tagged with an identity unique to it",
				"connector", p.cfg.ConnectorName)
			return
		}
		identity := collectorownership.Identity{
			ConnectorName: p.cfg.ConnectorName,
			ConnectorID:   p.cfg.PlatformID,
		}
		mode := p.cfg.Capabilities.CollectorMode(collectorType)
		settings := func() collectorsettings.Settings {
			return collectorsettings.Read(p.cfgMgr.Effective())
		}

		resolver := &placement.Resolver{
			Kind:       collectorType,
			Cache:      p.cache,
			Workloads:  p.workloadStdb,
			Sources:    p.bstore,
			Collectors: p.bstore,
			Settings:   settings,
		}

		provisioner := &deployer.Provisioner{
			Identity:  identity,
			Provider:  p.cfg.Provider,
			AccountID: p.cfg.AccountID,
			License:   p.cfg.OnPremToken,
			Store:     p.bstore,
			Clients:   deployer.NewClients(p.cache),
		}

		p.collectorSurface = &collectorSurface{
			resolver: resolver,
			observer: &observer.Observer{
				Identity:          identity,
				Kind:              collectorType,
				Mode:              mode,
				OpAMPEndpoint:     p.cfg.OpAMPEndpoint,
				Pin:               collectorimage.Released,
				HeartbeatInterval: p.cfgMgr.Settings().HeartbeatInterval,
				Settings:          settings,
				Planner:           resolver,
				Store:             p.bstore,
				Clients:           observer.NewClients(p.cache),
			},
			deployer: &deployer.Deployer{
				Provisioner:   provisioner,
				Teardown:      deployer.NewTeardownClients(p.cache),
				Store:         p.bstore,
				Plans:         resolver,
				Kind:          collectorType,
				Mode:          mode,
				Settings:      settings,
				OpAMPEndpoint: p.cfg.OpAMPEndpoint,
				Pin:           collectorimage.Released,
				Desired:       p.DesiredConfig,
			},
		}
	})
	return p.collectorSurface
}

// collectorSurface is the trio, held together because they share one plan.
type collectorSurface struct {
	resolver *placement.Resolver
	observer *observer.Observer
	deployer *deployer.Deployer
}
