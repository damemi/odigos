package collectorconfig

import (
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// memory_limiter sizing, derived from the memory the Collector's workload was allocated.
//
// Absolute MiB rather than `limit_percentage`. The percentage form measures against the memory the
// process can see, which on a shared host is the host's, not the Collector's budget — the per-workload
// Lambda config uses percentages precisely because one layer serves functions from 128MB to 10GB and it
// cannot know the allocation. Here Odigos chose the allocation, so it can state the limit exactly.
const (
	// LimitPercentOfAllocation is the hard limit as a share of the workload's memory.
	//
	// Not 100%: the Collector process sits roughly 50 MiB above limit_mib in RSS (its own note in the
	// upstream processor), the Go runtime holds more, and on a managed Collector the Supervisor shares
	// the same allocation — it is the parent process, not a sidecar. A limit set at the allocation would
	// be reached only after the kernel had already killed the task, which is the one outcome
	// memory_limiter exists to prevent.
	LimitPercentOfAllocation = 80
	// SpikePercentOfLimit is the soft limit as a share of the hard limit. Matches the upstream default
	// relationship: crossing it starts refusing data and forcing GC, so the hard limit is approached
	// under back-pressure rather than hit.
	SpikePercentOfLimit = 20
	// CheckInterval is how often usage is sampled. One second is upstream's recommendation for a
	// component in the data path.
	CheckInterval = "1s"
	// MinAllocationMiB is the smallest allocation that produces usable limits. Below it the derived
	// numbers are small enough that the Collector refuses data as soon as it accepts a batch, which
	// reads as a broken destination rather than as a Collector that was never given enough memory.
	// smallest documented size is 1024.
	MinAllocationMiB = 256
)

// MemoryLimits is the derived memory_limiter sizing.
type MemoryLimits struct {
	LimitMiB      int
	SpikeLimitMiB int
}

// DeriveMemoryLimits sizes memory_limiter from the workload's memory allocation.
//
// Integer arithmetic throughout, deliberately: this feeds a document whose hash decides whether a
// configuration is pushed, and a float rounding difference between builds would be a push with no
// change behind it.
func DeriveMemoryLimits(allocationMiB int) (MemoryLimits, []collectorstatus.Problem) {
	if allocationMiB < MinAllocationMiB {
		return MemoryLimits{}, []collectorstatus.Problem{{
			Code:     "CollectorMemoryTooSmall",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("refusing to render a Collector configuration for a %d MiB allocation: "+
				"the minimum is %d MiB, and below it memory_limiter refuses telemetry as fast as the receiver "+
				"accepts it — which looks like a broken destination rather than a Collector with too little "+
				"memory. The smallest documented size allocates 1024 MiB", allocationMiB, MinAllocationMiB),
		}}
	}

	limit := allocationMiB * LimitPercentOfAllocation / 100
	return MemoryLimits{
		LimitMiB:      limit,
		SpikeLimitMiB: limit * SpikePercentOfLimit / 100,
	}, nil
}
