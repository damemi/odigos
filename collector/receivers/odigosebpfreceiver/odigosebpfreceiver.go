package odigosebpfreceiver

import (
	"context"
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"

	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"

	"github.com/odigos-io/odigos/collector/receivers/odigosebpfreceiver/internal/metadata"
	"github.com/odigos-io/odigos/common/ebpf/unixfd"
)

type ebpfReceiver struct {
	config       *Config
	cancel       context.CancelFunc
	logger       *zap.Logger
	receiverType ReceiverType

	// Pipeline consumers for forwarding telemetry data
	nextTraces consumer.Traces
	nextLogs   consumer.Logs

	// Telemetry
	telemetry *metadata.TelemetryBuilder
	settings  receiver.Settings

	wg sync.WaitGroup
}

func (r *ebpfReceiver) Start(ctx context.Context, host component.Host) error {
	// Initialize telemetry
	telemetryBuilder, err := metadata.NewTelemetryBuilder(r.settings.TelemetrySettings)
	if err != nil {
		return fmt.Errorf("failed to create telemetry builder: %w", err)
	}
	r.telemetry = telemetryBuilder

	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	switch r.receiverType {
	case ReceiverTypeTraces:
		r.startTracesReceiver(ctx)
	case ReceiverTypeLogs:
		r.startLogsReceiver(ctx)
	}

	return nil
}

// startTracesReceiver sets up a single FD client and map manager for traces.
func (r *ebpfReceiver) startTracesReceiver(ctx context.Context) {
	updates := make(chan *ebpf.Map, 1)

	/*
	 * eBPF Receiver Architecture
	 *
	 * This receiver operates with two main goroutines that work together to handle
	 * eBPF maps from odiglet and process trace data:
	 *
	 * 1. FD Client Goroutine: Connects to odiglet via Unix socket and receives file
	 *    descriptors for eBPF maps. When odiglet restarts, it creates a new map and
	 *    sends us the FD. This goroutine converts FDs to map objects and forwards
	 *    them to the map manager.
	 *
	 * 2. Map Manager Goroutine: Handles the lifecycle of eBPF maps. It receives new
	 *    maps from the FD client, stops any existing perf readers, closes old maps,
	 *    and starts new perf readers for incoming maps. This ensures seamless
	 *    switching between maps during odiglet restarts.
	 *
	 * The two goroutines communicate via the 'updates' channel, creating a pipeline
	 * that maintains continuous trace data flow even when odiglet restarts.
	 */
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		var (
			currentMap   *ebpf.Map
			readerCancel context.CancelFunc
			readerWg     sync.WaitGroup
		)

		defer func() {
			if readerCancel != nil {
				readerCancel()
				readerWg.Wait()
			}
			if currentMap != nil {
				currentMap.Close()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case newMap, ok := <-updates:
				if !ok {
					return
				}
				if readerCancel != nil {
					readerCancel()
					readerWg.Wait()
				}
				if currentMap != nil {
					currentMap.Close()
				}

				currentMap = newMap
				readerCtx, cancel := context.WithCancel(ctx)
				readerCancel = cancel

				r.logger.Info("switched to new eBPF traces map", zap.Int("fd", newMap.FD()))

				readerWg.Add(1)
				go func() {
					defer func() {
						r.logger.Info("traces reader stopped")
						readerWg.Done()
					}()

					if err := r.tracesReadLoop(readerCtx, newMap); err != nil {
						r.logger.Error("tracesReadLoop failed", zap.Error(err))
					}
				}()
			}
		}
	}()

	// FD client: connects to odiglet and receives FDs for traces eBPF maps.
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(updates)

		r.logger.Info("starting traces FD client")

		err := unixfd.ConnectAndListen(ctx, unixfd.DefaultSocketPath, unixfd.ReqGetTracesFD, r.logger, func(fd int) {
			r.logger.Info("received new traces FD from odiglet", zap.Int("fd", fd))

			newMap, err := ebpf.NewMapFromFD(fd)
			if err != nil {
				r.logger.Error("failed to create map from FD", zap.Error(err), zap.Int("fd", fd))
				unix.Close(fd)
				return
			}

			select {
			case updates <- newMap:
				r.logger.Info("queued new traces map for processing")
			case <-ctx.Done():
				newMap.Close()
			}
		})

		if err != nil && ctx.Err() == nil {
			r.logger.Error("traces FD client failed", zap.Error(err))
		}
	}()
}

// startLogsReceiver connects to odiglet to receive the logs ring buffer FD
// and stream per-process attribute events.
func (r *ebpfReceiver) startLogsReceiver(ctx context.Context) {
	updates := make(chan *ebpf.Map, 1)
	attrCache := &logsAttrCache{cache: make(map[uint32]string)}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		var (
			currentRingBuf *ebpf.Map
			readerCancel   context.CancelFunc
			readerWg       sync.WaitGroup
		)

		defer func() {
			if readerCancel != nil {
				readerCancel()
				readerWg.Wait()
			}
			if currentRingBuf != nil {
				currentRingBuf.Close()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case newMap, ok := <-updates:
				if !ok {
					return
				}

				if readerCancel != nil {
					readerCancel()
					readerWg.Wait()
				}
				if currentRingBuf != nil {
					currentRingBuf.Close()
				}

				currentRingBuf = newMap

				r.logger.Info("received new logs ring buffer",
					zap.Int("ringBuf_fd", currentRingBuf.FD()))

				readerCtx, cancel := context.WithCancel(ctx)
				readerCancel = cancel

				ringBuf := currentRingBuf
				readerWg.Add(1)
				go func() {
					defer func() {
						r.logger.Info("logs reader stopped")
						readerWg.Done()
					}()

					if err := r.logsReadLoop(readerCtx, ringBuf, attrCache); err != nil {
						r.logger.Error("logsReadLoop failed", zap.Error(err))
					}
				}()
			}
		}
	}()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(updates)

		r.logger.Info("starting logs FD client")

		err := unixfd.ConnectAndListenLogs(ctx, unixfd.DefaultSocketPath, r.logger,
			func(fd int) {
				r.logger.Info("received logs ring buffer FD from odiglet", zap.Int("fd", fd))

				ringBuf, err := ebpf.NewMapFromFD(fd)
				if err != nil {
					r.logger.Error("failed to create ring buffer map from FD", zap.Error(err), zap.Int("fd", fd))
					unix.Close(fd)
					return
				}

				select {
				case updates <- ringBuf:
					r.logger.Info("queued new logs ring buffer for processing")
				case <-ctx.Done():
					ringBuf.Close()
				}
			},
			func(line string) {
				r.handleLogsAttrEvent(line, attrCache)
			},
		)

		if err != nil && ctx.Err() == nil {
			r.logger.Error("logs FD client failed", zap.Error(err))
		}
	}()
}

// handleLogsAttrEvent parses "R<pid>:<attrs>" or "U<pid>" and updates the cache.
func (r *ebpfReceiver) handleLogsAttrEvent(line string, cache *logsAttrCache) {
	ev, ok := unixfd.DecodeAttrEvent(line)
	if !ok {
		return
	}
	switch ev.Type {
	case unixfd.AttrEventRegister:
		cache.set(ev.PID, ev.Attrs)
	case unixfd.AttrEventUnregister:
		cache.delete(ev.PID)
	}
}

func (r *ebpfReceiver) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}

	// Cleanup telemetry
	if r.telemetry != nil {
		r.telemetry.Shutdown()
	}

	done := make(chan struct{})

	// Wait for all goroutines to finish in a separate goroutine
	// so we can respect the shutdown context timeout
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		r.logger.Warn("odigos-ebpf: receiver shutdown did not finish before context was canceled")
		return ctx.Err()
	case <-done:
		r.logger.Info("odigos-ebpf: receiver shutdown complete")
		return nil
	}
}
