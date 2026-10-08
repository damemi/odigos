package main

import (
	"context"
	"fmt"

	cilumebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"

	"github.com/odigos-io/odigos-enterprise/common-enterprise/ebpf/ebpfmaps"
	"github.com/odigos-io/odigos-enterprise/common-enterprise/ebpf/unixfd"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/logcapture"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
)

// ebpfMapExchange owns the eBPF maps the enterprise SDKs write into and the unix socket that
// hands their file descriptors to the enterprise collector's odigosebpfreceiver. Both ends are
// enterprise-only, so none of this exists in the OSS odiglet.
type ebpfMapExchange struct {
	traces *cilumebpf.Map
	logs   *cilumebpf.Map
}

// newEbpfMapExchange creates the shared maps. The maps live for the lifetime of the process;
// if any of them fails to be created the ones already made are closed before returning.
func newEbpfMapExchange() (*ebpfMapExchange, error) {
	// odiglet raises this again when it builds the instrumentation manager, but map creation
	// happens first and needs the limit lifted already.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("failed to remove memlock rlimit: %w", err)
	}

	traces, err := ebpfmaps.CreateTracesMap()
	if err != nil {
		return nil, fmt.Errorf("failed to create traces eBPF map: %w", err)
	}

	// A kernel without ring buffer support yields a nil logs map rather than an error.
	logs, err := ebpfmaps.CreateLogsMap()
	if err != nil {
		traces.Close()
		return nil, fmt.Errorf("failed to create logs eBPF map: %w", err)
	}

	if logs != nil {
		logcapture.GetInstance().SetExternalLogsMap(logs)
	}

	return &ebpfMapExchange{
		traces: traces,
		logs:   logs,
	}, nil
}

// applyTo hands the maps to the instrumentation manager, so the enterprise SDK factories run as
// external readers against them instead of creating maps of their own.
func (e *ebpfMapExchange) applyTo(opts *ebpf.InstrumentationManagerOptions) {
	opts.TracesMap = e.traces
	opts.LogsMap = e.logs
}

// serve runs the file descriptor server until ctx is canceled. It stays up for odiglet's whole
// lifetime so a collector that restarts can reconnect and ask for the current descriptors.
func (e *ebpfMapExchange) serve(ctx context.Context) error {
	server := &unixfd.Server{
		SocketPath: unixfd.DefaultSocketPath,
		Logger:     commonlogger.ToLogr(),
		TracesFDProvider: func() int {
			return e.traces.FD()
		},
		LogsFDsProvider: func() []int {
			if e.logs != nil {
				return []int{e.logs.FD()}
			}
			return nil
		},
		LogsAttrSubscribe: func() (<-chan string, []string) {
			return logcapture.GetInstance().SubscribeAttrs()
		},
	}

	return server.Run(ctx)
}

// close releases the maps. Safe to call on a partially populated exchange.
func (e *ebpfMapExchange) close() {
	for _, m := range []*cilumebpf.Map{e.traces, e.logs} {
		if m != nil {
			m.Close()
		}
	}
}
