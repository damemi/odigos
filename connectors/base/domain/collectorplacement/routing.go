package collectorplacement

import (
	"github.com/odigos-io/odigos/connectors/base/domain/destination"
)

// Endpoint is where one regional Collector can be reached, as the transport resolver published it.
type Endpoint struct {
	GRPC     string
	HTTP     string
	Insecure bool
	// Ready is the cutover latch: has this Collector reported ready at least once. It travels
	// with the endpoint because the two are recorded on the same row and read on the same pass, and
	// because a caller that had to fetch it separately could forget to — which is a fleet re-pointed
	// before anything was ready.
	Ready bool
}

// RoutingTargets projects a placement plan into exactly one routing target per deployable region. The
// target's Network is only the selected host VPC. Consumer VPCs are deliberately not projected as
// Collector targets: their ability to use this endpoint must be supplied as explicit reachability
// evidence by the provider's workload reconciler.
//
// endpoints is keyed by NetworkRef.Key(). A network with no endpoint yet stays enabled but unroutable,
// and routing reports it as not-yet-deployed rather than pointing at nothing.
func RoutingTargets(plan Plan, endpoints map[string]Endpoint) []destination.CollectorTarget {
	out := make([]destination.CollectorTarget, 0, len(plan.Regions))
	for _, r := range plan.Regions {
		if r.Host.NetworkID == "" {
			continue
		}
		ep := endpoints[r.Host.Key()]
		out = append(out, destination.CollectorTarget{
			Region:       r.Region,
			Network:      r.Host.NetworkID,
			Endpoint:     ep.GRPC,
			HTTPEndpoint: ep.HTTP,
			Insecure:     ep.Insecure,
			EverReady:    ep.Ready,
		})
	}
	return out
}
