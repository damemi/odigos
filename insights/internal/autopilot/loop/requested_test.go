package loop

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

func javaRec(class, method string) schema.Recommendation {
	return schema.Recommendation{
		Category: "performance", Function: class + "." + method, Language: schema.LangJava, Why: "  slow path ",
		Probe: schema.Probe{ClassName: class, MethodName: method},
	}
}

// probeRequestFixture is checkout with one transaction whose call tree has
// com.acme.Auth.check under Spring's DispatcherServlet, both app frames, and a
// JDK frame under it. Call trees written by the exporter hold no JDK frames;
// the one here stands for a row the classifier does not call app.
func probeRequestFixture(t *testing.T, objs ...client.Object) (*Manager, *fakeRequestedStore, ProbeRequest) {
	t.Helper()
	req := newFakeRequestedStore()
	req.tries["app/1"] = []store.CallTreeNode{
		{ID: "a", Name: "void org.springframework.web.servlet.DispatcherServlet.doService(jakarta.servlet.http.HttpServletRequest)"},
		{ID: "b", ParentID: "a", Name: "boolean com.acme.Auth.check(java.lang.String)"},
		{ID: "c", ParentID: "b", Name: "java.lang.Object java.util.HashMap.get(java.lang.Object)"},
	}
	m, _ := newRequestedRuleManager(t, fakeRecommendationStore{}, req, objs...)
	source := checkout
	source.ContainerName = "ignored"
	return m, req, ProbeRequest{
		InterrogationID: "int-0b6f6c1e",
		Source:          source,
		Transactions:    []ProbeTransaction{{ContainerName: "app", TransactionID: "1"}},
		Round:           2,
		Revision:        "interrogation=1/prompt=7/request=4/model=autopilot",
		Recommendation:  javaRec("com.acme.Auth", "check"),
		ChangesAllowed:  true,
		RunMaxProbes:    5,
	}
}

func autopilotRuleWith(labels map[string]string, disabled bool, probes ...instrumentationrules.JavaCustomProbe) *odigosv1.InstrumentationRule {
	ci := &instrumentationrules.CustomInstrumentationsWithMetadata{}
	for _, p := range probes {
		ci.Java = append(ci.Java, instrumentationrules.JavaCustomProbeWithMetadata{JavaCustomProbe: p})
	}
	return &odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Namespace: testOdigosNs, Name: autopilotRuleObjectName(checkout), Labels: labels},
		Spec: odigosv1.InstrumentationRuleSpec{
			RuleName: autopilotWorkloadRuleName(checkout), Disabled: disabled,
			Scopes: workloadScopes(checkout), CustomInstrumentations: ci,
		},
	}
}

func userRuleWith(probes ...instrumentationrules.JavaCustomProbe) *odigosv1.InstrumentationRule {
	r := autopilotRuleWith(nil, false, probes...)
	r.Name, r.Spec.RuleName = "user-rule", "user-rule"
	return r
}

var managed = map[string]string{k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosAutopilotLoopManagedByValue}

func TestRequestProbe_Denials(t *testing.T) {
	auth := instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Auth", MethodName: "check"}
	other := instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Other", MethodName: "run"}
	for _, tc := range []struct {
		name   string
		objs   []client.Object
		modify func(*Manager, *fakeRequestedStore, *ProbeRequest)
		want   DenyReason
	}{
		{"changes not allowed", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) { r.ChangesAllowed = false }, DenyChangesNotAllowed},
		{"unsupported language", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) {
			r.Recommendation = schema.Recommendation{Language: schema.LangGo, Probe: schema.Probe{PackageName: "main", FunctionName: "run"}}
		}, DenyUnsupportedLanguage},
		{"invalid probe", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) { r.Recommendation.Probe.MethodName = "" }, DenyInvalidProbe},
		{"not in call graph", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) {
			r.Recommendation = javaRec("com.acme.Missing", "run")
		}, DenyNotInCallGraph},
		{"no transactions", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) { r.Transactions = nil }, DenyNotInCallGraph},
		{"not application code", nil, func(_ *Manager, _ *fakeRequestedStore, r *ProbeRequest) {
			r.Recommendation = javaRec("java.util.HashMap", "get")
		}, DenyNotApplicationCode},
		{"source not instrumented", nil, func(_ *Manager, s *fakeRequestedStore, r *ProbeRequest) {
			r.Source.WorkloadName = "gone"
		}, DenySourceNotInstrumented},
		{"rule disabled", []client.Object{autopilotRuleWith(managed, true)}, nil, DenyRuleDisabled},
		{"rule not managed", []client.Object{autopilotRuleWith(nil, false)}, nil, DenyRuleNotManaged},
		{"already requested and reverted", nil, func(_ *Manager, s *fakeRequestedStore, r *ProbeRequest) {
			p := requestedRow(r.InterrogationID, checkout, storedAuth, firstSeen)
			end(&p)
			s.rows[p.ChangeID] = p
		}, DenyAlreadyRequested},
		{"already requested and expired", nil, func(_ *Manager, s *fakeRequestedStore, r *ProbeRequest) {
			p := requestedRow(r.InterrogationID, checkout, storedAuth, firstSeen)
			p.ExpiresAt = time.Now().Add(-time.Second)
			s.rows[p.ChangeID] = p
		}, DenyAlreadyRequested},
		{"probed by another rule", []client.Object{userRuleWith(auth)}, nil, DenyAlreadyProbed},
		{"placed by the loop", []client.Object{autopilotRuleWith(managed, false, auth)}, nil, DenyAlreadyProbed},
		{"workload cap", []client.Object{userRuleWith(other)}, func(m *Manager, _ *fakeRequestedStore, _ *ProbeRequest) {
			m.MaxProbesPerWorkload = 1
		}, DenyWorkloadCap},
		{"workload cap with a probe the loop placed", []client.Object{autopilotRuleWith(managed, false, other)}, func(m *Manager, _ *fakeRequestedStore, _ *ProbeRequest) {
			m.MaxProbesPerWorkload = 1
		}, DenyWorkloadCap},
		{"workload cap with a live requested probe", nil, func(m *Manager, s *fakeRequestedStore, _ *ProbeRequest) {
			m.MaxProbesPerWorkload = 1
			p := requestedRow("int-other", checkout, storedPrice, firstSeen)
			s.rows[p.ChangeID] = p
		}, DenyWorkloadCap},
		{"run budget", nil, func(_ *Manager, s *fakeRequestedStore, r *ProbeRequest) {
			p := requestedRow(r.InterrogationID, checkout, storedPrice, firstSeen)
			s.rows[p.ChangeID] = p
			r.RunMaxProbes = 1
		}, DenyRunBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ps, r := probeRequestFixture(t, tc.objs...)
			if tc.modify != nil {
				tc.modify(m, ps, &r)
			}
			_, err := m.RequestProbe(context.Background(), r)
			var denied *ProbeDeniedError
			if !errors.As(err, &denied) || denied.Reason != tc.want {
				t.Fatalf("err = %v, want denial %s", err, tc.want)
			}
			if len(ps.inserted) != 0 {
				t.Fatalf("denied probe was stored: %+v", ps.inserted)
			}
		})
	}
}

func TestRequestProbe_AdmittedWhenOnlyAnotherRunHoldsIt(t *testing.T) {
	auth := instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Auth", MethodName: "check"}
	m, ps, r := probeRequestFixture(t, autopilotRuleWith(managed, false, auth))
	m.MaxProbesPerWorkload = 1
	held := requestedRow("int-other", checkout, storedAuth, firstSeen)
	held.InRule = true
	ps.rows[held.ChangeID] = held

	got, err := m.RequestProbe(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangeID == held.ChangeID || got.InterrogationID != r.InterrogationID {
		t.Fatalf("stored = %+v, want a row of this run", got)
	}
}

// Library code the application calls is app code to the frame classifier, so
// a probe on it is admitted like one on the application's own classes.
func TestRequestProbe_AdmitsLibraryFrame(t *testing.T) {
	m, ps, r := probeRequestFixture(t)
	r.Recommendation = javaRec("org.springframework.web.servlet.DispatcherServlet", "doService")
	got, err := m.RequestProbe(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "org.springframework.web.servlet.DispatcherServlet.doService" || len(ps.inserted) != 1 {
		t.Fatalf("stored = %+v, inserts %d", got, len(ps.inserted))
	}
}

func TestRequestProbe_StoredRow(t *testing.T) {
	m, ps, r := probeRequestFixture(t)
	before := time.Now()
	got, err := m.RequestProbe(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.inserted) != 1 {
		t.Fatalf("inserted %d rows, want 1", len(ps.inserted))
	}
	inserted := ps.inserted[0]
	if inserted.FirstSeen.Before(before) || inserted.FirstSeen.After(time.Now()) {
		t.Fatalf("first_seen = %v, want the request time", inserted.FirstSeen)
	}
	source := checkout
	source.ContainerName = "app"
	want := store.RequestedProbe{
		Recommendation:  storedRecommendations(source, "1", r.Revision, []schema.Recommendation{r.Recommendation})[0],
		ChangeID:        ChangeID(r.InterrogationID, "java", "com.acme.Auth.check"),
		InterrogationID: r.InterrogationID,
		Round:           2,
	}
	want.FirstSeen = inserted.FirstSeen
	if inserted != want {
		t.Fatalf("inserted = %+v\nwant %+v", inserted, want)
	}
	if got.State != store.ProbeApproved {
		t.Fatalf("returned row state = %s", got.State)
	}

	// Asking again for a live probe returns the row and stores nothing.
	again, err := m.RequestProbe(context.Background(), r)
	if err != nil || again.ChangeID != got.ChangeID || len(ps.inserted) != 1 {
		t.Fatalf("repeat = %+v, %v; inserts %d", again, err, len(ps.inserted))
	}
}

func TestRequestProbe_FindsFunctionInLaterTransaction(t *testing.T) {
	m, ps, r := probeRequestFixture(t)
	ps.tries["worker/2"] = []store.CallTreeNode{{ID: "x", Name: "void com.acme.Price.total()"}}
	r.Transactions = append(r.Transactions, ProbeTransaction{ContainerName: "worker", TransactionID: "2"})
	r.Recommendation = javaRec("com.acme.Price", "total")

	if _, err := m.RequestProbe(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := ps.inserted[0]; got.TransactionID != "2" || got.ContainerName != "worker" {
		t.Fatalf("stored transaction = %s/%s, want worker/2", got.ContainerName, got.TransactionID)
	}
}

func TestRequestProbe_RunNotOpen(t *testing.T) {
	m, ps, r := probeRequestFixture(t)
	ps.runShut = true
	if _, err := m.RequestProbe(context.Background(), r); !errors.Is(err, store.ErrRunNotOpen) {
		t.Fatalf("err = %v, want ErrRunNotOpen", err)
	}
}

func TestProbesInEffect(t *testing.T) {
	auth := instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Auth", MethodName: "check"}
	other := instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Other", MethodName: "run"}
	m, ps, _ := probeRequestFixture(t, autopilotRuleWith(managed, false, auth), userRuleWith(other))
	live := requestedRow("int-1", checkout, storedPrice, firstSeen)
	inRuleToo := requestedRow("int-1", checkout, storedAuth, firstSeen)
	expired := requestedRow("int-1", checkout, storedCart, firstSeen)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	otherWorkload := requestedRow("int-2", cart, storedCart, firstSeen)
	for _, p := range []store.RequestedProbe{live, inRuleToo, expired, otherWorkload} {
		ps.rows[p.ChangeID] = p
	}

	got, err := m.ProbesInEffect(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	ruleName := autopilotWorkloadRuleName(checkout)
	want := []CustomProbe{
		{RuleName: ruleName, Language: "java", Target: "com.acme.Auth.check"},
		{RuleName: "user-rule", Language: "java", Target: "com.acme.Other.run"},
		{RuleName: ruleName, Language: "java", Target: "com.acme.Price.total"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ProbesInEffect = %+v\nwant %+v", got, want)
	}
}
