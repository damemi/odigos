// Package collectorplacement decides which regional managed Collectors exist and
// records each workload network's physical location.
//
// Pure — no I/O, no provider SDK types. Provider-neutral on purpose: providers
// supply discovered networks/sources and this package chooses a host VPC without
// making any reachability assumption.
//
// It answers one question per network — who serves this network's workloads — and that answer has to
// match what destination.ResolveRouting tells a workload, or Odigos points traffic at a Collector it
package collectorplacement

import (
	"fmt"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

// NetworkRef identifies one network in one region.
type NetworkRef struct {
	Region    string
	NetworkID string
}

// Key identifies a physical network in a plan. It includes region because two clouds can hand out the
// same network id in different regions; the regional Collector itself is keyed only by region.
func (n NetworkRef) Key() string { return n.Region + "/" + n.NetworkID }

// Source is one discovered, enabled source and the network it runs on.
type Source struct {
	// Ref identifies the source for reporting — a function ARN, a service ARN, a resource id.
	Ref    string
	Region string
	// NetworkID is empty for a source with no network at all: a Lambda without VpcConfig, which runs in
	// a provider-managed VPC and cannot reach an internal endpoint. Those are collected in
	// Plan.Unattached and keep exporting straight to the destination.
	NetworkID string
}

// Input is everything the decision depends on.
type Input struct {
	// Networks are the networks the provider found to EXIST, whether or not they have sources.
	//
	// Required: the provider must enumerate the physical networks before one can
	// be selected as a regional Collector host. This prevents deployment into a
	// VPC that is missing from discovery.
	Networks []NetworkRef
	// Sources are the enabled sources. They decide which workload networks consume a
	// Collector, never whether the Collector exists. Lifecycle intent comes from
	// Settings.DeployRegions so a Collector can be provisioned before any source is marked.
	Sources []Source
	// Settings is the resolved operational configuration.
	Settings collectorsettings.Settings
}

// Role is the regional placement decision for a workload network.
type Role string

const (
	// RoleCollector: this network hosts the region's one Collector.
	RoleCollector Role = "collector"
	// RoleConsumer: this network's workloads are served by the regional Collector
	// hosted in another VPC. This is not a sharedWith relationship.
	RoleConsumer Role = "consumer"
	// RoleNone: nothing serves this network's workloads; they export straight to the destination.
	RoleNone Role = "none"
)

// Reason says why, when the Role is not the obvious one. Codes, not sentences.
type Reason string

const (
	// ReasonHasSources: the ordinary case — enabled sources run here.
	ReasonHasSources Reason = "has-sources"
	// ReasonRegionHost: this network hosts the region's one Collector.
	ReasonRegionHost Reason = "region-host"
	// ReasonRegionConsumer: this network has sources served by the regional host.
	ReasonRegionConsumer Reason = "region-consumer"

	// ReasonFeatureDisabled: `collector.enabled` is false. Nothing anywhere.
	ReasonFeatureDisabled Reason = "feature-disabled"
	ReasonRegionDisabled  Reason = "region-disabled"
	// ReasonNoEnabledSources: nothing runs here that Odigos instruments, so there is nothing to collect.
	ReasonNoEnabledSources Reason = "no-enabled-sources"
	// ReasonHostNetworkRequired means the region has multiple candidate VPCs and
	// the customer must choose the physical host explicitly.
	ReasonHostNetworkRequired Reason = "host-network-required"
	// ReasonHostNetworkUnknown means the configured host was not discovered.
	ReasonHostNetworkUnknown Reason = "host-network-unknown"
)

// Network is one network's decision.
type Network struct {
	NetworkRef
	Role   Role
	Reason Reason
	// ServedBy is the selected host VPC for the region. It is a physical location,
	// not another Collector identity or a sharedWith graph edge.
	ServedBy string
	// CollectorRegion is the logical Collector identity serving this network.
	CollectorRegion string
	// Sources are the enabled source refs on this network, sorted.
	Sources []string
}

// Region is one regional Collector decision. Host is empty when placement was
// refused because no safe host could be selected.
type Region struct {
	Region   string
	Host     NetworkRef
	Networks []Network
	Sources  []string
}

// Serves reports whether this network is the physical host of a Collector.
func (n Network) Serves() bool { return n.Role == RoleCollector }

// Plan is the whole decision.
type Plan struct {
	// Networks, sorted by key, one entry per network the plan has an opinion about.
	Networks []Network
	// Regions is the lifecycle/reporting view: at most one regional Collector per
	// region. A region with an ambiguous host has no Host and is not deployable.
	Regions []Region
	// Unattached are the sources with no network at all. They export straight to the destination, and
	// attaching them to a network is the customer's decision because it costs the workload its default
	// internet egress.
	Unattached []string
	// Problems are reportable placement conditions, especially ambiguous or
	// unknown host selection.
	Problems []collectorstatus.Problem
}

// CollectorNetworks returns one host network per deployable region, sorted by
// region. NetworkID is retained solely as the physical host location.
func (p Plan) CollectorNetworks() []NetworkRef {
	var out []NetworkRef
	for _, r := range p.Regions {
		if r.Host.NetworkID != "" {
			out = append(out, r.Host)
		}
	}
	return out
}

// CollectorRegions returns the logical deployment units, one per region.
func (p Plan) CollectorRegions() []string {
	out := make([]string, 0, len(p.Regions))
	for _, r := range p.Regions {
		if r.Host.NetworkID != "" {
			out = append(out, r.Region)
		}
	}
	return out
}

// ServedBy answers, for one network, which network's Collector its workloads should use. Empty means
// none, and the workload exports straight to the destination.
func (p Plan) ServedBy(region, networkID string) string {
	key := NetworkRef{Region: region, NetworkID: networkID}.Key()
	for _, n := range p.Networks {
		if n.Key() == key {
			return n.ServedBy
		}
	}
	return ""
}

// Resolve groups sources by workload network, then chooses at most one host VPC
// for each region. Reachability is intentionally not inferred here: a consumer
// network is merely associated with the regional host and a later provider layer
// must verify whether it can use the host ingress.
func Resolve(in Input) Plan {
	known := map[string]NetworkRef{}
	for _, n := range in.Networks {
		if n.NetworkID != "" && n.Region != "" {
			known[n.Key()] = n
		}
	}

	sources, unattached, orphaned := groupSources(in.Sources, known)
	plan := Plan{Unattached: unattached}
	for _, ref := range sortedRefs(orphaned) {
		plan.Problems = append(plan.Problems, collectorstatus.Problem{
			Code:     "CollectorSourceOnUnknownNetwork",
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("sources run on network %s in %s, which is not a network Odigos could "+
				"find; they keep exporting straight to the destination", ref.NetworkID, ref.Region),
		})
	}

	// Explicit regional configuration is the lifecycle intent. Marking a source must not create cloud
	// infrastructure as a side effect: the supported sequence is create Collector, apply its regional
	// route marker, then mark sources for instrumentation.
	byRegion := map[string][]NetworkRef{}
	configured := map[string]bool{}
	for _, region := range in.Settings.DeployRegions() {
		configured[region] = true
		byRegion[region] = nil
	}

	// Keep a workload-facing entry for every source-bearing network. A VPC is workload location, never
	// an independent Collector switch. Unconfigured regions remain on direct export even when a source
	// is marked there.
	for key, srcs := range sources {
		ref := known[key]
		n := Network{NetworkRef: ref, Sources: srcs, CollectorRegion: ref.Region}
		if !in.Settings.Enabled {
			n.Role, n.Reason = RoleNone, ReasonFeatureDisabled
		} else if !configured[ref.Region] {
			n.Role, n.Reason = RoleNone, ReasonRegionDisabled
		} else {
			n.Role, n.Reason = RoleConsumer, ReasonRegionConsumer
			byRegion[ref.Region] = append(byRegion[ref.Region], ref)
		}
		plan.Networks = append(plan.Networks, n)
	}

	for _, region := range sortedRegionKeys(byRegion) {
		candidates := byRegion[region]
		// Before sources are marked there is no workload VPC from which to infer placement. In that
		// case every discovered network in the configured region is eligible: one can be selected
		// safely, several require networkId, and none produce a reportable refusal.
		if len(candidates) == 0 {
			candidates = knownNetworksInRegion(known, region)
		}
		hostID := in.Settings.Region(region).HostNetworkID
		host, problem := chooseHost(region, candidates, hostID, known)
		r := Region{Region: region, Sources: regionSources(candidates, sources)}
		if problem != nil {
			plan.Problems = append(plan.Problems, *problem)
			plan.Regions = append(plan.Regions, r)
			continue
		}
		r.Host = host
		hostPresent := false
		for i := range plan.Networks {
			n := &plan.Networks[i]
			if n.Region != region || n.Role == RoleNone {
				continue
			}
			n.ServedBy = host.NetworkID
			if n.NetworkID == host.NetworkID {
				hostPresent = true
				n.Role, n.Reason = RoleCollector, ReasonRegionHost
			}
			r.Networks = append(r.Networks, *n)
		}
		// An explicitly selected host may run no workload of its own. It still
		// needs a regional placement/report row and is the endpoint's physical
		// location.
		if !hostPresent {
			n := Network{NetworkRef: host, CollectorRegion: region, Role: RoleCollector,
				Reason: ReasonRegionHost, ServedBy: host.NetworkID}
			plan.Networks = append(plan.Networks, n)
			r.Networks = append(r.Networks, n)
		}
		plan.Regions = append(plan.Regions, r)
	}

	// Stable reporting order independent of map iteration.
	sortNetworks(plan.Networks)
	return plan
}

func chooseHost(region string, candidates []NetworkRef, configured string, known map[string]NetworkRef) (NetworkRef, *collectorstatus.Problem) {
	if configured != "" {
		ref, ok := known[(NetworkRef{Region: region, NetworkID: configured}).Key()]
		if !ok {
			return NetworkRef{}, &collectorstatus.Problem{Code: "CollectorHostNetworkUnknown", Severity: collectorstatus.SeverityError,
				Message: fmt.Sprintf("Collector host network %s is configured for region %s, but Odigos could not find that network", configured, region)}
		}
		return ref, nil
	}
	unique := map[string]NetworkRef{}
	for _, ref := range candidates {
		unique[ref.NetworkID] = ref
	}
	if len(unique) == 1 {
		for _, ref := range unique {
			return ref, nil
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sortStrings(ids)
	if len(ids) == 0 {
		return NetworkRef{}, &collectorstatus.Problem{Code: "CollectorHostNetworkRequired", Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("region %s has no discovered network in which to place its Collector; configure collector.regions.%s.networkId after a network is available", region, region)}
	}
	return NetworkRef{}, &collectorstatus.Problem{Code: "CollectorHostNetworkRequired", Severity: collectorstatus.SeverityError,
		Message: fmt.Sprintf("region %s has multiple eligible networks (%s); configure collector.regions.%s.networkId to select the network", region, stringsJoin(ids), region)}
}

func knownNetworksInRegion(known map[string]NetworkRef, region string) []NetworkRef {
	out := make([]NetworkRef, 0)
	for _, ref := range known {
		if ref.Region == region {
			out = append(out, ref)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].NetworkID < out[j-1].NetworkID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func regionSources(refs []NetworkRef, byNetwork map[string][]string) []string {
	var out []string
	for _, ref := range refs {
		out = append(out, byNetwork[ref.Key()]...)
	}
	sortStrings(out)
	return out
}

func sortedRegionKeys(m map[string][]NetworkRef) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortNetworks(ns []Network) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && ns[j].Key() < ns[j-1].Key(); j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

func stringsJoin(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	out := ids[0]
	for _, id := range ids[1:] {
		out += ", " + id
	}
	return out
}

// groupSources buckets sources by network, separating the ones with no network and the ones on a network
// the provider did not find.
func groupSources(sources []Source, known map[string]NetworkRef) (byNetwork map[string][]string, unattached []string, orphaned map[string]NetworkRef) {
	byNetwork = map[string][]string{}
	orphaned = map[string]NetworkRef{}

	for _, s := range sources {
		if s.NetworkID == "" {
			unattached = append(unattached, s.Ref)
			continue
		}
		ref := NetworkRef{Region: s.Region, NetworkID: s.NetworkID}
		if _, ok := known[ref.Key()]; !ok {
			// A source on a network nothing enumerated. Reported rather than dropped: it is either a
			// discovery gap or a network Odigos has no permission to see, and both are worth saying.
			orphaned[ref.Key()] = ref
			continue
		}
		byNetwork[ref.Key()] = append(byNetwork[ref.Key()], s.Ref)
	}

	sortStrings(unattached)
	for k := range byNetwork {
		sortStrings(byNetwork[k])
	}
	return byNetwork, unattached, orphaned
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortedRefs(m map[string]NetworkRef) []NetworkRef {
	out := make([]NetworkRef, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
