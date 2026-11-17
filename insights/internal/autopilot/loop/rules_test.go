package loop

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/common/consts"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
)

const testOdigosNs = "odigos-system"

// fakeWorkload is a comparable workload identity. SourceKey carries an
// attributes map and cannot be a map key.
type fakeWorkload struct{ namespace, kind, name string }

func workloadOf(s clickhouse.SourceKey) fakeWorkload {
	return fakeWorkload{s.Namespace, s.WorkloadKind, s.WorkloadName}
}

type fakeRecommendationStore map[fakeWorkload][]clickhouse.Recommendation

func (f fakeRecommendationStore) ListRecommendations(_ context.Context, source clickhouse.SourceKey) ([]clickhouse.Recommendation, error) {
	return f[workloadOf(source)], nil
}

func (f fakeRecommendationStore) ListRecommendedWorkloads(context.Context) ([]clickhouse.SourceKey, error) {
	var out []clickhouse.SourceKey
	for k := range f {
		out = append(out, clickhouse.SourceKey{Namespace: k.namespace, WorkloadKind: k.kind, WorkloadName: k.name})
	}
	return out, nil
}

var (
	checkout   = clickhouse.SourceKey{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "checkout"}
	firstSeen  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	storedAuth = clickhouse.Recommendation{
		FirstSeen: firstSeen, Language: "java", Target: "com.acme.Auth.check",
		Category: "security", Function: "com.acme.Auth.check", Why: "auth",
		ClassName: "com.acme.Auth", MethodName: "check",
	}
	storedPrice = clickhouse.Recommendation{
		FirstSeen: firstSeen.Add(time.Minute), Language: "java", Target: "com.acme.Price.total",
		Category: "performance", Function: "com.acme.Price.total", Why: "pricing",
		ClassName: "com.acme.Price", MethodName: "total",
	}
)

func newRuleTestManager(t *testing.T, store fakeRecommendationStore, objs ...client.Object) (*Manager, client.Client) {
	t.Helper()
	t.Setenv(consts.CurrentNamespaceEnvVar, testOdigosNs)
	scheme := runtime.NewScheme()
	if err := odigosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ic := &odigosv1.InstrumentationConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{ic}, objs...)...).Build()
	return &Manager{Client: c, AutoApply: true, recommendations: store}, c
}

func getRule(t *testing.T, c client.Client) (*odigosv1.InstrumentationRule, bool) {
	t.Helper()
	var rule odigosv1.InstrumentationRule
	err := c.Get(context.Background(), client.ObjectKey{Namespace: testOdigosNs, Name: autopilotRuleObjectName(checkout)}, &rule)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return &rule, true
}

func javaTargets(rule *odigosv1.InstrumentationRule) []string {
	var out []string
	for _, p := range rule.Spec.CustomInstrumentations.Java {
		out = append(out, p.String())
	}
	return out
}

func TestReconcileRulesCreatesFromStore(t *testing.T) {
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth, storedPrice}})
	m.reconcileRules(context.Background(), "")

	rule, ok := getRule(t, c)
	if !ok {
		t.Fatal("rule not created")
	}
	if got := javaTargets(rule); len(got) != 2 || got[0] != "com.acme.Auth.check" || got[1] != "com.acme.Price.total" {
		t.Fatalf("probes = %v, want oldest first", got)
	}
	if got := rule.Spec.CustomInstrumentations.Java[0].AddedAt; got != firstSeen.Format(time.RFC3339) {
		t.Fatalf("AddedAt = %q, want FirstSeen", got)
	}
	if rule.Labels[k8sconsts.OdigosProfilesManagedByLabel] != k8sconsts.OdigosAutopilotLoopManagedByValue {
		t.Fatalf("labels = %v", rule.Labels)
	}
	if w, ok := ruleWorkload(rule); !ok || workloadOf(w) != workloadOf(checkout) {
		t.Fatalf("scope workload = %+v", w)
	}
}

func TestReconcileRulesRecreatesDeletedRule(t *testing.T) {
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}})
	m.reconcileRules(context.Background(), "")
	rule, _ := getRule(t, c)
	if err := c.Delete(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	m.reconcileRules(context.Background(), "")
	if _, ok := getRule(t, c); !ok {
		t.Fatal("deleted rule was not recreated")
	}
}

func TestReconcileRulesRevertsDriftAndIsIdempotent(t *testing.T) {
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth, storedPrice}})
	m.reconcileRules(context.Background(), "")
	rule, _ := getRule(t, c)
	rule.Spec.CustomInstrumentations.Java = rule.Spec.CustomInstrumentations.Java[:1]
	if err := c.Update(context.Background(), rule); err != nil {
		t.Fatal(err)
	}

	m.reconcileRules(context.Background(), "")
	rule, _ = getRule(t, c)
	if got := javaTargets(rule); len(got) != 2 {
		t.Fatalf("probes = %v, want drift reverted", got)
	}

	version := rule.ResourceVersion
	m.reconcileRules(context.Background(), "")
	rule, _ = getRule(t, c)
	if rule.ResourceVersion != version {
		t.Fatal("reconcile of an up-to-date rule wrote it again")
	}
}

func TestReconcileRulesLeavesDisabledRule(t *testing.T) {
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth, storedPrice}})
	m.reconcileRules(context.Background(), "")
	rule, _ := getRule(t, c)
	rule.Spec.Disabled = true
	rule.Spec.CustomInstrumentations.Java = nil
	if err := c.Update(context.Background(), rule); err != nil {
		t.Fatal(err)
	}

	m.reconcileRules(context.Background(), "")
	rule, _ = getRule(t, c)
	if !rule.Spec.Disabled || len(rule.Spec.CustomInstrumentations.Java) != 0 {
		t.Fatalf("disabled rule was changed: %+v", rule.Spec)
	}
}

func TestReconcileRulesDeletesRuleForUninstrumentedWorkload(t *testing.T) {
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}})
	m.reconcileRules(context.Background(), "")
	ic := &odigosv1.InstrumentationConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"}}
	if err := c.Delete(context.Background(), ic); err != nil {
		t.Fatal(err)
	}
	// The store was cleared too, as DeleteSource does; the rule is found by label.
	m.recommendations = fakeRecommendationStore{}

	m.reconcileRules(context.Background(), "")
	if _, ok := getRule(t, c); ok {
		t.Fatal("rule for uninstrumented workload was not deleted")
	}
}

func TestReconcileRulesDryRunAndOffDoNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoApply bool
		mode      string
	}{
		{"dry-run", false, ""},
		{"off", true, autopilotModeOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}})
			m.AutoApply = tc.autoApply
			m.reconcileRules(context.Background(), tc.mode)
			if _, ok := getRule(t, c); ok {
				t.Fatal("rule created")
			}
		})
	}
}

func TestReconcileRulesSkipsProbesOtherRulesProvide(t *testing.T) {
	userRule := &odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Namespace: testOdigosNs, Name: "user-rule"},
		Spec: odigosv1.InstrumentationRuleSpec{
			Scopes: workloadScopes(checkout),
			CustomInstrumentations: &instrumentationrules.CustomInstrumentationsWithMetadata{
				Java: []instrumentationrules.JavaCustomProbeWithMetadata{{
					JavaCustomProbe: instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Auth", MethodName: "check"},
				}},
			},
		},
	}
	m, c := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth, storedPrice}}, userRule)
	m.reconcileRules(context.Background(), "")
	rule, _ := getRule(t, c)
	if got := javaTargets(rule); len(got) != 1 || got[0] != "com.acme.Price.total" {
		t.Fatalf("probes = %v, want only the uncovered probe", got)
	}

	// Once the user rule covers everything, the autopilot rule goes away.
	m.recommendations = fakeRecommendationStore{workloadOf(checkout): {storedAuth}}
	m.reconcileRules(context.Background(), "")
	if _, ok := getRule(t, c); ok {
		t.Fatal("rule with no remaining probes was not deleted")
	}
}

func TestProbesForAnalysisUsesStoreNotOwnRule(t *testing.T) {
	m, _ := newRuleTestManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}})
	m.AutoApply = false // dry-run: no rule exists, but the model must still see the stored probe
	source := checkout
	source.ContainerName = "app"
	probes, err := m.probesForAnalysis(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 || probes[0].Target != "com.acme.Auth.check" || probes[0].RuleName != autopilotWorkloadRuleName(checkout) {
		t.Fatalf("probes = %+v", probes)
	}
}

func TestBuildDesiredProbesRespectsCap(t *testing.T) {
	stored := []clickhouse.Recommendation{storedAuth, storedPrice}

	got, err := buildDesiredProbes(stored, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if probeCount(got) != 1 || got.Java[0].ClassName != storedAuth.ClassName {
		t.Fatalf("cap 1 should keep only the oldest probe, got %+v", got)
	}

	others := []customProbe{{RuleName: "user", Language: "java", Target: "com.acme.Other.run"}}
	got, err = buildDesiredProbes(stored, others, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("other rules fill the cap, want no probes, got %+v", got)
	}

	got, err = buildDesiredProbes(stored, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if probeCount(got) != 2 {
		t.Fatalf("zero cap uses the default, want 2 probes, got %d", probeCount(got))
	}
}
