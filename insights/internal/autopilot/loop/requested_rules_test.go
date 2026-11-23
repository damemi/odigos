package loop

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/common/consts"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
)

// fakeRequestedStore keeps requested probes in memory with the semantics of
// the Postgres statements.
type fakeRequestedStore struct {
	mu       sync.Mutex
	rows     map[string]store.RequestedProbe
	placedAt map[string]time.Time
	tries    map[string][]store.CallTreeNode
	listErr  error
	runShut  bool
	inserted []store.RequestedProbe
	blocks   int
}

func newFakeRequestedStore(rows ...store.RequestedProbe) *fakeRequestedStore {
	f := &fakeRequestedStore{rows: map[string]store.RequestedProbe{}, placedAt: map[string]time.Time{}, tries: map[string][]store.CallTreeNode{}}
	for _, p := range rows {
		f.rows[p.ChangeID] = p
	}
	return f
}

func (f *fakeRequestedStore) row(id string) store.RequestedProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[id]
}

func (f *fakeRequestedStore) update(id string, fn func(*store.RequestedProbe)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.rows[id]
	fn(&p)
	f.rows[id] = p
}

func (f *fakeRequestedStore) GetCallTree(_ context.Context, source store.SourceKey, transactionID string) ([]store.CallTreeNode, error) {
	return f.tries[source.ContainerName+"/"+transactionID], nil
}

func (f *fakeRequestedStore) InsertRequestedProbe(_ context.Context, p store.RequestedProbe, runMaxProbes int, now time.Time) (store.RequestedProbe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runShut {
		return store.RequestedProbe{}, store.ErrRunNotOpen
	}
	if prev, ok := f.rows[p.ChangeID]; ok {
		return prev, nil
	}
	n := 0
	for _, r := range f.rows {
		if r.InterrogationID == p.InterrogationID {
			n++
		}
	}
	if n >= runMaxProbes {
		return store.RequestedProbe{}, store.ErrRunBudgetUsed
	}
	f.inserted = append(f.inserted, p)
	p.State = store.ProbeApproved
	p.ExpiresAt = now.Add(time.Hour)
	p.UpdatedAt = now
	f.rows[p.ChangeID] = p
	return p, nil
}

func (f *fakeRequestedStore) GetRequestedProbe(_ context.Context, changeID string) (store.RequestedProbe, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.rows[changeID]
	return p, ok, nil
}

func (f *fakeRequestedStore) ListRequestedProbes(_ context.Context, now time.Time) ([]store.RequestedProbe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []store.RequestedProbe
	for _, p := range f.rows {
		if p.InRule || p.BeingPlaced || p.Live(now) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b store.RequestedProbe) int {
		return cmp.Or(
			strings.Compare(a.Namespace, b.Namespace),
			strings.Compare(a.WorkloadKind, b.WorkloadKind),
			strings.Compare(a.WorkloadName, b.WorkloadName),
			a.FirstSeen.Compare(b.FirstSeen),
			strings.Compare(a.Target, b.Target),
		)
	})
	return out, nil
}

func (f *fakeRequestedStore) MarkRequestedProbesBeingPlaced(_ context.Context, rows []store.RequestedProbe) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range rows {
		if p, ok := f.rows[r.ChangeID]; ok && p.PlaceGen == r.PlaceGen {
			p.BeingPlaced = true
			p.PlaceGen++
			f.rows[r.ChangeID] = p
			f.placedAt[r.ChangeID] = time.Now()
			n++
		}
	}
	return n == len(rows), nil
}

func (f *fakeRequestedStore) SettleRequestedProbes(_ context.Context, read []store.RequestedProbe, placed []string, blocked map[string]string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range read {
		id := r.ChangeID
		p, ok := f.rows[id]
		if !ok || p.PlaceGen != r.PlaceGen {
			continue
		}
		in := slices.Contains(placed, id)
		if in && !p.InRule {
			p.AppliedAt = now
		}
		if p.InRule && !in {
			p.RemovedAt = now
		}
		p.InRule = in
		p.BeingPlaced = p.BeingPlaced && !in && time.Since(f.placedAt[id]) < 5*time.Minute
		p.BlockReason = ""
		if !in {
			p.BlockReason = blocked[id]
		}
		f.rows[id] = p
	}
	return nil
}

func (f *fakeRequestedStore) BlockRequestedProbes(_ context.Context, ids []string, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks++
	for _, id := range ids {
		if p, ok := f.rows[id]; ok {
			p.BlockReason = reason
			f.rows[id] = p
		}
	}
	return nil
}

func (f *fakeRequestedStore) EndWorkloadProbes(_ context.Context, w store.SourceKey, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, p := range f.rows {
		if rowWorkload(p) == workloadKeyOf(w) && p.State == store.ProbeApproved {
			p.State = store.ProbeReverted
			p.EndReason = "source_gone"
			p.UpdatedAt = now
			f.rows[id] = p
		}
	}
	return nil
}

// requestedRow is a run's live probe on w built from a loop row, so the two
// name the same probe.
func requestedRow(run string, w store.SourceKey, r store.Recommendation, firstSeen time.Time) store.RequestedProbe {
	r.Namespace, r.WorkloadKind, r.WorkloadName = w.Namespace, w.WorkloadKind, w.WorkloadName
	r.FirstSeen = firstSeen
	return store.RequestedProbe{
		Recommendation:  r,
		ChangeID:        ChangeID(run, r.Language, r.Target),
		InterrogationID: run,
		State:           store.ProbeApproved,
		ExpiresAt:       time.Now().Add(time.Hour),
	}
}

func end(p *store.RequestedProbe) {
	p.State = store.ProbeReverted
	p.EndReason = "revert"
}

var storedCart = store.Recommendation{
	FirstSeen: firstSeen.Add(2 * time.Minute), Language: "java", Target: "com.acme.Cart.add",
	Category: "observability", Function: "com.acme.Cart.add", Why: "cart",
	ClassName: "com.acme.Cart", MethodName: "add",
}

func newRequestedRuleManager(t *testing.T, loop fakeRecommendationStore, req *fakeRequestedStore, objs ...client.Object) (*Manager, client.Client) {
	t.Helper()
	m, c := newRuleTestManager(t, loop, objs...)
	m.requested = req
	return m, c
}

func wantTargets(t *testing.T, c client.Client, want ...string) {
	t.Helper()
	rule, ok := getRule(t, c)
	if len(want) == 0 {
		if ok {
			t.Fatalf("rule exists with %v, want none", javaTargets(rule))
		}
		return
	}
	if !ok {
		t.Fatalf("no rule, want %v", want)
	}
	if got := javaTargets(rule); !slices.Equal(got, want) {
		t.Fatalf("rule probes = %v, want %v", got, want)
	}
}

func TestReconcile_RequestedProbeWithAutoApplyOff(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req)
	m.AutoApply = false

	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")
	if got := req.row(row.ChangeID); !got.InRule || got.AppliedAt.IsZero() || got.BlockReason != "" {
		t.Fatalf("row after placing = %+v", got)
	}

	req.update(row.ChangeID, end)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c)
	if got := req.row(row.ChangeID); got.InRule || got.RemovedAt.IsZero() {
		t.Fatalf("row after ending = in_rule %v removed_at %v", got.InRule, got.RemovedAt)
	}

	m.reconcileRules(context.Background(), autopilotModeOff)
	wantTargets(t, c)
}

// settleFailsOnce loses the first settle after a rule write, as a leader
// stopping between the two would.
type settleFailsOnce struct {
	*fakeRequestedStore
	failed bool
}

func (s *settleFailsOnce) SettleRequestedProbes(ctx context.Context, read []store.RequestedProbe, placed []string, blocked map[string]string, now time.Time) error {
	if !s.failed {
		s.failed = true
		return context.Canceled
	}
	return s.fakeRequestedStore.SettleRequestedProbes(ctx, read, placed, blocked, now)
}

func TestReconcile_LostSettleDoesNotOrphanProbe(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRuleTestManager(t, fakeRecommendationStore{})
	m.requested = &settleFailsOnce{fakeRequestedStore: req}
	m.AutoApply = false

	if err := m.reconcileRule(context.Background(), checkout, "", []store.RequestedProbe{row}, time.Now()); err == nil {
		t.Fatal("settle error not returned")
	}
	wantTargets(t, c, "com.acme.Auth.check")

	// The run ends before the next pass.
	req.update(row.ChangeID, end)
	m.reconcilePendingProbes(context.Background(), map[workloadKey]probeRetry{}, time.Now())
	wantTargets(t, c)
	if got := req.row(row.ChangeID); got.InRule {
		t.Fatal("row still in_rule after removal")
	}
}

// heldSettles keeps a leader's settles back until flush, so they can land
// after another leader's.
type heldSettles struct {
	*fakeRequestedStore
	held []func()
}

func (h *heldSettles) SettleRequestedProbes(ctx context.Context, read []store.RequestedProbe, placed []string, blocked map[string]string, now time.Time) error {
	h.held = append(h.held, func() { _ = h.fakeRequestedStore.SettleRequestedProbes(ctx, read, placed, blocked, now) })
	return nil
}

func (h *heldSettles) flush() {
	for _, f := range h.held {
		f()
	}
}

// Two leaders overlap at a lease handover: A read the row while it was live,
// the run ends, B takes the probe out, A writes it back, and B settles last.
func TestReconcile_StaleLeaderDoesNotOrphanProbe(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req)
	m.AutoApply = false
	ctx := context.Background()

	m.reconcileRules(ctx, "")
	wantTargets(t, c, "com.acme.Auth.check")
	staleA := req.row(row.ChangeID)
	req.update(row.ChangeID, end)

	bStore := &heldSettles{fakeRequestedStore: req}
	b := &Manager{Client: c, recommendations: fakeRecommendationStore{}, requested: bStore}
	if err := b.reconcileRule(ctx, checkout, "", []store.RequestedProbe{req.row(row.ChangeID)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcileRule(ctx, checkout, "", []store.RequestedProbe{staleA}, time.Now()); err != nil {
		t.Fatal(err)
	}
	bStore.flush()

	m.reconcileRules(ctx, "")
	m.reconcilePendingProbes(ctx, map[workloadKey]probeRetry{}, time.Now())
	wantTargets(t, c)
}

// beforeWrite runs hook once, just before the next rule write is sent.
type beforeWrite struct {
	client.Client
	hook func()
}

func (b *beforeWrite) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if h := b.hook; h != nil {
		b.hook = nil
		h()
	}
	return b.Client.Create(ctx, obj, opts...)
}

// Another leader's pass that writes nothing runs while A's write is in
// flight: the run has ended, so it sees no rule and nothing to place. A's
// write then lands and its settle is lost.
func TestReconcile_PassWithoutWriteKeepsFreshFlag(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRuleTestManager(t, fakeRecommendationStore{})
	m.AutoApply = false
	m.requested = &settleFailsOnce{fakeRequestedStore: req}
	b := &Manager{Client: c, recommendations: fakeRecommendationStore{}, requested: req}
	ctx := context.Background()

	m.Client = &beforeWrite{Client: c, hook: func() {
		req.update(row.ChangeID, end)
		if err := b.reconcileRule(ctx, checkout, "", []store.RequestedProbe{req.row(row.ChangeID)}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}}
	_ = m.reconcileRule(ctx, checkout, "", []store.RequestedProbe{row}, time.Now())
	m.Client = c
	wantTargets(t, c, "com.acme.Auth.check")

	m.reconcilePendingProbes(ctx, map[workloadKey]probeRetry{}, time.Now())
	wantTargets(t, c)
}

func TestReconcile_FrozenRuleKeepsLoopProbes(t *testing.T) {
	loop := fakeRecommendationStore{workloadOf(checkout): {storedAuth}}
	req := newFakeRequestedStore()
	m, c := newRequestedRuleManager(t, loop, req)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")

	// Auto-apply goes off; the loop then stores a probe it does not apply.
	m.AutoApply = false
	loop[workloadOf(checkout)] = []store.Recommendation{storedAuth, storedPrice}
	row := requestedRow("run-1", checkout, storedCart, storedCart.FirstSeen)
	req.rows[row.ChangeID] = row

	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check", "com.acme.Cart.add")

	req.update(row.ChangeID, end)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")
}

func TestReconcile_DryRunLoopRowNotAdopted(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen.Add(time.Hour))
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}}, req)
	m.AutoApply = false

	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")

	req.update(row.ChangeID, end)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c)
}

func TestReconcile_EndedRowLeavesRuleToLoop(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen.Add(time.Hour))
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}}, req)

	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")
	if !req.row(row.ChangeID).InRule {
		t.Fatal("row not placed while the loop's row holds the same probe")
	}

	req.update(row.ChangeID, end)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")
	if got := req.row(row.ChangeID); got.InRule {
		t.Fatal("ended row still in_rule")
	}
	listed, err := req.ListRequestedProbes(context.Background(), time.Now())
	if err != nil || len(listed) != 0 {
		t.Fatalf("next pass reads %d rows (err %v), want none", len(listed), err)
	}
}

func TestReconcile_RuleLeftAloneBlocksProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		reason string
	}{
		{"disabled", map[string]string{k8sconsts.OdigosProfilesManagedByLabel: k8sconsts.OdigosAutopilotLoopManagedByValue}, BlockRuleDisabled},
		{"not managed", nil, BlockRuleNotManaged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := &odigosv1.InstrumentationRule{
				ObjectMeta: metav1.ObjectMeta{Namespace: testOdigosNs, Name: autopilotRuleObjectName(checkout), Labels: tc.labels},
				Spec:       odigosv1.InstrumentationRuleSpec{Disabled: tc.reason == BlockRuleDisabled, Scopes: workloadScopes(checkout)},
			}
			row := requestedRow("run-1", checkout, storedAuth, firstSeen)
			req := newFakeRequestedStore(row)
			m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req, existing)
			before, _ := getRule(t, c)

			m.reconcileRules(context.Background(), "")
			after, _ := getRule(t, c)
			if after.ResourceVersion != before.ResourceVersion {
				t.Fatal("rule left alone was written")
			}
			if got := req.row(row.ChangeID); got.InRule || got.BlockReason != tc.reason {
				t.Fatalf("row = in_rule %v block %q, want %q", got.InRule, got.BlockReason, tc.reason)
			}
		})
	}
}

func TestReconcile_BlockReasons(t *testing.T) {
	t.Run("cap", func(t *testing.T) {
		row := requestedRow("run-1", checkout, storedPrice, storedPrice.FirstSeen)
		req := newFakeRequestedStore(row)
		m, c := newRequestedRuleManager(t, fakeRecommendationStore{workloadOf(checkout): {storedAuth}}, req)
		m.MaxProbesPerWorkload = 1

		m.reconcileRules(context.Background(), "")
		wantTargets(t, c, "com.acme.Auth.check")
		if got := req.row(row.ChangeID); got.InRule || got.BlockReason != BlockWorkloadCap {
			t.Fatalf("row = in_rule %v block %q", got.InRule, got.BlockReason)
		}
	})

	t.Run("cap goes to the older requested probe", func(t *testing.T) {
		row := requestedRow("run-1", checkout, storedCart, storedPrice.FirstSeen.Add(-time.Minute))
		req := newFakeRequestedStore(row)
		m, c := newRequestedRuleManager(t, fakeRecommendationStore{workloadOf(checkout): {storedPrice}}, req)
		m.MaxProbesPerWorkload = 1

		m.reconcileRules(context.Background(), "")
		wantTargets(t, c, "com.acme.Cart.add")
		if got := req.row(row.ChangeID); !got.InRule || got.BlockReason != "" {
			t.Fatalf("row = in_rule %v block %q", got.InRule, got.BlockReason)
		}
	})

	t.Run("covered by another rule", func(t *testing.T) {
		userRule := &odigosv1.InstrumentationRule{
			ObjectMeta: metav1.ObjectMeta{Namespace: testOdigosNs, Name: "user-rule"},
			Spec: odigosv1.InstrumentationRuleSpec{
				Scopes: workloadScopes(checkout),
				CustomInstrumentations: &instrumentationrules.CustomInstrumentationsWithMetadata{
					Java: []instrumentationrules.JavaCustomProbeWithMetadata{{
						JavaCustomProbe: instrumentationrules.JavaCustomProbe{ClassName: "com.acme.Price", MethodName: "total"},
					}},
				},
			},
		}
		row := requestedRow("run-1", checkout, storedPrice, storedPrice.FirstSeen)
		req := newFakeRequestedStore(row)
		m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req, userRule)

		m.reconcileRules(context.Background(), "")
		wantTargets(t, c)
		if got := req.row(row.ChangeID); got.InRule || got.BlockReason != BlockCoveredByRule {
			t.Fatalf("row = in_rule %v block %q", got.InRule, got.BlockReason)
		}
	})
}

func TestReconcile_SourceGoneEndsRequestedProbes(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req)
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c, "com.acme.Auth.check")

	ic := &odigosv1.InstrumentationConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"}}
	if err := c.Delete(context.Background(), ic); err != nil {
		t.Fatal(err)
	}
	m.reconcileRules(context.Background(), "")
	wantTargets(t, c)
	if got := req.row(row.ChangeID); got.State != store.ProbeReverted || got.EndReason != "source_gone" || got.InRule {
		t.Fatalf("row = %+v", got)
	}
}

func TestReconcile_SourceGoneUnderLeftRuleEndsRequestedProbes(t *testing.T) {
	for name, leave := range map[string]func(*odigosv1.InstrumentationRule){
		"disabled":    func(r *odigosv1.InstrumentationRule) { r.Spec.Disabled = true },
		"not managed": func(r *odigosv1.InstrumentationRule) { r.Labels = nil },
	} {
		t.Run(name, func(t *testing.T) {
			row := requestedRow("run-1", checkout, storedAuth, firstSeen)
			req := newFakeRequestedStore(row)
			m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req)
			m.reconcileRules(context.Background(), "")
			rule, _ := getRule(t, c)
			leave(rule)
			if err := c.Update(context.Background(), rule); err != nil {
				t.Fatal(err)
			}
			m.reconcileRules(context.Background(), "")

			ic := &odigosv1.InstrumentationConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"}}
			if err := c.Delete(context.Background(), ic); err != nil {
				t.Fatal(err)
			}
			m.reconcileRules(context.Background(), "")
			after, _ := getRule(t, c)
			if after.ResourceVersion != rule.ResourceVersion {
				t.Fatal("rule left alone was written")
			}
			if got := req.row(row.ChangeID); got.State != store.ProbeReverted || got.InRule {
				t.Fatalf("row = state %v in_rule %v, want reverted and out of the rule", got.State, got.InRule)
			}
		})
	}
}

var (
	cart = store.SourceKey{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "cart"}
	idle = store.SourceKey{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "idle"}
)

// newPendingTestManager has three instrumented workloads, and rule creates
// for failCreate fail while it is set.
func newPendingTestManager(t *testing.T, req *fakeRequestedStore, failCreate *string) (*Manager, client.Client, map[string]int) {
	t.Helper()
	t.Setenv(consts.CurrentNamespaceEnvVar, testOdigosNs)
	scheme := runtime.NewScheme()
	if err := odigosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var objs []client.Object
	for _, name := range []string{"checkout", "cart", "idle"} {
		objs = append(objs, &odigosv1.InstrumentationConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-" + name}})
	}
	var mu sync.Mutex
	writes := map[string]int{}
	note := func(obj client.Object) {
		mu.Lock()
		defer mu.Unlock()
		writes[obj.GetName()]++
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			note(obj)
			if *failCreate == obj.GetName() {
				return errors.New("admission webhook unavailable")
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			note(obj)
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			note(obj)
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	return &Manager{Client: c, AutoApply: true, recommendations: fakeRecommendationStore{}, requested: req}, c, writes
}

func TestReconcilePending_OnlyPendingWorkloads(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	checkoutRow := requestedRow("run-1", checkout, storedAuth, firstSeen)
	cartRow := requestedRow("run-2", cart, storedCart, firstSeen)
	// Already settled: in the rule and live, so not pending. Its rule is
	// missing, which only a full pass repairs.
	idleRow := requestedRow("run-3", idle, storedPrice, firstSeen)
	idleRow.InRule = true
	req := newFakeRequestedStore(checkoutRow, cartRow, idleRow)

	failCreate := autopilotRuleObjectName(checkout)
	m, _, writes := newPendingTestManager(t, req, &failCreate)
	backoff := map[workloadKey]probeRetry{}
	checkoutRule, cartRule, idleRule := autopilotRuleObjectName(checkout), autopilotRuleObjectName(cart), autopilotRuleObjectName(idle)

	m.reconcilePendingProbes(ctx, backoff, now)
	if writes[checkoutRule] != 1 || writes[cartRule] != 1 || writes[idleRule] != 0 {
		t.Fatalf("writes = %v, want one each for checkout and cart, none for idle", writes)
	}
	if b := backoff[workloadKeyOf(checkout)]; b.failures != 1 || !b.next.Equal(now.Add(probePollInterval)) {
		t.Fatalf("backoff after first failure = %+v", b)
	}
	if !req.row(cartRow.ChangeID).InRule {
		t.Fatal("cart's probe was not placed")
	}

	// checkout is backing off; cart becomes pending again and is not held up.
	req.update(cartRow.ChangeID, end)
	m.reconcilePendingProbes(ctx, backoff, now.Add(time.Second))
	if writes[checkoutRule] != 1 {
		t.Fatalf("checkout retried during its backoff: %v", writes)
	}
	if writes[cartRule] != 2 || req.row(cartRow.ChangeID).InRule {
		t.Fatalf("cart's ended probe not removed: writes %v", writes)
	}

	// The delay doubles with each failure.
	at := now.Add(probePollInterval)
	m.reconcilePendingProbes(ctx, backoff, at)
	if b := backoff[workloadKeyOf(checkout)]; writes[checkoutRule] != 2 || b.failures != 2 || !b.next.Equal(at.Add(2*probePollInterval)) {
		t.Fatalf("second failure: writes %v backoff %+v", writes, b)
	}

	failCreate = ""
	at = at.Add(2 * probePollInterval)
	m.reconcilePendingProbes(ctx, backoff, at)
	if _, ok := backoff[workloadKeyOf(checkout)]; ok || !req.row(checkoutRow.ChangeID).InRule {
		t.Fatalf("after success: backoff %v, in_rule %v", backoff, req.row(checkoutRow.ChangeID).InRule)
	}
	if writes[idleRule] != 0 {
		t.Fatalf("idle workload written by the poll: %v", writes)
	}
}

func TestReconcilePending_SkipsBlockedRows(t *testing.T) {
	ctx := context.Background()
	// Live, but the cap held it out of the rule.
	capped := requestedRow("run-1", checkout, storedAuth, firstSeen)
	capped.BlockReason = BlockWorkloadCap
	// Ended, but still in a rule the user disabled.
	stuck := requestedRow("run-2", cart, storedCart, firstSeen)
	end(&stuck)
	stuck.InRule = true
	stuck.BlockReason = BlockRuleDisabled
	req := newFakeRequestedStore(capped, stuck)

	var failCreate string
	m, c, writes := newPendingTestManager(t, req, &failCreate)
	disabled := &odigosv1.InstrumentationRule{
		ObjectMeta: metav1.ObjectMeta{Namespace: testOdigosNs, Name: autopilotRuleObjectName(cart), Labels: managed},
		Spec:       odigosv1.InstrumentationRuleSpec{Disabled: true, Scopes: workloadScopes(cart)},
	}
	if err := c.Create(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	clear(writes)

	m.reconcilePendingProbes(ctx, map[workloadKey]probeRetry{}, time.Now())
	if len(writes) != 0 || req.blocks != 0 {
		t.Fatalf("poll reconciled blocked rows: writes %v, block calls %d", writes, req.blocks)
	}
}

func TestRun_ReconcilerPlacesRequestedProbe(t *testing.T) {
	row := requestedRow("run-1", checkout, storedAuth, firstSeen)
	req := newFakeRequestedStore(row)
	m, c := newRequestedRuleManager(t, fakeRecommendationStore{}, req)
	m.AutoApply = false

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	deadline := time.Now().Add(3 * probePollInterval)
	for !req.row(row.ChangeID).InRule {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("requested probe not placed by the poll")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	wantTargets(t, c, "com.acme.Auth.check")
}

// newDBRuleTestManager is newRuleTestManager on a database of its own, since
// tick and the full reconcile do nothing without one.
func newDBRuleTestManager(t *testing.T) (*Manager, client.Client) {
	t.Helper()
	db, err := store.Open(pgtest.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, c := newRuleTestManager(t, nil)
	m.recommendations = nil
	m.Store = db
	m.reconcileNow = make(chan struct{}, 1)
	return m, c
}

func TestTick_AsksReconcilerForFullPass(t *testing.T) {
	m, _ := newDBRuleTestManager(t)
	m.tick(context.Background())
	if len(m.reconcileNow) != 1 {
		t.Fatal("tick did not ask the reconcile goroutine for a pass")
	}
}
