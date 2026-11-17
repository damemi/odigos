package loop

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// recommendationStore is the part of the ClickHouse client rules are
// reconciled from. Tests substitute it.
type recommendationStore interface {
	ListRecommendations(ctx context.Context, source clickhouse.SourceKey) ([]clickhouse.Recommendation, error)
	ListRecommendedWorkloads(ctx context.Context) ([]clickhouse.SourceKey, error)
}

func (m *Manager) recommendationStore() recommendationStore {
	if m.recommendations != nil {
		return m.recommendations
	}
	return m.ClickHouse
}

// reconcileRules converges each workload's autopilot InstrumentationRule
// to the probes stored in tx_recommendations. It covers every workload with
// stored recommendations plus every rule the loop manages, so a deleted rule
// is recreated and a rule for a removed workload is deleted.
func (m *Manager) reconcileRules(ctx context.Context, mode string) {
	if m.Client == nil {
		return
	}
	workloads, err := m.ruleWorkloads(ctx)
	if err != nil {
		m.logWarn("failed to list workloads for rule reconcile", "error", err)
		return
	}
	for _, w := range workloads {
		if err := m.reconcileRule(ctx, w, mode); err != nil {
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
func (m *Manager) ruleWorkloads(ctx context.Context) ([]clickhouse.SourceKey, error) {
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
	var out []clickhouse.SourceKey
	add := func(w clickhouse.SourceKey) {
		id := workloadID{w.Namespace, w.WorkloadKind, w.WorkloadName}
		if !seen[id] {
			seen[id] = true
			out = append(out, clickhouse.SourceKey{Namespace: w.Namespace, WorkloadKind: w.WorkloadKind, WorkloadName: w.WorkloadName})
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
func ruleWorkload(rule *odigosv1.InstrumentationRule) (clickhouse.SourceKey, bool) {
	if rule.Spec.Scopes == nil || len(rule.Spec.Scopes.Sources) != 1 {
		return clickhouse.SourceKey{}, false
	}
	s := rule.Spec.Scopes.Sources[0]
	return clickhouse.SourceKey{Namespace: s.Namespace, WorkloadKind: string(s.Kind), WorkloadName: s.Name}, true
}

func (m *Manager) reconcileRule(ctx context.Context, w clickhouse.SourceKey, mode string) error {
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
			return nil
		}
		if rule.Spec.Disabled {
			// Disabling the rule is how a user opts a workload out.
			return nil
		}
	}

	if _, err := instrumentationConfigForSource(ctx, m.Client, w); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// The workload is no longer instrumented.
		return m.deleteRule(ctx, &rule, found)
	}
	if !m.AutoApply || mode == autopilotModeOff {
		// Dry-run and off only record recommendations. Existing rules stay.
		return nil
	}

	desired, err := m.desiredProbes(ctx, w, key.Name)
	if err != nil {
		return err
	}
	if desired == nil {
		return m.deleteRule(ctx, &rule, found)
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
		if err := m.Client.Create(ctx, &rule); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// The cache has not seen our earlier create yet. Its event
				// triggers another reconcile.
				return nil
			}
			return fmt.Errorf("create instrumentation rule: %w", err)
		}
		m.logInfo("created autopilot rule", "rule", rule.Name, "probes", probeCount(desired))
		return nil
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
		return nil
	}
	if err := m.Client.Update(ctx, updated); err != nil {
		if apierrors.IsConflict(err) {
			// Stale cache. The newer version's event triggers another reconcile.
			return nil
		}
		return fmt.Errorf("update instrumentation rule: %w", err)
	}
	m.logInfo("updated autopilot rule", "rule", updated.Name, "probes", probeCount(desired))
	return nil
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

// desiredProbes builds the rule's probes from stored recommendations, oldest
// first, leaving out probes another enabled rule already provides and keeping
// the workload within maxProbes. It returns nil when nothing is left.
func (m *Manager) desiredProbes(ctx context.Context, w clickhouse.SourceKey, ruleObject string) (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
	stored, err := m.recommendationStore().ListRecommendations(ctx, w)
	if err != nil {
		return nil, err
	}
	others, err := listCustomProbesForSource(ctx, m.Client, w, ruleObject)
	if err != nil {
		return nil, err
	}
	return buildDesiredProbes(stored, others, m.maxProbes())
}

// buildDesiredProbes fills the slots other rules leave under maxProbes with
// stored recommendations in FirstSeen order, so the oldest probes keep their
// place when the cap is lowered.
func buildDesiredProbes(stored []clickhouse.Recommendation, others []customProbe, maxProbes int) (*instrumentationrules.CustomInstrumentationsWithMetadata, error) {
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
func (m *Manager) probesForAnalysis(ctx context.Context, source clickhouse.SourceKey) ([]customProbe, error) {
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
