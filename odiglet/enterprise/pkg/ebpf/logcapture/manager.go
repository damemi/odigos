package logcapture

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/odigos-io/ebpf-core/pkg/instrumentors/logs"
	"github.com/odigos-io/odigos/common/ebpf/unixfd"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

var (
	instance *Manager
	once     sync.Once
)

func GetInstance() *Manager {
	once.Do(func() {
		instance = &Manager{}
	})
	return instance
}

// Manager handles eBPF log capture across all instrumented languages.
// BPF programs are lazily loaded on the first RegisterProcess call.
// In external reader mode (SetExternalLogsMap), logs go to a shared ring buffer
// consumed by odigosebpfreceiver instead of being exported directly.
type Manager struct {
	mu              sync.Mutex
	logCapture      *logs.LogCapture
	externalLogsMap *ebpf.Map
	started         bool

	currentAttrs map[uint32]string // pid → packed attrs
	attrSubs     []chan<- string   // subscriber channels
	attrSubsMu   sync.Mutex        // separate from mu to avoid deadlock
}

// Must be called before the first RegisterProcess.
func (m *Manager) SetExternalLogsMap(logsMap *ebpf.Map) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.externalLogsMap = logsMap
}

func (m *Manager) start(ctx context.Context) error {
	logger := slog.With("subsystem", "ebpflogcapture")

	captureOpts := []logs.Option{logs.WithLogger(logger)}
	if m.externalLogsMap != nil {
		captureOpts = append(captureOpts, logs.WithExternalReader(m.externalLogsMap))
	}
	if inode, err := pidNSInode(); err == nil && inode != 0 {
		captureOpts = append(captureOpts, logs.WithPIDNamespace(inode))
	}

	logCapture, err := logs.Instance(captureOpts...)
	if err != nil {
		return fmt.Errorf("load log capture: %w", err)
	}

	m.logCapture = logCapture
	m.started = true

	if m.externalLogsMap != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("Log capture started (external reader mode)")
	} else {
		commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("Log capture started")
	}
	return nil
}

// RegisterProcess adds a PID to log capture, lazily loading BPF programs on the first call.
func (m *Manager) RegisterProcess(ctx context.Context, pid int, packedAttrs string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		if err := m.start(ctx); err != nil {
			return err
		}
	}

	if packedAttrs != "" {
		if m.currentAttrs == nil {
			m.currentAttrs = make(map[uint32]string)
		}
		m.currentAttrs[uint32(pid)] = packedAttrs
		m.notifyAttr(unixfd.EncodeAttrRegister(uint32(pid), packedAttrs))
	}

	if err := m.logCapture.AddProcess(uint32(pid)); err != nil {
		return fmt.Errorf("register process %d: %w", pid, err)
	}

	return nil
}

// UnregisterProcess removes a PID from active log capture, e.g. when a workload dynamically
// disables ebpfLogCapture.
func (m *Manager) UnregisterProcess(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started || m.logCapture == nil {
		return
	}

	if err := m.logCapture.RemoveProcess(uint32(pid)); err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("failed to unregister process from log capture", "pid", pid, "error", err)
	}

	delete(m.currentAttrs, uint32(pid))
	m.notifyAttr(unixfd.EncodeAttrUnregister(uint32(pid)))
}

// pidNSInode reads our PID namespace inode so the BPF CO-RE walk can translate
// kernel TGIDs to the namespace TGIDs that odiglet sees.
func pidNSInode() (uint32, error) {
	self, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return 0, err
	}
	// Format: "pid:[4026531836]"
	start := strings.Index(self, "[")
	end := strings.Index(self, "]")
	if start < 0 || end < 0 || end <= start+1 {
		return 0, fmt.Errorf("unexpected ns/pid format: %s", self)
	}
	ino, err := strconv.ParseUint(self[start+1:end], 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(ino), nil
}

// SubscribeAttrs returns current attributes and a channel for future updates.
func (m *Manager) SubscribeAttrs() (<-chan string, []string) {
	m.mu.Lock()
	snapshot := make([]string, 0, len(m.currentAttrs))
	for pid, attrs := range m.currentAttrs {
		snapshot = append(snapshot, unixfd.EncodeAttrRegister(pid, attrs))
	}
	m.mu.Unlock()

	ch := make(chan string, 64)
	m.attrSubsMu.Lock()
	m.attrSubs = append(m.attrSubs, ch)
	m.attrSubsMu.Unlock()

	return ch, snapshot
}

// notifyAttr fans out to all subscribers, closing slow ones.
func (m *Manager) notifyAttr(line string) {
	m.attrSubsMu.Lock()
	defer m.attrSubsMu.Unlock()

	alive := m.attrSubs[:0]
	for _, ch := range m.attrSubs {
		select {
		case ch <- line:
			alive = append(alive, ch)
		default:
			close(ch)
		}
	}
	m.attrSubs = alive
}

func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return
	}

	m.attrSubsMu.Lock()
	for _, ch := range m.attrSubs {
		close(ch)
	}
	m.attrSubs = nil
	m.attrSubsMu.Unlock()

	if m.logCapture != nil {
		m.logCapture.Close()
	}

	m.started = false
	commonlogger.LoggerCompat().With("subsystem", "ebpflogcapture").Info("Log capture stopped")
}
