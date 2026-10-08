package placement

import (
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	awsstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// Problem codes for source-to-network attribution.
const (
	// ProblemSourceHasNoNetwork: enabled Lambda functions with no VpcConfig. Informational — says
	// this is a posture, not a fault.
	ProblemSourceHasNoNetwork = "CollectorSourceHasNoNetwork"
	// ProblemSourceNetworkUnresolved: a Fargate service whose subnets Odigos could not resolve to a VPC.
	ProblemSourceNetworkUnresolved = "CollectorSourceNetworkUnresolved"
)

// Inventory is the discovered AWS state placement reads: the workload rows the detectors persisted, and
// the sources that say which of them the customer actually asked to instrument.
//
// Read from the store rather than from AWS. Every field here was already fetched by discovery —
// DescribeServices returns the awsvpc configuration, ListFunctions returns VpcConfig — so re-describing
// the account to learn which VPC a workload runs on would double the discovery cost of every pass to
// learn nothing new.
type Inventory struct {
	ECSServices []awsstore.ECSService
	Lambdas     []awsstore.LambdaFunction
	// Sources are the customer's instrumentation intent. Only an ENABLED source's workload counts
	// towards a network needing a Collector: a discovered-but-not-instrumented service
	// sends no telemetry, and building an egress tier for it would bill the customer for a Collector
	// nothing exports through.
	Sources []basestore.SourceRecord
}

// Sources maps the enabled AWS workloads to the network each runs on, in the shape
// collectorplacement.Resolve consumes.
//
// The two workload kinds reach their network differently, and only one of them can fail to:
//
//   - A Lambda function carries its VPC id in VpcConfig, so attribution is a field read. An EMPTY id is
//     not a failure — it is the case: the function runs in an AWS-managed VPC, cannot reach an
//     internal endpoint, and keeps exporting straight to the destination. Those arrive as sources with
//     no network, which is how they reach Plan.Unattached.
//   - A Fargate service's awsvpcConfiguration names SUBNETS and never the VPC, so its network is a
//     subnet lookup against the topology. That lookup can miss — a subnet deleted since the last
//     discovery pass, or one in a region the topology scan failed on — and a miss is reported rather
//     than folded into the no-network bucket. Folding them would tell a customer to attach a Fargate
//     service to a VPC it is already in.
//
// Workloads in a region the topology does not describe are skipped entirely, with no problem: the
// region has no plan at all, and reporting every workload in it would bury the one message that matters
// (the scan failure itself, which Discover returns).
func Sources(inv Inventory, topo Topology) ([]collectorplacement.Source, []collectorstatus.Problem) {
	enabled := enabledExternalIDs(inv.Sources)

	var out []collectorplacement.Source
	var unattached, unresolved []string

	for _, fn := range inv.Lambdas {
		if !enabled[awsstore.KindLambda][fn.FunctionARN] || !topo.Scanned(fn.Region) {
			continue
		}
		out = append(out, collectorplacement.Source{Ref: fn.FunctionARN, Region: fn.Region, NetworkID: fn.VPCID})
		if fn.VPCID == "" {
			unattached = append(unattached, fn.FunctionARN)
		}
	}

	for _, svc := range inv.ECSServices {
		ref := svc.ClusterARN + "/" + svc.ServiceName
		if !enabled[awsstore.KindECSTask][ref] || !topo.Scanned(svc.Region) {
			continue
		}
		vpcID, ok := networkOfService(svc, topo)
		if !ok {
			unresolved = append(unresolved, ref)
			continue
		}
		out = append(out, collectorplacement.Source{Ref: ref, Region: svc.Region, NetworkID: vpcID})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].Ref < out[j].Ref
	})
	sort.Strings(unattached)
	sort.Strings(unresolved)

	var problems []collectorstatus.Problem
	if len(unattached) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code: ProblemSourceHasNoNetwork,
			// Info, not Degraded, and that is a customer decision rather than a softened one. A function
			// with no VpcConfig keeps the in-function Collector's batching and local queue; what it gives
			// up is fleet-wide processing and one egress point. Attaching it to a VPC costs it default
			// internet egress and needs a NAT gateway, so it is the customer's call to make and Odigos
			// must never make it — which means this cannot be a fault.
			Severity: collectorstatus.SeverityInfo,
			Message: fmt.Sprintf("%d instrumented Lambda function(s) have no VpcConfig (%s) and are exporting "+
				"directly to the destination. Attach a function to a VPC that has an Odigos Collector to "+
				"route it through the Collector; that costs the function its default internet egress and "+
				"needs a NAT gateway, so Odigos does not do it for you",
				len(unattached), joinIDs(unattached)),
		})
	}
	if len(unresolved) > 0 {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemSourceNetworkUnresolved,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("could not resolve which VPC %s runs on: none of its awsvpc subnets is a "+
				"subnet Odigos found. It is left out of Collector placement and keeps exporting straight to "+
				"the destination until the next discovery pass reconciles the two",
				joinIDs(unresolved)),
		})
	}
	return out, problems
}

// Networks returns every VPC in the scanned regions, which is collectorplacement.Input.Networks.
//
// Every VPC is supplied so the regional resolver can validate an explicitly selected host against the
// discovered topology. Networks with neither sources nor configuration are dropped by Resolve itself,
// so listing them here costs nothing in the plan.
func Networks(topo Topology) []collectorplacement.NetworkRef {
	vpcs := topo.VPCs()
	out := make([]collectorplacement.NetworkRef, 0, len(vpcs))
	for _, v := range vpcs {
		out = append(out, collectorplacement.NetworkRef{Region: v.Region, NetworkID: v.ID})
	}
	return out
}

// networkOfService resolves a Fargate service's VPC from its subnets.
//
// The first subnet that resolves wins, and no agreement check is needed: ECS requires every subnet in an
// awsvpcConfiguration to be in the same VPC, so a service whose subnets spanned two could not have been
// created. Trying the rest is for the case where one subnet was deleted but the service still names it.
func networkOfService(svc awsstore.ECSService, topo Topology) (string, bool) {
	for _, subnetID := range svc.SubnetIDs {
		if vpcID, ok := topo.VPCOfSubnet(subnetID); ok {
			return vpcID, true
		}
	}
	return "", false
}

// enabledExternalIDs indexes the enabled sources by kind and external id — the same identity the
// instrumentor and the observer match on, so a workload that placement counts is exactly one the
// instrumentor would instrument.
func enabledExternalIDs(sources []basestore.SourceRecord) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range sources {
		if !s.Enabled || s.ExternalResourceID == "" {
			continue
		}
		if out[s.Kind] == nil {
			out[s.Kind] = map[string]bool{}
		}
		out[s.Kind][s.ExternalResourceID] = true
	}
	return out
}
