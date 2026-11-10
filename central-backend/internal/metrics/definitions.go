package metrics

import "github.com/prometheus/client_golang/prometheus"

type GaugeDef struct {
	Name    string
	Desc    string
	Options []prometheus.GaugeOpts
	Target  *prometheus.Gauge // Pointer to the variable to populate
}

type CounterDef struct {
	Name    string
	Options []prometheus.CounterOpts
	Target  *prometheus.Counter // Pointer to the variable to populate
}

type CounterVecDef struct {
	Name   string
	Help   string
	Labels []string
	Target **prometheus.CounterVec // Pointer to the variable to populate
}

// Add new metrics here
var (
	SSEBroadcastRate              *prometheus.CounterVec
	RemoteFetchRequestsInProgress prometheus.Gauge
)

var RemoteFetchRequestsInProgressDef = GaugeDef{
	Name:   "central_backend_remote_fetch_requests_in_progress_amount",
	Target: &RemoteFetchRequestsInProgress,
	Options: []prometheus.GaugeOpts{
		{
			Help: "Total number of remote fetch requests in progress",
		},
	},
}

var allGauges = []GaugeDef{
	RemoteFetchRequestsInProgressDef,
}

var SSEBroadcastRateDef = CounterVecDef{
	Name:   "central_backend_sse_broadcast_events_amount",
	Help:   "Rate of SSE broadcast events by cluster",
	Labels: []string{"cluster_id", "cluster_name"},
	Target: &SSEBroadcastRate,
}

var allCounterVecs = []CounterVecDef{
	SSEBroadcastRateDef,
}
