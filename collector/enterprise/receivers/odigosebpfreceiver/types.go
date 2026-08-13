package odigosebpfreceiver

const (
	numOfPages = 2048
	// attrValueMaxSize must match the value size of the metrics_attributes map in
	// ebpfshare/ebpfmaps. The writer truncates packed attributes to this length.
	attrValueMaxSize = 1024

	// serviceNameKey is the resource attribute a metric batch needs in order to be
	// attributed to a workload downstream.
	serviceNameKey = "service.name"
)

// ReceiverType defines the type of receiver (traces, metrics, or logs)
type ReceiverType int

const (
	ReceiverTypeTraces ReceiverType = iota
	ReceiverTypeMetrics
	ReceiverTypeLogs
)
