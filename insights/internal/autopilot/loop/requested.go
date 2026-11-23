package loop

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/common/autopilot"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// ProbeRequest is one probe an on-demand interrogation asks for.
type ProbeRequest struct {
	InterrogationID string
	// Source is the workload. ContainerName is ignored: a run covers every
	// container.
	Source store.SourceKey
	// Transactions are the run's scope. Call trees are stored per container
	// and transaction, so each entry names both.
	Transactions   []ProbeTransaction
	Round          int
	Revision       string
	Recommendation schema.Recommendation
	// ChangesAllowed is the run's opt-in to custom instrumentation.
	ChangesAllowed bool
	// RunMaxProbes is the run's probe budget.
	RunMaxProbes int
}

// ProbeTransaction is one transaction of a run's scope.
type ProbeTransaction struct {
	ContainerName string
	// TransactionID is the Insights transaction id in decimal, as the trie
	// tables store it.
	TransactionID string
}

// DenyReason says why a requested probe was refused.
type DenyReason string

const (
	DenyChangesNotAllowed DenyReason = "changes_not_allowed"
	// DenyFinalRound is given by the workflow, which knows the round, before
	// it calls RequestProbe. It is here so the set has one home.
	DenyFinalRound          DenyReason = "final_round"
	DenyUnsupportedLanguage DenyReason = "unsupported_language"
	DenyInvalidProbe        DenyReason = "invalid_probe"
	DenyNotInCallGraph      DenyReason = "not_in_call_graph"
	// DenyNotApplicationCode is a target found only as runtime or agent
	// frames. The profiles exporter drops those before a call tree is
	// written, so only rows from an exporter that classifies differently
	// give it.
	DenyNotApplicationCode    DenyReason = "not_application_code"
	DenySourceNotInstrumented DenyReason = "source_not_instrumented"
	DenyRuleDisabled          DenyReason = "rule_disabled"
	DenyRuleNotManaged        DenyReason = "rule_not_managed"
	DenyAlreadyProbed         DenyReason = "already_probed"
	DenyAlreadyRequested      DenyReason = "already_requested"
	DenyWorkloadCap           DenyReason = "workload_cap_reached"
	DenyRunBudget             DenyReason = "run_budget_exceeded"
)

// ProbeDeniedError reports why a requested probe was refused.
type ProbeDeniedError struct {
	Reason DenyReason
	Detail string
}

func (e *ProbeDeniedError) Error() string {
	if e.Detail == "" {
		return "probe denied: " + string(e.Reason)
	}
	return fmt.Sprintf("probe denied: %s: %s", e.Reason, e.Detail)
}

// Block reasons the reconciler records on a live probe it could not place.
const (
	BlockRuleDisabled   = "rule_disabled"
	BlockRuleNotManaged = "rule_not_managed"
	BlockWorkloadCap    = "workload_cap_reached"
	BlockCoveredByRule  = "covered_by_rule"
)

// probeChecks are the checks RequestProbe reports, in the order it runs them.
var probeChecks = []string{
	"changes_allowed",
	"in_call_graph",
	"application_code",
	"not_already_probed",
	"within_workload_cap",
	"within_run_budget",
}

// PassedChecks lists, in order, the checks a probe passed: all of them for
// reason "" (a stored row), those before the failing one otherwise.
func PassedChecks(reason DenyReason) []string {
	var n int
	switch reason {
	case "":
		n = len(probeChecks)
	case DenyChangesNotAllowed:
		n = 0
	case DenyFinalRound, DenyUnsupportedLanguage, DenyInvalidProbe, DenyNotInCallGraph:
		n = 1
	case DenyNotApplicationCode:
		n = 2
	case DenySourceNotInstrumented, DenyRuleDisabled, DenyRuleNotManaged, DenyAlreadyRequested, DenyAlreadyProbed:
		n = 3
	case DenyWorkloadCap:
		n = 4
	case DenyRunBudget:
		n = 5
	}
	return slices.Clone(probeChecks[:n])
}

// ChangeID is the id of the change an interrogation makes for one probe. A
// repeated request of the same run gives the same id.
func ChangeID(interrogationID, language, target string) string {
	h := sha256.Sum256([]byte(interrogationID + "\x00" + language + "\x00" + target))
	return "chg_" + hex.EncodeToString(h[:])[:20]
}

// RuleObjectName is the name of the workload's autopilot InstrumentationRule.
func RuleObjectName(w store.SourceKey) string {
	return autopilotRuleObjectName(w)
}

// requestedProbeStore is the part of the database client that holds probes
// interrogations asked for, and the call trees they are checked against.
// Tests substitute it.
type requestedProbeStore interface {
	GetCallTree(ctx context.Context, source store.SourceKey, transactionID string) ([]store.CallTreeNode, error)
	InsertRequestedProbe(ctx context.Context, p store.RequestedProbe, runMaxProbes int, now time.Time) (store.RequestedProbe, error)
	GetRequestedProbe(ctx context.Context, changeID string) (store.RequestedProbe, bool, error)
	ListRequestedProbes(ctx context.Context, now time.Time) ([]store.RequestedProbe, error)
	MarkRequestedProbesBeingPlaced(ctx context.Context, rows []store.RequestedProbe) (bool, error)
	SettleRequestedProbes(ctx context.Context, read []store.RequestedProbe, placed []string, blocked map[string]string, now time.Time) error
	BlockRequestedProbes(ctx context.Context, ids []string, reason string) error
	EndWorkloadProbes(ctx context.Context, w store.SourceKey, now time.Time) error
}

// requestedProbes returns the store, or nil when there is none: then no probe
// was requested.
func (m *Manager) requestedProbes() requestedProbeStore {
	if m.requested != nil {
		return m.requested
	}
	if m.Store != nil {
		return m.Store
	}
	return nil
}

func (m *Manager) listRequestedProbes(ctx context.Context, now time.Time) ([]store.RequestedProbe, error) {
	ps := m.requestedProbes()
	if ps == nil {
		return nil, nil
	}
	return ps.ListRequestedProbes(ctx, now)
}

// RequestProbe validates one probe and records it as desired state. It writes
// no rule: the reconciler applies it. Asking again for a live probe of the
// same run returns the stored row. A refusal is a *ProbeDeniedError. It fails
// with store.ErrRunNotOpen when the run ended or was stopped with revert in
// the meantime.
func (m *Manager) RequestProbe(ctx context.Context, req ProbeRequest) (store.RequestedProbe, error) {
	ps := m.requestedProbes()
	if ps == nil {
		return store.RequestedProbe{}, errors.New("requested probe store is not configured")
	}
	now := time.Now()
	rec := req.Recommendation
	w := workloadKeyOf(req.Source).source()
	deny := func(reason DenyReason, detail string) (store.RequestedProbe, error) {
		return store.RequestedProbe{}, &ProbeDeniedError{Reason: reason, Detail: detail}
	}

	if !req.ChangesAllowed {
		return deny(DenyChangesNotAllowed, "")
	}
	// Call trees, the only evidence, come from the Java agent.
	if rec.Language != schema.LangJava {
		return deny(DenyUnsupportedLanguage, rec.Language)
	}
	if rec.Probe.ClassName == "" || rec.Probe.MethodName == "" {
		return deny(DenyInvalidProbe, "class and method are required")
	}
	ci, err := rec.ToCustomInstrumentationsAt(now)
	if err == nil {
		err = ci.Verify()
	}
	if err != nil {
		return deny(DenyInvalidProbe, err.Error())
	}
	target := probeTarget(rec)
	probeKey := rec.Language + "|" + target

	tx, matched, err := findInCallGraph(ctx, ps, w, req.Transactions, target)
	if err != nil {
		return store.RequestedProbe{}, err
	}
	if tx == nil {
		if matched {
			return deny(DenyNotApplicationCode, target)
		}
		return deny(DenyNotInCallGraph, target)
	}

	if _, err := instrumentationConfigForSource(ctx, m.Client, w); err != nil {
		if apierrors.IsNotFound(err) {
			return deny(DenySourceNotInstrumented, "")
		}
		return store.RequestedProbe{}, err
	}
	rule, found, err := m.autopilotRule(ctx, w)
	if err != nil {
		return store.RequestedProbe{}, err
	}
	if found {
		if rule.Labels[k8sconsts.OdigosProfilesManagedByLabel] != k8sconsts.OdigosAutopilotLoopManagedByValue {
			return deny(DenyRuleNotManaged, rule.Name)
		}
		if rule.Spec.Disabled {
			return deny(DenyRuleDisabled, rule.Name)
		}
	}

	changeID := ChangeID(req.InterrogationID, rec.Language, target)
	if prev, ok, err := ps.GetRequestedProbe(ctx, changeID); err != nil {
		return store.RequestedProbe{}, err
	} else if ok {
		if prev.Live(now) {
			return prev, nil
		}
		return deny(DenyAlreadyRequested, prev.State)
	}

	others, err := listCustomProbesForSource(ctx, m.Client, w, RuleObjectName(w))
	if err != nil {
		return store.RequestedProbe{}, err
	}
	for _, p := range others {
		if p.Language+"|"+p.Target == probeKey {
			return deny(DenyAlreadyProbed, p.RuleName)
		}
	}
	rows, err := ps.ListRequestedProbes(ctx, now)
	if err != nil {
		return store.RequestedProbe{}, err
	}
	rows = workloadRows(rows, w)
	var ruleProbes []customProbe
	if found {
		ruleProbes = probesFromCustomInstrumentations(rule.Spec.RuleName, rule.Spec.CustomInstrumentations)
	}
	// The rule spec has no owner per probe. A probe in it that no requested
	// row names was placed by the loop; one another run holds is not a reason
	// to refuse, since this run would lose it when that run's row ends.
	if probeKeys(ruleProbes)[probeKey] && !requestedKeys(rows)[probeKey] {
		return deny(DenyAlreadyProbed, rule.Spec.RuleName)
	}

	inEffect := slices.Concat(others, ruleProbes, liveProbes(w, rows, now), []customProbe{{Language: rec.Language, Target: target}})
	if uniqueProbeCount(inEffect) > m.maxProbes() {
		return deny(DenyWorkloadCap, fmt.Sprintf("%d probes", m.maxProbes()))
	}

	stored := storedRecommendations(
		store.SourceKey{Namespace: w.Namespace, WorkloadKind: w.WorkloadKind, WorkloadName: w.WorkloadName, ContainerName: tx.ContainerName},
		tx.TransactionID, req.Revision, []schema.Recommendation{rec})[0]
	stored.FirstSeen = now
	row, err := ps.InsertRequestedProbe(ctx, store.RequestedProbe{
		Recommendation:  stored,
		ChangeID:        changeID,
		InterrogationID: req.InterrogationID,
		Round:           req.Round,
	}, req.RunMaxProbes, now)
	if errors.Is(err, store.ErrRunBudgetUsed) {
		return deny(DenyRunBudget, fmt.Sprintf("%d probes", req.RunMaxProbes))
	}
	return row, err
}

// findInCallGraph looks for target in the call trees of txs, in order, and
// returns the first transaction where it is an app frame: the application's
// code or a library it calls, as autopilot.ClassifyFrame tells them apart
// from runtime and agent frames. matched reports whether it was found at all.
func findInCallGraph(ctx context.Context, ps requestedProbeStore, w store.SourceKey, txs []ProbeTransaction, target string) (*ProbeTransaction, bool, error) {
	matched := false
	for i, tx := range txs {
		source := w
		source.ContainerName = tx.ContainerName
		nodes, err := ps.GetCallTree(ctx, source, tx.TransactionID)
		if err != nil {
			return nil, false, fmt.Errorf("get call tree for %s: %w", tx.TransactionID, err)
		}
		for _, n := range nodes {
			if autopilot.ClassFromFrameName(n.Name) != target {
				continue
			}
			if autopilot.ClassifyFrame(n.Name) == autopilot.FrameKindApp {
				return &txs[i], true, nil
			}
			matched = true
		}
	}
	return nil, matched, nil
}

// ProbesInEffect returns the probes that apply to the workload now or will
// once the reconciler runs: every enabled rule's probes (the loop's own rule
// included) plus live requested rows. It is what an interrogation tells the
// model already exists.
func (m *Manager) ProbesInEffect(ctx context.Context, w store.SourceKey) ([]CustomProbe, error) {
	probes, err := listCustomProbesForSource(ctx, m.Client, w, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows, err := m.listRequestedProbes(ctx, now)
	if err != nil {
		return nil, err
	}
	have := map[customProbe]bool{}
	for _, p := range probes {
		have[p] = true
	}
	for _, p := range liveProbes(w, workloadRows(rows, w), now) {
		if !have[p] {
			have[p] = true
			probes = append(probes, p)
		}
	}
	sortCustomProbes(probes)
	return probes, nil
}

func (m *Manager) autopilotRule(ctx context.Context, w store.SourceKey) (*odigosv1.InstrumentationRule, bool, error) {
	var rule odigosv1.InstrumentationRule
	key := client.ObjectKey{Namespace: env.GetCurrentNamespace(), Name: autopilotRuleObjectName(w)}
	if err := m.Client.Get(ctx, key, &rule); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get instrumentation rule: %w", err)
	}
	return &rule, true, nil
}

// workloadKey is a comparable workload identity. SourceKey carries an
// attributes map and cannot be a map key.
type workloadKey struct{ namespace, kind, name string }

func workloadKeyOf(s store.SourceKey) workloadKey {
	return workloadKey{s.Namespace, s.WorkloadKind, s.WorkloadName}
}

func rowWorkload(p store.RequestedProbe) workloadKey {
	return workloadKey{p.Namespace, p.WorkloadKind, p.WorkloadName}
}

func (k workloadKey) source() store.SourceKey {
	return store.SourceKey{Namespace: k.namespace, WorkloadKind: k.kind, WorkloadName: k.name}
}

// requestedByWorkload groups rows by workload, in the order the workloads first appear.
func requestedByWorkload(rows []store.RequestedProbe) (map[workloadKey][]store.RequestedProbe, []workloadKey) {
	by := map[workloadKey][]store.RequestedProbe{}
	var order []workloadKey
	for _, p := range rows {
		k := rowWorkload(p)
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], p)
	}
	return by, order
}

func workloadRows(rows []store.RequestedProbe, w store.SourceKey) []store.RequestedProbe {
	k := workloadKeyOf(w)
	var out []store.RequestedProbe
	for _, p := range rows {
		if rowWorkload(p) == k {
			out = append(out, p)
		}
	}
	return out
}

// liveProbes are the live rows as probes of the workload's autopilot rule,
// which is where the reconciler puts them.
func liveProbes(w store.SourceKey, rows []store.RequestedProbe, now time.Time) []customProbe {
	var out []customProbe
	for _, p := range rows {
		if p.Live(now) {
			out = append(out, customProbe{RuleName: autopilotWorkloadRuleName(w), Language: p.Language, Target: p.Target})
		}
	}
	return out
}

func probeKeys(probes []customProbe) map[string]bool {
	out := make(map[string]bool, len(probes))
	for _, p := range probes {
		out[p.Language+"|"+p.Target] = true
	}
	return out
}

func requestedKeys(rows []store.RequestedProbe) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, p := range rows {
		out[p.Language+"|"+p.Target] = true
	}
	return out
}

// liveRecommendations returns the probes of the rows that are live at now.
func liveRecommendations(rows []store.RequestedProbe, now time.Time) []store.Recommendation {
	var out []store.Recommendation
	for _, p := range rows {
		if p.Live(now) {
			out = append(out, p.Recommendation)
		}
	}
	return out
}

// mergeByFirstSeen orders loop and requested probes together, so cap slots
// go to the oldest whichever source it came from.
func mergeByFirstSeen(stored, requested []store.Recommendation) []store.Recommendation {
	out := append(slices.Clone(stored), requested...)
	slices.SortStableFunc(out, func(a, b store.Recommendation) int {
		return cmp.Or(a.FirstSeen.Compare(b.FirstSeen), strings.Compare(a.Language, b.Language), strings.Compare(a.Target, b.Target))
	})
	return out
}

// frozenRecommendations is what the loop keeps of its own probes while it
// does not apply recommendations (dry-run, mode off) but a run's probes are
// written into the rule: the stored rows already in the rule, and nothing new.
// A probe some requested row names is the run's, whatever the loop stored,
// because the rule spec does not say who placed it.
func frozenRecommendations(stored []store.Recommendation, rule *odigosv1.InstrumentationRule, found bool, requested []store.RequestedProbe) []store.Recommendation {
	if !found {
		return nil
	}
	inRule := probeKeys(probesFromCustomInstrumentations("", rule.Spec.CustomInstrumentations))
	held := requestedKeys(requested)
	var out []store.Recommendation
	for _, r := range stored {
		k := r.Language + "|" + r.Target
		if inRule[k] && !held[k] {
			out = append(out, r)
		}
	}
	return out
}

// markBeingPlaced flags, before a rule write, the requested rows whose probes
// desired holds. The outcome is settled after the write; if that settle is
// lost and the row then ends, the flag still lists the row, so the next pass
// takes the probe out, in dry-run and mode off too. Flagging bumps each row's
// PlaceGen, in the store and in requested, so this write's settle applies and
// one from an older read does not. It reports false when a row was flagged
// since it was read, and the write must wait for the next pass.
func (m *Manager) markBeingPlaced(ctx context.Context, requested []store.RequestedProbe, desired *instrumentationrules.CustomInstrumentationsWithMetadata) (bool, error) {
	ps := m.requestedProbes()
	if len(requested) == 0 || ps == nil {
		return true, nil
	}
	inRule := probeKeys(probesFromCustomInstrumentations("", desired))
	var marked []int
	var rows []store.RequestedProbe
	for i, p := range requested {
		if inRule[p.Language+"|"+p.Target] {
			marked = append(marked, i)
			rows = append(rows, p)
		}
	}
	ok, err := ps.MarkRequestedProbesBeingPlaced(ctx, rows)
	if !ok || err != nil {
		return false, err
	}
	for _, i := range marked {
		requested[i].PlaceGen++
	}
	return true, nil
}

// settleRequested records which requested probes the rule now holds. A live
// row is placed when its probe is in desired, whichever row put it there;
// otherwise it is blocked because another rule has it or the cap is full. A
// row that is not live is never placed.
func (m *Manager) settleRequested(ctx context.Context, requested []store.RequestedProbe, desired *instrumentationrules.CustomInstrumentationsWithMetadata, others []customProbe, now time.Time) error {
	ps := m.requestedProbes()
	if len(requested) == 0 || ps == nil {
		return nil
	}
	inRule := probeKeys(probesFromCustomInstrumentations("", desired))
	covered := probeKeys(others)
	var placed []string
	blocked := map[string]string{}
	for _, p := range requested {
		if !p.Live(now) {
			continue
		}
		k := p.Language + "|" + p.Target
		switch {
		case inRule[k]:
			placed = append(placed, p.ChangeID)
		case covered[k]:
			blocked[p.ChangeID] = BlockCoveredByRule
		default:
			blocked[p.ChangeID] = BlockWorkloadCap
		}
	}
	return ps.SettleRequestedProbes(ctx, requested, placed, blocked, now)
}

// blockRequested records why a workload's rule was left alone on every row
// read for it, which also keeps the poll from retrying them every interval.
func (m *Manager) blockRequested(ctx context.Context, requested []store.RequestedProbe, reason string) error {
	ps := m.requestedProbes()
	if len(requested) == 0 || ps == nil {
		return nil
	}
	ids := make([]string, 0, len(requested))
	for _, p := range requested {
		ids = append(ids, p.ChangeID)
	}
	return ps.BlockRequestedProbes(ctx, ids, reason)
}

// leaveRule blocks the requested probes of a workload whose rule the loop
// must not write. When the workload is no longer instrumented it ends them
// instead, so their rows do not stay in_rule for good.
func (m *Manager) leaveRule(ctx context.Context, w store.SourceKey, requested []store.RequestedProbe, reason string, now time.Time) error {
	if len(requested) == 0 {
		return nil
	}
	_, err := instrumentationConfigForSource(ctx, m.Client, w)
	if err == nil {
		return m.blockRequested(ctx, requested, reason)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if requested, err = m.endRequested(ctx, w, requested, now); err != nil {
		return err
	}
	return m.settleRequested(ctx, requested, nil, nil, now)
}

// endRequested ends the requested probes of a workload that is no longer
// instrumented, and returns the rows as they now are.
func (m *Manager) endRequested(ctx context.Context, w store.SourceKey, requested []store.RequestedProbe, now time.Time) ([]store.RequestedProbe, error) {
	ps := m.requestedProbes()
	if len(requested) == 0 || ps == nil {
		return requested, nil
	}
	if err := ps.EndWorkloadProbes(ctx, w, now); err != nil {
		return nil, err
	}
	out := slices.Clone(requested)
	for i := range out {
		if out[i].State == store.ProbeApproved {
			out[i].State = store.ProbeReverted
		}
	}
	return out, nil
}

// probeRetry is the poll's backoff for one workload whose rule write failed.
type probeRetry struct {
	failures int
	next     time.Time
}

// probeRetryDelay doubles from probePollInterval per consecutive failure, up
// to tickInterval: a rule write that keeps failing costs what it does in the
// tick.
func probeRetryDelay(failures int) time.Duration {
	d := probePollInterval
	for i := 1; i < failures && d < tickInterval; i++ {
		d *= 2
	}
	return min(d, tickInterval)
}

// reconcilePendingProbes reconciles only the workloads with a requested probe
// that is live and not in its rule, in its rule and no longer live, or being
// placed by a write whose outcome was not recorded. Rows with a block reason
// wait for the next full pass, so a probe that cannot be placed does not cost
// a reconcile every interval.
func (m *Manager) reconcilePendingProbes(ctx context.Context, backoff map[workloadKey]probeRetry, now time.Time) {
	ps := m.requestedProbes()
	if ps == nil || m.Client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, tickInterval)
	defer cancel()
	rows, err := ps.ListRequestedProbes(ctx, now)
	if err != nil {
		m.logWarn("failed to list requested probes", "error", err)
		return
	}
	byWorkload, order := requestedByWorkload(rows)
	var pending []workloadKey
	for _, k := range order {
		if slices.ContainsFunc(byWorkload[k], func(p store.RequestedProbe) bool {
			return (p.BeingPlaced || p.InRule != p.Live(now)) && p.BlockReason == ""
		}) {
			pending = append(pending, k)
		}
	}
	if len(pending) == 0 {
		return
	}
	mode, err := readAutopilotMode(ctx, m.Client)
	if err != nil {
		m.logWarn("failed to read autopilot mode", "error", err)
	}
	for _, k := range pending {
		if b, ok := backoff[k]; ok && now.Before(b.next) {
			continue
		}
		if err := m.reconcileRule(ctx, k.source(), mode, byWorkload[k], now); err != nil {
			b := backoff[k]
			b.failures++
			b.next = now.Add(probeRetryDelay(b.failures))
			backoff[k] = b
			m.logWarn("failed to reconcile autopilot rule for requested probes",
				"namespace", k.namespace,
				"kind", k.kind,
				"name", k.name,
				"retry_in", b.next.Sub(now).String(),
				"error", err,
			)
			continue
		}
		delete(backoff, k)
	}
}
