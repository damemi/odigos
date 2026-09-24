// Package metrics holds the insights service's Prometheus instrumentation:
// per-method repository call counts/latency (WrapRepo) and engine throughput,
// so we can track DB calls per sub-transaction.
//
// Collectors use a private registry, not the global default, so /metrics
// carries only this service's signal and tests can import the package without
// duplicate-registration panics.
package metrics

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "insights"

var (
	registry = prometheus.NewRegistry()

	// repoCalls counts every repository method invocation, labelled by method
	// and ok/error. The method label distinguishes hot-path (engine) calls
	// from cold-path (UI/API) calls without a separate metric.
	repoCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "repo",
		Name:      "calls_total",
		Help:      "Total repository method calls, by method and status (ok|error).",
	}, []string{"method", "status"})

	// repoCallDuration times each repository method. Buckets are tuned for
	// DB round-trips (sub-millisecond to a couple of seconds) rather than the
	// Prometheus web-latency defaults.
	repoCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "repo",
		Name:      "call_duration_seconds",
		Help:      "Repository method latency in seconds, by method.",
		Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
	}, []string{"method"})

	// tracesProcessed counts traces handed to the engine (before
	// decomposition). The denominator for per-trace DB cost.
	tracesProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "traces_processed_total",
		Help:      "Total traces handed to the engine, before sub-transaction decomposition.",
	})

	// subtransactionsProcessed counts the sub-transactions the engine scored
	// (one per service-entry span). The denominator for per-sub-transaction
	// DB cost, which is the headline number for this initiative.
	subtransactionsProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "subtransactions_processed_total",
		Help:      "Total sub-transactions processed by the engine (one per service-entry span).",
	})

	// transactionCache counts transaction lookup cache results by hit/miss. A
	// hit returns before the repo (and its UpsertTransaction metric), so a
	// climbing hit ratio is the proof the transaction lookup left the hot path.
	transactionCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "txn_cache",
		Name:      "lookups_total",
		Help:      "Transaction lookup cache results, by result (hit|miss).",
	}, []string{"result"})

	// policyCache counts enabled-policy lookup cache results by hit/miss. A
	// hit returns before the repo (and its ListEnabledPolicies metric), so a
	// climbing hit ratio is the proof the policy lookup left the hot path.
	policyCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "policy_cache",
		Name:      "lookups_total",
		Help:      "Enabled-policy lookup cache results, by result (hit|miss).",
	}, []string{"result"})

	// baselineCache counts in-memory baseline store lookups by result. Same
	// idea as transactionCache for the (much larger) baseline read volume.
	baselineCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "lookups_total",
		Help:      "In-memory baseline store lookups, by result (hit|miss).",
	}, []string{"result"})

	// baselineEvictions counts entries dropped from the in-memory store
	// (always flush-before-evict). A high rate means the LRU cap is too small
	// for the working set.
	baselineEvictions = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "evictions_total",
		Help:      "Baseline store entries evicted by the LRU cap (flushed first).",
	})

	// transactionsDroppedOverCap counts sub-transactions dropped because their
	// service already holds max_transactions_per_service identities. A steady
	// rate means a service emits high-cardinality span names (request ids in
	// paths) or the cap is too low for it. No service label: the offenders are
	// visible on GET /services as transaction_limit_reached, and a label per
	// service would itself be unbounded.
	transactionsDroppedOverCap = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "transactions_dropped_over_cap_total",
		Help:      "Sub-transactions dropped because their service reached max_transactions_per_service.",
	})

	// baselineDirty is the current number of transactions with unflushed
	// deltas waiting for the write-behind flusher.
	baselineDirty = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "dirty_transactions",
		Help:      "Transactions with unflushed deltas pending the write-behind flusher.",
	})

	// baselineResident is the current number of transactions held in the
	// in-memory store (bounded by the LRU cap).
	baselineResident = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "resident_transactions",
		Help:      "Transactions currently resident in the in-memory baseline store.",
	})

	// baselineFlush counts write-behind flush cycles by result, and times
	// them. Confirms DB writes now track flush cadence, not trace volume.
	baselineFlush = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "flushes_total",
		Help:      "Write-behind flush cycles, by result (ok|error).",
	}, []string{"result"})

	baselineFlushDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "baseline_cache",
		Name:      "flush_duration_seconds",
		Help:      "Write-behind flush cycle latency in seconds.",
		Buckets:   []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
	})

	promotionSweeperClasses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "promotion_sweeper_classes_total",
		Help:      "Classes promoted by the time-based promotion sweeper, by source (memory|db).",
	}, []string{"source"})

	// anomalyDuplicatesSuppressed counts the repeat findings an operator never
	// had to triage: a deviation a nested sub-transaction would have reported
	// had an enclosing one in the same trace not already reported the same
	// item. Read against the anomaly count, it says how much of the findings
	// feed the overlapping subtrees would otherwise be repeating.
	anomalyDuplicatesSuppressed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "anomaly_duplicates_suppressed_total",
		Help:      "Deviations suppressed because an enclosing sub-transaction already fired them, by class.",
	}, []string{"class"})

	// samplesDropped counts sample traces (examples / evidence) the background
	// writer could not keep because its queue was full. Samples are best-effort
	// context for a finding, so ingestion is never stalled to store one; a
	// non-zero rate means ClickHouse writes are lagging the sample rate.
	samplesDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "samples_dropped_total",
		Help:      "Sample traces dropped because the background sample writer queue was full.",
	})

	// recommendationMiningRuns counts mining runs per outcome, and
	// recommendationMiningDuration how long one transaction takes to mine. Both
	// run in the miner goroutine, so a rising duration is a storage or sample
	// volume signal, never an ingest one.
	recommendationMiningRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "recommendation_mining_runs_total",
		Help:      "Recommendation mining runs by result (ok|error|empty).",
	}, []string{"result"})
	recommendationMiningDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "recommendation_mining_duration_seconds",
		Help:      "Wall-clock duration of mining one transaction.",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 12),
	})
	recommendationMiningCandidates = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "recommendation_mining_candidates",
		Help:      "Recommendations produced per mining run.",
		Buckets:   []float64{0, 1, 2, 3, 5, 8, 13, 20},
	})
	// recommendationShadow counts live traces evaluated against open
	// recommendations, by whether the relation held. It is the only per-trace
	// cost of recommendations and should stay a small fraction of traces.
	recommendationShadow = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "engine",
		Name:      "recommendation_shadow_evaluations_total",
		Help:      "Live traces evaluated against open recommendations, by outcome (held|broken).",
	}, []string{"outcome"})
)

func init() {
	registry.MustRegister(
		repoCalls, repoCallDuration, tracesProcessed, subtransactionsProcessed, transactionsDroppedOverCap,
		transactionCache, policyCache, baselineCache, baselineEvictions, baselineDirty, baselineResident,
		baselineFlush, baselineFlushDuration, promotionSweeperClasses,
		anomalyDuplicatesSuppressed, samplesDropped,
		recommendationMiningRuns, recommendationMiningDuration, recommendationMiningCandidates, recommendationShadow,
	)
}

// IncSamplesDropped records one sample trace dropped by the background writer.
func IncSamplesDropped() { samplesDropped.Inc() }

// ObserveRecommendationMining records one mining run: its result
// ("ok"|"error"|"empty"), duration, and how many recommendations it produced.
func ObserveRecommendationMining(result string, seconds float64, candidates int) {
	recommendationMiningRuns.WithLabelValues(result).Inc()
	recommendationMiningDuration.Observe(seconds)
	if result != "error" {
		recommendationMiningCandidates.Observe(float64(candidates))
	}
}

// AddRecommendationShadow records live shadow evaluations by outcome.
func AddRecommendationShadow(held, broken int) {
	if held > 0 {
		recommendationShadow.WithLabelValues("held").Add(float64(held))
	}
	if broken > 0 {
		recommendationShadow.WithLabelValues("broken").Add(float64(broken))
	}
}

// Handler serves the insights service's metrics for Prometheus scraping.
// Mount it at GET /metrics.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// IncTracesProcessed records one trace handed to the engine.
func IncTracesProcessed() {
	tracesProcessed.Inc()
}

// AddSubtransactionsProcessed records n sub-transactions scored for one trace.
func AddSubtransactionsProcessed(n int) {
	if n > 0 {
		subtransactionsProcessed.Add(float64(n))
	}
}

func IncTransactionsDroppedOverCap() {
	transactionsDroppedOverCap.Inc()
}

// IncAnomalyDuplicateSuppressed records one deviation suppressed as a duplicate
// of a firing on an enclosing sub-transaction.
func IncAnomalyDuplicateSuppressed(class string) {
	anomalyDuplicatesSuppressed.WithLabelValues(class).Inc()
}

// IncTransactionCache records one transaction lookup cache result ("hit"|"miss").
func IncTransactionCache(result string) { transactionCache.WithLabelValues(result).Inc() }

// IncPolicyCache records one enabled-policy lookup cache result ("hit"|"miss").
func IncPolicyCache(result string) { policyCache.WithLabelValues(result).Inc() }

// IncBaselineCache records one in-memory baseline store lookup ("hit"|"miss").
func IncBaselineCache(result string) { baselineCache.WithLabelValues(result).Inc() }

// AddBaselineEvictions records n entries evicted from the baseline store.
func AddBaselineEvictions(n int) {
	if n > 0 {
		baselineEvictions.Add(float64(n))
	}
}

// SetBaselineDirty publishes the current dirty-transaction backlog.
func SetBaselineDirty(n int) { baselineDirty.Set(float64(n)) }

// SetBaselineResident publishes the current resident-transaction count.
func SetBaselineResident(n int) { baselineResident.Set(float64(n)) }

// ObserveBaselineFlush records one flush cycle: its result ("ok"|"error") and
// wall-clock duration.
func ObserveBaselineFlush(result string, seconds float64) {
	baselineFlush.WithLabelValues(result).Inc()
	baselineFlushDuration.Observe(seconds)
	lastFlushUnixNano.Store(time.Now().UnixNano())
	ok := result == "ok"
	lastFlushOK.Store(ok)
	if ok {
		lastFlushErr.Store("")
	} else {
		lastFlushErr.Store("flush failed")
	}
}

// AddPromotionSweeperClasses records n classes promoted by the time-based sweeper.
func AddPromotionSweeperClasses(source string, n int) {
	if n > 0 {
		promotionSweeperClasses.WithLabelValues(source).Add(float64(n))
	}
}

var (
	lastFlushUnixNano atomic.Int64
	lastFlushOK       atomic.Bool
	lastFlushErr      atomic.Value // string
)

// LastBaselineFlush returns the most recent write-behind flush outcome for storage setting page.
func LastBaselineFlush() (ok bool, at time.Time, errMsg string, ever bool) {
	ns := lastFlushUnixNano.Load()
	if ns == 0 {
		return false, time.Time{}, "", false
	}
	if v := lastFlushErr.Load(); v != nil {
		errMsg, _ = v.(string)
	}
	return lastFlushOK.Load(), time.Unix(0, ns).UTC(), errMsg, true
}
