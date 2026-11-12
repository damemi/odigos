// Package snapshotsummary derives the per-cluster statistics shown on the
// Central connections page from a raw cluster snapshot, so listing every
// connection never has to load or parse every full snapshot.
package snapshotsummary

import (
	"encoding/json"
	"fmt"
	"time"
)

// Summary is the per-cluster rollup stored next to the snapshot it was built from.
type Summary struct {
	Workloads             int `json:"workloads"`
	InstrumentedWorkloads int `json:"instrumentedWorkloads"`
	// WorkloadsByHealth counts instrumented workloads only, keyed by the
	// DesiredStateProgress value the proxy reports. A workload without an
	// InstrumentationConfig always reports Disabled, so counting it here would
	// only restate Workloads - InstrumentedWorkloads.
	WorkloadsByHealth          map[string]int `json:"workloadsByHealth,omitempty"`
	Namespaces                 int            `json:"namespaces"`
	AutoInstrumentedNamespaces int            `json:"autoInstrumentedNamespaces"`
	// SnapshotTimestamp is when the proxy generated the snapshot (unix seconds).
	SnapshotTimestamp int64 `json:"snapshotTimestamp"`
}

// Health values of the OSS frontend's DesiredStateProgress enum, as the proxy reports them.
const (
	healthError   = "Error"
	healthFailure = "Failure"
	healthPending = "Pending"
	healthWaiting = "Waiting"
)

// WorkloadsWithIssues counts instrumented workloads whose health is Error or Failure.
func (s *Summary) WorkloadsWithIssues() int {
	return s.WorkloadsByHealth[healthError] + s.WorkloadsByHealth[healthFailure]
}

// WorkloadsInProgress counts instrumented workloads whose instrumentation is
// still rolling out (Pending or Waiting).
func (s *Summary) WorkloadsInProgress() int {
	return s.WorkloadsByHealth[healthPending] + s.WorkloadsByHealth[healthWaiting]
}

// snapshot is the subset of the proxy's ClusterSnapshot payload a summary reads.
type snapshot struct {
	Timestamp  time.Time `json:"timestamp"`
	Namespaces []struct {
		Selected  bool `json:"selected"`
		Workloads []struct {
			Selected bool   `json:"selected"`
			Health   string `json:"health"`
		} `json:"workloads"`
	} `json:"namespaces"`
}

// Compute builds the summary of a raw snapshot payload. fallbackTimestamp
// (unix seconds) is used when the payload carries no timestamp of its own.
func Compute(data []byte, fallbackTimestamp int64) (*Summary, error) {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot data: %w", err)
	}

	summary := &Summary{
		Namespaces:        len(snap.Namespaces),
		SnapshotTimestamp: fallbackTimestamp,
	}
	if !snap.Timestamp.IsZero() {
		summary.SnapshotTimestamp = snap.Timestamp.Unix()
	}

	for _, ns := range snap.Namespaces {
		if ns.Selected {
			summary.AutoInstrumentedNamespaces++
		}
		for _, wl := range ns.Workloads {
			summary.Workloads++
			if !wl.Selected {
				continue
			}
			summary.InstrumentedWorkloads++
			if summary.WorkloadsByHealth == nil {
				summary.WorkloadsByHealth = make(map[string]int)
			}
			summary.WorkloadsByHealth[wl.Health]++
		}
	}

	return summary, nil
}
