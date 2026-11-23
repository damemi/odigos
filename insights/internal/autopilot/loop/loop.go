// Package loop owns the autopilot control loop that drives function
// instrumentation decisions from stored call-tree evidence and InstrumentationRules.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/autopilot/model"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

const tickInterval = time.Minute

// tickTimeout bounds one tick and may exceed tickInterval: ticks run
// sequentially and the ticker drops ticks missed while one is running.
const tickTimeout = 10 * time.Minute

// probePollInterval is how often the reconciler looks for requested probes
// whose rule membership is out of date. Requests, reverts and expiry come
// from other goroutines, other replicas and the clock, so nothing signals them.
const probePollInterval = 2 * time.Second

// CallTreeFlusher copies call trees buffered in Redis into the autopilot store.
type CallTreeFlusher interface {
	Flush(ctx context.Context)
}

// Manager coordinates the autopilot loop.
type Manager struct {
	Client    client.Client
	Store     *store.Client
	Log       *commonlogger.OdigosLogger
	Model     *model.Client
	ModelName string
	AutoApply bool
	// MaxProbesPerWorkload caps how many distinct custom probes may apply to
	// one workload. Zero uses defaultMaxProbesPerWorkload.
	MaxProbesPerWorkload int
	// CallTreeFlusher, when set, runs at the start of each tick so call trees
	// buffered in Redis reach the store before sources are analyzed.
	CallTreeFlusher CallTreeFlusher
	// RuleEvents, when set, signals that an InstrumentationRule changed. The
	// loop then reconciles rules without waiting for the next tick, so a
	// deleted autopilot rule is recreated promptly.
	RuleEvents <-chan struct{}

	retryOnce sync.Once
	retry     *retryTracker
	// recommendations overrides Store as the recommendation store in tests.
	recommendations recommendationStore
	// requested overrides the database as the requested-probe store in tests.
	requested requestedProbeStore
	// reconcileNow asks the reconcile goroutine for a full pass. Run creates it.
	reconcileNow chan struct{}
}

// Run blocks until ctx is cancelled. Every minute it discovers sources that
// have stored call trees and, for each source+transaction, sends the
// model the call-graph edges it has not yet successfully analyzed. Edges are
// acknowledged only by a successful run, so failed or skipped calls are
// retried on later ticks.
func (m *Manager) Run(ctx context.Context) {
	m.logInfo("autopilot loop started",
		"interval", tickInterval.String(),
		"tick_timeout", tickTimeout.String(),
		"auto_apply", m.AutoApply,
		"max_probes_per_workload", m.maxProbes(),
	)

	// A tick can hold this goroutine for minutes of model calls, and a probe
	// an interrogation requested cannot wait that long for its rule.
	m.reconcileNow = make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Go(func() { m.runReconciler(ctx) })
	defer wg.Wait()

	m.tick(ctx)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.logInfo("autopilot loop stopped")
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// runReconciler is the only goroutine that writes InstrumentationRules: a
// full pass after each tick and on rule events, and in between a poll of the
// workloads whose requested probes are not yet in or out of their rule.
func (m *Manager) runReconciler(ctx context.Context) {
	poll := time.NewTicker(probePollInterval)
	defer poll.Stop()
	backoff := map[workloadKey]probeRetry{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.RuleEvents:
			m.reconcileRulesNow(ctx)
		case <-m.reconcileNow:
			m.reconcileRulesNow(ctx)
		case <-poll.C:
			m.reconcilePendingProbes(ctx, backoff, time.Now())
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	if m.Store == nil {
		m.logWarn("skipping tick: autopilot store is nil")
		return
	}

	tickCtx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	mode, modeErr := readAutopilotMode(tickCtx, m.Client)
	if modeErr != nil {
		m.logWarn("failed to read autopilot mode", "error", modeErr)
	}
	// off still flushes call trees and deletes data for sources that are gone,
	// but does not call the model.
	if m.CallTreeFlusher != nil {
		m.CallTreeFlusher.Flush(tickCtx)
	}

	sources, err := m.Store.ListSourcesWithTransactions(tickCtx)
	if err != nil {
		m.logWarn("failed to list sources with transactions", "error", err)
		return
	}

	var tickSummary sourceTickSummary
	m.logInfo("autopilot tick", "sources", len(sources), "mode", mode)

	for _, source := range sources {
		if !source.K8SComplete() {
			m.logDebug("skipping source without a full kubernetes workload identity",
				"namespace", source.Namespace,
				"kind", source.WorkloadKind,
				"name", source.WorkloadName,
				"container", source.ContainerName)
			continue
		}
		if err := m.processSource(tickCtx, source, &tickSummary, mode); err != nil {
			m.logWarn("failed to process source",
				"namespace", source.Namespace,
				"kind", source.WorkloadKind,
				"name", source.WorkloadName,
				"container", source.ContainerName,
				"error", err,
			)
		}
	}
	tickSummary.log(m)
	// Rules are written by the reconcile goroutine. A pass already asked for
	// covers this tick's recommendations too.
	select {
	case m.reconcileNow <- struct{}{}:
	default:
	}
	if until := m.retries().pausedUntilTime(time.Now()); !until.IsZero() {
		m.logWarn("llm calls paused after quota or auth failure", "until", until.UTC().Format(time.RFC3339))
	}
}

// reconcileRulesNow runs a full rule reconcile. Only the reconcile goroutine
// calls it, so rule writes stay single-writer.
func (m *Manager) reconcileRulesNow(ctx context.Context) {
	if m.Store == nil {
		return
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, tickInterval)
	defer cancel()
	mode, err := readAutopilotMode(reconcileCtx, m.Client)
	if err != nil {
		m.logWarn("failed to read autopilot mode", "error", err)
	}
	m.reconcileRules(reconcileCtx, mode)
}

type sourceTickSummary struct {
	skippedUnchanged int
	skippedAtCap     int
	deferredBackoff  int
	deferredPaused   int
	exhausted        int
	emptyCallTree    int
	llmCalls         int
	analyzeErrors    int
	duplicateTxRows  int
	reanalyzeReasons map[string]int
}

func (s *sourceTickSummary) noteReason(reason string) {
	if s.reanalyzeReasons == nil {
		s.reanalyzeReasons = map[string]int{}
	}
	s.reanalyzeReasons[reason]++
}

func (s sourceTickSummary) log(m *Manager) {
	fields := []any{
		"skipped_unchanged", s.skippedUnchanged,
		"skipped_at_cap", s.skippedAtCap,
		"deferred_backoff", s.deferredBackoff,
		"deferred_paused", s.deferredPaused,
		"exhausted", s.exhausted,
		"empty_call_tree", s.emptyCallTree,
		"llm_calls", s.llmCalls,
		"analyze_errors", s.analyzeErrors,
		"duplicate_tx_rows", s.duplicateTxRows,
	}
	for reason, n := range s.reanalyzeReasons {
		fields = append(fields, "reanalyze_"+reason, n)
	}
	m.logInfo("autopilot tick summary", fields...)
}

func (m *Manager) processSource(ctx context.Context, source store.SourceKey, summary *sourceTickSummary, mode string) error {
	if _, err := instrumentationConfigForSource(ctx, m.Client, source); err != nil {
		if apierrors.IsNotFound(err) {
			if delErr := m.Store.DeleteSource(ctx, source); delErr != nil {
				return fmt.Errorf("delete source data: %w", delErr)
			}
			m.retries().forgetSource(source)
			m.logInfo("deleted source data; instrumentation config not found",
				"namespace", source.Namespace,
				"kind", source.WorkloadKind,
				"name", source.WorkloadName,
			)
			return nil
		}
		return fmt.Errorf("get instrumentation config: %w", err)
	}
	if mode == autopilotModeOff {
		return nil
	}

	txs, err := m.Store.ListTransactions(ctx, source)
	if err != nil {
		return fmt.Errorf("list transactions: %w", err)
	}

	probes, err := m.probesForAnalysis(ctx, source)
	if err != nil {
		return fmt.Errorf("list custom probes: %w", err)
	}

	maxProbes := m.maxProbes()
	if uniqueProbeCount(probes) >= maxProbes {
		m.logInfo("skipping source analysis; probe cap reached",
			"namespace", source.Namespace,
			"kind", source.WorkloadKind,
			"name", source.WorkloadName,
			"container", source.ContainerName,
			"existing_probes", uniqueProbeCount(probes),
			"max_probes", maxProbes,
			"transactions", len(txs),
		)
		summary.skippedAtCap += len(txs)
		return nil
	}

	seenTx := make(map[string]int, len(txs))
	for i, tx := range txs {
		if uniqueProbeCount(probes) >= maxProbes {
			// Cap filled earlier in this tick; remaining txs cannot add probes.
			summary.skippedAtCap += len(txs) - i
			break
		}
		nodes, err := m.Store.GetCallTree(ctx, source, tx.ID)
		if err != nil {
			return fmt.Errorf("get call tree for %s: %w", tx.ID, err)
		}
		if len(nodes) == 0 {
			summary.emptyCallTree++
			continue
		}
		ev := BuildAppEvidence(nodes)
		if len(ev.CallGraph) == 0 {
			summary.emptyCallTree++
			continue
		}
		seenTx[tx.ID]++
		if seenTx[tx.ID] > 1 {
			summary.duplicateTxRows++
			m.logWarn("duplicate transaction row in insights transactions",
				"namespace", source.Namespace,
				"kind", source.WorkloadKind,
				"name", source.WorkloadName,
				"container", source.ContainerName,
				"transaction_id", tx.ID,
				"occurrence", seenTx[tx.ID],
			)
		}
		outcome, reason, err := m.analyzeTransaction(ctx, source, probes, transactionCallTree{Transaction: tx, Nodes: nodes}, ev)
		if err != nil {
			summary.analyzeErrors++
			m.logWarn("failed to analyze transaction",
				"namespace", source.Namespace,
				"kind", source.WorkloadKind,
				"name", source.WorkloadName,
				"container", source.ContainerName,
				"transaction_id", tx.ID,
				"reanalyze_reason", reason,
				"error", err,
			)
			continue
		}
		switch outcome {
		case analyzeSkipped:
			summary.skippedUnchanged++
		case analyzeSkippedAtCap:
			summary.skippedAtCap++
		case analyzeDeferredBackoff:
			summary.deferredBackoff++
		case analyzeDeferredPaused:
			summary.deferredPaused++
		case analyzeExhausted:
			summary.exhausted++
		case analyzeLLM:
			summary.llmCalls++
			summary.noteReason(reason)
			// Refresh probe inventory so later transactions in this tick see
			// the recommendations this analysis stored.
			if refreshed, listErr := m.probesForAnalysis(ctx, source); listErr == nil {
				probes = refreshed
			}
		}
	}

	return nil
}

type analyzeOutcome int

const (
	analyzeSkipped analyzeOutcome = iota
	analyzeSkippedAtCap
	analyzeLLM
	analyzeDryRunEmit
	// analyzeDeferredBackoff: the transaction failed recently and waits.
	analyzeDeferredBackoff
	// analyzeDeferredPaused: model calls are paused (quota, budget, auth).
	analyzeDeferredPaused
	// analyzeExhausted: this exact request kept failing; it waits for new
	// evidence or a new revision.
	analyzeExhausted
)

func (m *Manager) analyzeTransaction(ctx context.Context, source store.SourceKey, probes []customProbe, tx transactionCallTree, ev AppEvidence) (analyzeOutcome, string, error) {
	if uniqueProbeCount(probes) >= m.maxProbes() {
		m.logInfo("skipping transaction; probe cap reached",
			"transaction_id", tx.Transaction.ID,
			"existing_probes", uniqueProbeCount(probes),
			"max_probes", m.maxProbes(),
		)
		return analyzeSkippedAtCap, "", nil
	}

	key := newRetryKey(source, tx.Transaction.ID)
	now := time.Now()
	if until := m.retries().pausedUntilTime(now); !until.IsZero() {
		return analyzeDeferredPaused, "", nil
	}
	if m.retries().backingOff(key, now) {
		return analyzeDeferredBackoff, "", nil
	}

	revision := analysisRevision(m.ModelName)
	last, err := m.Store.LastSuccessfulRun(ctx, source, tx.Transaction.ID, revision)
	if err != nil {
		// Without the acknowledged graph a delta cannot be computed. Retry next
		// tick rather than paying for a full analysis.
		return analyzeSkipped, "", fmt.Errorf("last successful run: %w", err)
	}
	plan := planAnalysis(last, ev)
	if plan.Reason == "" {
		m.logInfo("skipping transaction; no pending evidence",
			"transaction_id", tx.Transaction.ID,
			"call_graph_edges", len(ev.CallGraph),
			"call_tree_nodes_total", len(tx.Nodes),
		)
		return analyzeSkipped, "", nil
	}
	reason := plan.Reason

	req := buildAnalysisRequest(source, probes, tx, plan)
	user, err := schema.MarshalUserMessage(req)
	if err != nil {
		return analyzeLLM, reason, fmt.Errorf("marshal analysis request: %w", err)
	}
	inputHash := hashAnalysisInput(user)
	if m.retries().exhausted(key, inputHash) {
		return analyzeExhausted, reason, nil
	}

	snapshot, err := marshalEvidenceSnapshot(plan.Commit)
	if err != nil {
		return analyzeLLM, reason, fmt.Errorf("marshal evidence snapshot: %w", err)
	}

	run := store.Run{
		Namespace:        source.Namespace,
		WorkloadKind:     source.WorkloadKind,
		WorkloadName:     source.WorkloadName,
		ContainerName:    source.ContainerName,
		TransactionID:    tx.Transaction.ID,
		CallTreeHash:     hashEvidence(plan.Commit),
		EvidenceSnapshot: snapshot,
		Model:            m.ModelName,
		PromptHash:       inputHash,
		AnalysisRevision: revision,
	}

	if !m.Model.Enabled() {
		emitRequest(source, tx.Transaction, user, len(probes), plan.Mode, m)
		return analyzeDryRunEmit, reason, nil
	}

	llmLog := []any{
		"reanalyze_reason", reason,
		"evidence_mode", plan.Mode,
		"namespace", source.Namespace,
		"kind", source.WorkloadKind,
		"name", source.WorkloadName,
		"container", source.ContainerName,
		"transaction_id", tx.Transaction.ID,
		"call_graph_edges", len(ev.CallGraph),
		"sent_edges", len(plan.Send),
		"call_tree_nodes_total", len(tx.Nodes),
		"request_bytes", len(user),
		"model", m.ModelName,
	}
	if last != nil {
		llmLog = append(llmLog, "last_success_at", last.Timestamp.UTC().Format(time.RFC3339))
	}
	m.logInfo("calling llm for transaction analysis", llmLog...)

	// fail records a failed attempt. Nothing is acknowledged, so the same
	// pending edges are sent again once the backoff allows.
	fail := func(class failureClass, retryAfter time.Duration, cause error) (analyzeOutcome, string, error) {
		m.retries().recordFailure(key, inputHash, class, retryAfter, time.Now())
		run.Error = cause.Error()
		if run.AnalysisStatus == "" {
			run.AnalysisStatus = class.analysisStatus()
		}
		if insErr := m.Store.InsertRun(ctx, run); insErr != nil {
			m.logWarn("failed to record failed run", "transaction_id", tx.Transaction.ID, "error", insErr)
		}
		return analyzeLLM, reason, cause
	}

	raw, err := m.Model.CompleteAnalysis(ctx, user)
	if err != nil {
		if isShutdown(ctx, err) {
			return analyzeLLM, reason, err
		}
		class, retryAfter := classifyModelError(err)
		return fail(class, retryAfter, fmt.Errorf("model complete: %w", err))
	}
	run.RawResponse = raw

	parsed, err := schema.Parse([]byte(raw))
	if err != nil {
		return fail(failurePermanent, 0, fmt.Errorf("parse model response: %w", err))
	}
	if parsedJSON, marshalErr := json.Marshal(parsed); marshalErr == nil {
		run.Parsed = string(parsedJSON)
	}
	// Store recommendations before the run acknowledges the evidence, so a
	// crash in between repeats the call instead of losing them.
	if err := m.storeRecommendations(ctx, source, tx.Transaction.ID, revision, parsed.Recommendations); err != nil {
		return fail(failureTransient, 0, fmt.Errorf("store recommendations: %w", err))
	}
	run.AnalysisStatus = store.AnalysisSucceeded
	m.logInfo("analysis succeeded",
		"transaction_id", tx.Transaction.ID,
		"recommendations", len(parsed.Recommendations),
	)

	// The insert is the acknowledgement. If it fails, the next tick sends the
	// same edges again, and storing the same probes again is a no-op. Rules
	// are reconciled from the store at the end of the tick.
	if err := m.Store.InsertRun(ctx, run); err != nil {
		return analyzeLLM, reason, err
	}
	m.retries().recordSuccess(key, time.Now())
	return analyzeLLM, reason, nil
}

// storeRecommendations inserts the probes not already stored for the
// workload. The loop is the only writer, so read-then-insert is safe.
func (m *Manager) storeRecommendations(ctx context.Context, source store.SourceKey, transactionID, revision string, recs []schema.Recommendation) error {
	if len(recs) == 0 {
		return nil
	}
	existing, err := m.Store.ListRecommendations(ctx, source)
	if err != nil {
		return err
	}
	return m.Store.InsertRecommendations(ctx, unstoredRecommendations(storedRecommendations(source, transactionID, revision, recs), existing))
}

func buildAnalysisRequest(source store.SourceKey, probes []customProbe, tx transactionCallTree, plan analysisPlan) schema.AnalysisRequest {
	if plan.Mode == "delta" {
		return buildDeltaAnalysisRequest(source, probes, tx, plan.Send)
	}
	return buildFullAnalysisRequest(source, probes, tx, AppEvidence{CallGraph: plan.Send})
}

// retries returns the backoff tracker, creating it on first use.
func (m *Manager) retries() *retryTracker {
	m.retryOnce.Do(func() {
		if m.retry == nil {
			m.retry = newRetryTracker()
		}
	})
	return m.retry
}

func (m *Manager) maxProbes() int {
	if m.MaxProbesPerWorkload < 1 {
		return defaultMaxProbesPerWorkload
	}
	return m.MaxProbesPerWorkload
}

func emitRequest(source store.SourceKey, tx store.Transaction, user string, probeCount int, evidenceMode string, m *Manager) {
	service := fmt.Sprintf("%s/%s/%s/%s",
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName)
	label := fmt.Sprintf("%s tx=%s", service, tx.ID)

	m.logInfo("built autopilot request",
		"service", service,
		"transaction_id", tx.ID,
		"root_span_name", tx.RootSpanName,
		"custom_probes", probeCount,
		"request_bytes", len(user),
		"schema_version", schema.RequestVersion,
		"evidence_mode", evidenceMode,
		"model_configured", false,
	)

	fmt.Fprintf(os.Stdout, "\n===== AUTOPILOT REQUEST START: %s =====\n", label)
	fmt.Fprint(os.Stdout, user)
	if !strings.HasSuffix(user, "\n") {
		fmt.Fprint(os.Stdout, "\n")
	}
	fmt.Fprintf(os.Stdout, "===== AUTOPILOT REQUEST END: %s =====\n\n", label)
}

// autopilotModeOff matches common.AutopilotModeOff. Any other value,
// including unset, keeps analysis running.
const autopilotModeOff = "off"

// readAutopilotMode returns autopilot.mode from the effective config.
// "off" and "auto" match common.AutopilotMode.
func readAutopilotMode(ctx context.Context, c client.Client) (string, error) {
	if c == nil {
		return "", nil
	}
	var cm corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{
		Namespace: env.GetCurrentNamespace(),
		Name:      consts.OdigosEffectiveConfigName,
	}, &cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	raw := ""
	if cm.Data != nil {
		raw = cm.Data[consts.OdigosConfigurationFileName]
	}
	if raw == "" {
		return "", nil
	}
	var doc struct {
		Autopilot struct {
			Mode string `json:"mode" yaml:"mode"`
		} `json:"autopilot" yaml:"autopilot"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("parse effective config: %w", err)
	}
	return doc.Autopilot.Mode, nil
}

func (m *Manager) logInfo(msg string, keysAndValues ...any) {
	if m.Log != nil {
		m.Log.Info(msg, keysAndValues...)
	}
}

func (m *Manager) logDebug(msg string, keysAndValues ...any) {
	if m.Log != nil {
		m.Log.Debug(msg, keysAndValues...)
	}
}

func (m *Manager) logWarn(msg string, keysAndValues ...any) {
	if m.Log != nil {
		m.Log.Warn(msg, keysAndValues...)
	}
}
