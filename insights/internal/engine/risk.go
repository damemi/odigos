package engine

import (
	"encoding/json"
	"math"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// assessRisk turns the deviated classes and their evidence into a risk
// assessment via the shared catalog. Set classes score at full confidence
// (novelty is binary); metric classes (latency, payload) scale confidence by
// how far past the learned p99 they landed. The policy's SignalWeights override
// catalog weights (uniformly over class + enricher signals, keyed by source; 0
// mutes) and its EnricherLists tune what enrichers flag. Called by
// Engine.processOne to gate firing; the score is also persisted for
// explainability.
func (e *Engine) assessRisk(deviated []repo.DeviationClass, evidence map[string]json.RawMessage, policy repo.Policy) risk.Assessment {
	signals := make([]risk.Signal, 0, len(deviated))
	for _, class := range deviated {
		confidence := confidenceForClass(class, evidence[string(class)])
		signal, ok := risk.NewSignal(class, confidence, titleFor(class))
		if !ok {
			continue // class not in the catalog (placeholder) - skip, don't zero-score
		}
		signals = append(signals, signal)
	}
	// Enrichers add signals the flat class→weight mapping can't express (e.g. a
	// new DB access being a destructive op vs a benign read), so a dangerous
	// sub-case outscores a generic deviation of the same class.
	signals = append(signals, e.enrichRisk(deviated, evidence, enrichConfig{lists: policy.EnricherLists})...)
	// Apply the policy's per-scope weight overrides once, over class + enricher
	// signals alike (keyed by Source).
	signals = risk.ApplyWeightOverrides(signals, policy.SignalWeights)
	return risk.Assess(signals)
}

// magnitudeSaturation is the multiple of p99 at which a metric class reaches
// full confidence: ~0 at 1x p99, 1.0 at >= 10x.
const magnitudeSaturation = 10.0

// confidenceForClass returns a 0-1 confidence for a class from its evidence.
// Metric classes (D3, D8) derive it from magnitude; everything else is binary
// novelty at full confidence. Used by assessRisk.
func confidenceForClass(class repo.DeviationClass, evidence json.RawMessage) float64 {
	switch class {
	case repo.D3Latency:
		var decoded struct {
			Us  int64 `json:"latency_us"`
			P99 int64 `json:"latency_p99"`
		}
		_ = json.Unmarshal(evidence, &decoded)
		return magnitudeConfidence(decoded.Us, decoded.P99)
	case repo.D8PayloadSize:
		var decoded struct {
			ReqSize  int64 `json:"request_body_size"`
			ReqP99   int64 `json:"request_p99"`
			RespSize int64 `json:"response_body_size"`
			RespP99  int64 `json:"response_p99"`
		}
		_ = json.Unmarshal(evidence, &decoded)
		// Take the worse of the two sides; either can drive the deviation.
		return math.Max(
			magnitudeConfidence(decoded.ReqSize, decoded.ReqP99),
			magnitudeConfidence(decoded.RespSize, decoded.RespP99),
		)
	default:
		return 1.0
	}
}

// magnitudeConfidence scales linearly from 0 at the baseline p99 to 1.0 at
// magnitudeSaturation x p99. A missing/degenerate baseline falls back to full
// confidence so a real deviation is never under-weighted by absent data.
func magnitudeConfidence(observed, p99 int64) float64 {
	if observed <= 0 || p99 <= 0 {
		return 1.0
	}
	ratio := float64(observed) / float64(p99)
	if ratio <= 1 {
		return 0
	}
	confidence := (ratio - 1) / (magnitudeSaturation - 1)
	if confidence > 1 {
		return 1
	}
	return confidence
}
