package ebpfmaps

import (
	"fmt"
	"os"

	cilumebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/features"
)

func CreateTracesMap() (*cilumebpf.Map, error) {
	spec := &cilumebpf.MapSpec{
		Name: "traces",
	}

	// Check if the current kernel supports the ring buffer
	if features.HaveMapType(cilumebpf.RingBuf) == nil {
		spec.Type = cilumebpf.RingBuf
		// Set MaxEntries for ring buffer: MaxEntries = NumOfPages * os.Getpagesize()
		spec.MaxEntries = uint32(NumOfPages * os.Getpagesize())
	} else {
		// If not, default to the old buffer
		spec.Type = cilumebpf.PerfEventArray
	}
	m, err := cilumebpf.NewMap(spec)
	if err != nil {
		return nil, fmt.Errorf("create traces eBPF map: %w", err)
	}
	return m, nil
}

// CreateLogsMap creates the logs RingBuf eBPF map used to receive log events
// from the eBPF probes. Returns (nil, nil) when the kernel does not support
// RingBuf, since there is no PerfEventArray fallback for logs.
func CreateLogsMap() (*cilumebpf.Map, error) {
	if err := features.HaveMapType(cilumebpf.RingBuf); err != nil {
		return nil, nil //nolint:nilerr // graceful: no RingBuf support, no fallback for logs
	}

	logsSpec := &cilumebpf.MapSpec{
		Type:       cilumebpf.RingBuf,
		Name:       "logs",
		MaxEntries: LogsMaxEntries,
	}

	logsMap, err := cilumebpf.NewMap(logsSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to create logs eBPF map: %w", err)
	}

	return logsMap, nil
}
