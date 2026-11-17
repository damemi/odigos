package odigosautopilotprofilesexporter

import (
	"github.com/odigos-io/odigos/common/autopilot"
)

// maxFramesPerSample caps stack depth written per profile sample after filtering.
const maxFramesPerSample = 64

// IsRuntimeProbeFrame reports frames that should not be stored in autopilot trie
// evidence (JDK, VM stubs, agent plumbing). Library frames are not runtime frames: they
// are kept, collapsed per run. Align with autopilot loop rollup.
func IsRuntimeProbeFrame(name string) bool {
	return autopilot.IsRuntimeFrame(name)
}

// capFrames fits leaf-first frames into limit. Library frames are dropped root-most
// first; if app frames alone exceed limit, the leaf-most limit app frames are kept.
func capFrames(names, types []string, kinds []autopilot.FrameKind, limit int) ([]string, []string) {
	if limit <= 0 || len(names) <= limit {
		return names, types
	}
	drop := make([]bool, len(names))
	excess := len(names) - limit
	for i := len(names) - 1; i >= 0 && excess > 0; i-- {
		if kinds[i] == autopilot.FrameKindLibrary {
			drop[i] = true
			excess--
		}
	}
	outNames := make([]string, 0, limit)
	outTypes := make([]string, 0, limit)
	for i := range names {
		if !drop[i] {
			outNames = append(outNames, names[i])
			outTypes = append(outTypes, types[i])
		}
	}
	if len(outNames) > limit {
		outNames, outTypes = outNames[:limit], outTypes[:limit]
	}
	return outNames, outTypes
}
