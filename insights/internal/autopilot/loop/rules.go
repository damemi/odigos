package loop

import (
	"context"
	"fmt"
	"slices"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// recommendationStore is the part of the autopilot store rules are
// reconciled from. Tests substitute it.
type recommendationStore interface {
	ListRecommendations(ctx context.Context, source store.SourceKey) ([]store.Recommendation, error)
	ListRecommendedWorkloads(ctx context.Context) ([]store.SourceKey, error)
}

func (m *Manager) recommendationStore() recommendationStore {
	if m.recommendations != nil {
		return m.recommendations
	}
	return m.Store
}

// reconcileRules converges each workload's autopilot InstrumentationRule
// to the probes stored in autopilot_recommendations plus the live probes
// interrogations requested. It covers every workload with stored
// recommendations or requested probes plus every rule the loop manages, so a
// deleted rule is recreated and a rule for a removed workload is deleted.
func (m *Manager) reconcileRules(ctx context.Context, mode string) {
	if m.Client == nil {
		return
	}
	now := time.Now()
	requested, err := m.listRequestedProbes(ctx, now)
	if err != nil {
		// Reconciling without them would take every requested probe out of its rule.
		m.logWarn("failed to list requested probes for rule reconcile", "error", err)
		return
	}
	workloads, err := m.ruleWorkloads(ctx)
	if err != nil {
		m.logWarn("failed to list workloads for rule reconcile", "error", err)
		return
	}
	byWorkload, requestedWorkloads := requestedByWorkload(requested)
	for _, k := range requestedWorkloads {
		if !slices.ContainsFunc(workloads, func(w store.SourceKey) bool { return workloadKeyOf(w) == k }) {
			workloads = append(workloads, k.source())
		}
	}
	for _, w := range workloads {
		if err := m.reconcileRule(ctx, w, mode, byWorkload[workloadKeyOf(w)], now); err != nil {
			m.logWarn("failed to reconcile autopilot rule",
				"namespace", w.Namespace,
				"kind", w.WorkloadKind,
				"name", w.WorkloadName,
				"error", err,
			)
		}
	}
}

// ruleWorkloads returns the union of workloads with stored recommendations
// and workloads named by managed rules, as container-less keys.
func (m *Manager) ruleWorkloads(ctx context.Context) ([]store.SourceKey, error) {
	stored, err := m.recommendationStore().ListRecommendedWorkloads(ctx)
	if err != nil {
		return nil, err
	}
	var rules odigosv1.InstrumentationRuleList
	if err := m.Client.List(ctx, &rules,
		client.InNamespace(env.GetCurrentNamespace()),
		client.MatchingLabels{k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosAutopilotLoopManagedByValue},
	); err != nil {
		return nil, fmt.Errorf("list autopilot rules: %w", err)
	}

	// SourceKey carries an attributes map and is not comparable, so dedupe on
	// the workload identity.
	type workloadID struct{ namespace, kind, name string }
	seen := map[workloadID]bool{}
	var out []store.SourceKey
	add := func(w store.SourceKey) {
		id := workloadID{w.Namespace, w.WorkloadKind, w.WorkloadName}
		if !seen[id] {
			seen[id] = true
			out = append(out, store.SourceKey{Namespace: w.Namespace, WorkloadKind: w.WorkloadKind, WorkloadName: w.WorkloadName})
		}
	}
	for _, w := range stored {
		add(w)
	}
	for i := range rules.Items {
		if w, ok := ruleWorkload(&rules.Items[i]); ok {
			add(w)
		}
	}
	return out, nil
}

// ruleWorkload reads the workload a managed rule targets from its scope.
func ruleWorkload(rule *odigosv1.InstrumentationRule) (store.SourceKey, bool) {
	if rule.Spec.Scopes == nil || len(rule.Spec.Scopes.Sources) != 1 {
		return store.SourceKey{}, false
	}
	s := rule.Spec.Scopes.Sources[0]
	return store.SourceKey{Namespace: s.Namespace, WorkloadKind: string(s.Kind), WorkloadName: s.Name}, true
}

// reconcileRule writes one workload's rule. requested are the workload's
// requested probes that are live or still in the rule; they go into the rule
// even in dry-run and mode off, and each path that settles the rule records
// which of them it holds.
func (m *Manager) reconcileRule(ctx context.Context, w store.SourceKey, mode string, requested []store.RequestedProbe, now time.Time) error {
	key := client.ObjectKey{Namespace: env.GetCurrentNamespace(), Name: autopilotRuleObjectName(w)}
	var rule odigosv1.InstrumentationRule
	found := true
	if err := m.Client.Get(ctx, key, &rule); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get instrumentation rule: %w", err)
		}
		found = false
	}
	if found {
		if rule.Labels[k8sconsts.OdigosProfilesManagedByLabel] != k8sconsts.OdigosAutopilotLoopManagedByValue {
			// Same name, but not ours. Leave it alone.
			return m.leaveRule(ctx, w, requested, BlockRuleNotManaged, now)
		}
		if rule.Spec.Disabled {
			// Disabling the rule is how a user opts a workload out.
			return m.leaveRule(ctx, w, requested, BlockRuleDisabled, now)
		}
	}

	if _, err := instrumentationConfigForSource(ctx, m.Client, w); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// The workload is no longer instrumented.
		requested, err = m.endRequested(ctx, w, requested, now)
		if err != nil {
			return err
		}
		if err := m.deleteRule(ctx, &rule, found); err != nil {
			return err
		}
		return m.settleRequested(ctx, requested, nil, nil, now)
	}
	loopApplies := m.AutoApply && mode != autopilotModeOff
	if !loopApplies && len(requested) == 0 {
		// Dry-run and off only record recommendations. Existing rules stay.
		return nil
	}

	desired, others, err := m.desiredProbes(ctx, w, key.Name, &rule, found, requested, loopApplies, now)
	if err != nil {
		return err
	}
	if desired == nil {
		if err := m.deleteRule(ctx, &rule, found); err != nil {
			return err
		}
		return m.settleRequested(ctx, requested, nil, others, now)
	}

	if !found {
		rule = odigosv1.InstrumentationRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				Labels:    autopilotRuleLabels(w),
			},
			Spec: odigosv1.InstrumentationRuleSpec{
				RuleName:               autopilotWorkloadRuleName(w),
				CustomInstrumentations: desired,
				Scopes:                 workloadScopes(w),
			},
		}
		if ok, err := m.markBeingPlaced(ctx, requested, desired); !ok || err != nil {
			return err
		}
		if err := m.Client.Create(ctx, &rule); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// The cache has not seen our earlier create yet. Its event
				// triggers another reconcile.
				return nil
			}
			return fmt.Errorf("create instrumentation rule: %w", err)
		}
		m.logInfo("created autopilot rule", "rule", rule.Name, "probes", probeCount(desired))
		return m.settleRequested(ctx, requested, desired, others, now)
	}

	updated := rule.DeepCopy()
	updated.Spec.RuleName = autopilotWorkloadRuleName(w)
	updated.Spec.CustomInstrumentations = desired
	updated.Spec.Scopes = workloadScopes(w)
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for k, v := range autopilotRuleLabels(w) {
		updated.Labels[k] = v
	}
	if apiequality.Semantic.DeepEqual(rule.Spec, updated.Spec) && apiequality.Semantic.DeepEqual(rule.Labels, updated.Labels) {
		return m.settleRequested(ctx, requested, desired, others, now)
	}
	if ok, err := m.markBeingPlaced(ctx, requested, desired); !ok || err != nil {
		return err
	}
	if err := m.Client.Update(ctx, updated); err != nil {
		if apierrors.IsConflict(err) {
			// Stale cache. The newer version's event triggers another reconcile.
			return nil
		}
		return fmt.Errorf("update instrumentation rule: %w", err)
	}
	m.logInfo("updated autopilot rule", "rule", updated.Name, "probes", probeCount(desired))
	return m.settleRequested(ctx, requested, desired, others, now)
}

func (m *Manager) deleteRule(ctx context.Context, rule *odigosv1.InstrumentationRule, found bool) error {
	if !found {
		return nil
	}
	if err := m.Client.Delete(ctx, rule); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete instrumentation rule: %w", err)
	}
	m.logInfo("deleted autopilot rule", "rule", rule.Name)
	return nil
}

// desiredProbes builds the rule's probes from stored recommendations and live
// requested probes, oldest first, leaving out probes another enabled rule
// already provides and keeping the workload within maxProbes. When the loop
// does not apply its recommendations, only those already in the rule stay.
// It returns nil when nothing is left, and the probes other rules provide.
func (m *Manager) desiredProbes(ctx context.Context, w store.SourceKey, ruleObject string, rule *odigosv1.InstrumentationRule, found bool,
	requested []store.RequestedProbe, loopApplies bool, now time.Time) (*instrumentationrules.CustomInstrumentationsWithMetadata, []customProbe, error) {
	stored, err := m.recommendationStore().ListRecommendations(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	if !loopApplies {
		stored = frozenRecommendations(stored, rule, found, requested)
	}
	others, err := listCustomProbesForSource(ctx, m.Client, w, ruleObject)
	if err != nil {
		return nil, nil, err
	}
	desired, err := buildDesiredProbes(mergeByFirstSeen(stored, liveRecommendations(requested, now)), others, m.maxProbes())
	return desired, others, err
}

// buildDesiredProbes fills the slots other rules leave under maxProbes with
// stored recommendations in FirstSeen order, so the oldest probes keep their
// place when the cap is lowered.
func buildDesiredProbes(stored []store.Recommendation, others []customProbe, maxProbes int) (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
	if maxProbes < 1 {
		maxProbes = defaultMaxProbesPerWorkload
	}
	covered := make(map[string]bool, len(others))
	for _, p := range others {
		covered[p.Language+"|"+p.Target] = true
	}
	room := probeSlotsAvailable(others, maxProbes)
	out := &instrumentationrules.CustomInstrumentationsWithMetadata{}
	for _, r := range stored {
		if room == 0 {
			break
		}
		if covered[r.Language+"|"+r.Target] {
			continue
		}
		covered[r.Language+"|"+r.Target] = true
		room--
		ci, err := recommendationFromStored(r).ToCustomInstrumentationsAt(r.FirstSeen)
		if err != nil {
			return nil, fmt.Errorf("stored recommendation %s %s: %w", r.Language, r.Target, err)
		}
		out.Java = append(out.Java, ci.Java...)
		out.Golang = append(out.Golang, ci.Golang...)
		out.Php = append(out.Php, ci.Php...)
		out.Cpp = append(out.Cpp, ci.Cpp...)
	}
	if probeCount(out) == 0 {
		return nil, nil
	}
	if err := out.Verify(); err != nil {
		return nil, err
	}
	return out, nil
}

func probeCount(ci *instrumentationrules.CustomInstrumentationsWithMetadata) int {
	if ci == nil {
		return 0
	}
	return len(ci.Java) + len(ci.Golang) + len(ci.Php) + len(ci.Cpp)
}

// probesForAnalysis is what the model is told already exists: probes from
// other rules plus every stored recommendation, applied or not. Reading our
// own probes from the store rather than our rule keeps the list right while
// the rule is missing or in dry-run.
func (m *Manager) probesForAnalysis(ctx context.Context, source store.SourceKey) ([]customProbe, error) {
	others, err := listCustomProbesForSource(ctx, m.Client, source, autopilotRuleObjectName(source))
	if err != nil {
		return nil, err
	}
	stored, err := m.recommendationStore().ListRecommendations(ctx, source)
	if err != nil {
		return nil, err
	}
	ruleName := autopilotWorkloadRuleName(source)
	for _, r := range stored {
		others = append(others, customProbe{RuleName: ruleName, Language: r.Language, Target: r.Target})
	}
	sortCustomProbes(others)
	return others, nil
}
