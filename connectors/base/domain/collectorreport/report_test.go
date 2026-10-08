package collectorreport

import (
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var now = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

const (
	region  = "eu-west-1"
	network = "vpc-0abc"
)

func at(d time.Duration) *time.Time { t := now.Add(d); return &t }

// running is a healthy automatic-mode Collector: deployed, connected, configured.
func running(mutate func(*Input)) Input {
	in := Input{
		Region:            region,
		NetworkID:         network,
		Mode:              spec.ModeAutomatic,
		Origin:            "odigos",
		Status:            collectorstatus.StatusReady,
		OpAMPLastSeenAt:   at(-10 * time.Second),
		OpAMPInstanceUID:  "01ab",
		HeartbeatInterval: 30 * time.Second,
		EndpointGRPC:      "collector.internal.example:4317",
		TLSMode:           collectoringress.TLSOff,
		IngressTier:       collectoringress.TierNetworkIdentities,
		AdmittedFrom:      3,
		DesiredCount:      2,
		RunningCount:      2,
		ConfigHash:        "abc",
		ConfigPushedAt:    at(-4 * time.Minute),
		Now:               now,
	}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

// TestHeartbeatAndStalenessAreExplicit is the criterion. Three states, not a boolean: "never connected"
// and "went quiet" have different causes and different remedies.
func TestHeartbeatAndStalenessAreExplicit(t *testing.T) {
	const interval = 30 * time.Second

	cases := []struct {
		name     string
		lastSeen *time.Time
		want     Liveness
	}{
		{name: "never", lastSeen: nil, want: LivenessNeverConnected},
		{name: "zero time is never", lastSeen: &time.Time{}, want: LivenessNeverConnected},
		{name: "just now", lastSeen: at(0), want: LivenessLive},
		{name: "one interval", lastSeen: at(-interval), want: LivenessLive},
		{name: "just inside the window", lastSeen: at(-(interval * StaleAfterIntervals) + time.Second), want: LivenessLive},
		{name: "exactly at the window", lastSeen: at(-interval * StaleAfterIntervals), want: LivenessLive},
		{name: "just past the window", lastSeen: at(-(interval * StaleAfterIntervals) - time.Second), want: LivenessStale},
		{name: "long gone", lastSeen: at(-24 * time.Hour), want: LivenessStale},
		{
			// Clock skew between the connector and the database is real; reading it as stale would
			// report a working Collector as broken.
			name: "in the future", lastSeen: at(time.Minute), want: LivenessLive,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LivenessOf(tc.lastSeen, interval, now); got != tc.want {
				t.Errorf("LivenessOf = %q, want %q", got, tc.want)
			}
		})
	}

	// The window is stated in terms of the heartbeat interval, and scales with it.
	if got := StaleWindow(interval); got != 90*time.Second {
		t.Errorf("StaleWindow(30s) = %s, want 90s", got)
	}
	if got := StaleWindow(time.Minute); got != 3*time.Minute {
		t.Errorf("StaleWindow(1m) = %s, want 3m", got)
	}
	// A zero interval is a caller bug, and a zero WINDOW would make every Collector stale the instant
	// it connected. The documented default is used instead.
	if got := StaleWindow(0); got != DefaultHeartbeatInterval*StaleAfterIntervals {
		t.Errorf("StaleWindow(0) = %s, want the default window", got)
	}
	if LivenessOf(at(-time.Second), 0, now) != LivenessLive {
		t.Error("a Collector seen a second ago was stale under a zero interval")
	}
}

// TestStalenessIsNamedInTheMessage: "stale" with no number sends someone to look with nothing to look
// for, and the window is what they need to know to judge it.
func TestStalenessIsNamedInTheMessage(t *testing.T) {
	r := Build(running(func(in *Input) { in.OpAMPLastSeenAt = at(-10 * time.Minute) }))

	if r.Liveness != LivenessStale {
		t.Fatalf("Liveness = %q", r.Liveness)
	}
	for _, want := range []string{"10m ago", "1m30s", "cannot change"} {
		if !strings.Contains(r.Message, want) {
			t.Errorf("the message does not mention %q: %s", want, r.Message)
		}
	}
}

// TestFailureMappingsAreStable is the criterion: every (recorded status, liveness) pair has one defined
// answer, so a new state cannot fall through to whatever the last branch happened to be.
func TestFailureMappingsAreStable(t *testing.T) {
	cases := []struct {
		recorded    collectorstatus.Status
		liveness    Liveness
		mode        string
		wantStatus  collectorstatus.Status
		wantHealthy bool
	}{
		// Live: the recorded status stands.
		{collectorstatus.StatusReady, LivenessLive, spec.ModeAutomatic, collectorstatus.StatusReady, true},
		{collectorstatus.StatusDeploying, LivenessLive, spec.ModeAutomatic, collectorstatus.StatusDeploying, false},
		{collectorstatus.StatusDegraded, LivenessLive, spec.ModeAutomatic, collectorstatus.StatusDegraded, false},
		{collectorstatus.StatusError, LivenessLive, spec.ModeAutomatic, collectorstatus.StatusError, false},
		{collectorstatus.StatusPending, LivenessLive, spec.ModeAutomatic, collectorstatus.StatusPending, false},

		// Silent after having connected: Odigos can no longer configure it, which is Degraded — working,
		// in a posture the customer must be told about.
		{collectorstatus.StatusReady, LivenessStale, spec.ModeAutomatic, collectorstatus.StatusDegraded, false},
		{collectorstatus.StatusDegraded, LivenessStale, spec.ModeAutomatic, collectorstatus.StatusDegraded, false},
		{collectorstatus.StatusError, LivenessStale, spec.ModeAutomatic, collectorstatus.StatusError, false},
		// Except mid-deploy, where silence is expected: the tasks are being replaced.
		{collectorstatus.StatusDeploying, LivenessStale, spec.ModeAutomatic, collectorstatus.StatusDeploying, false},
		{collectorstatus.StatusPending, LivenessStale, spec.ModeAutomatic, collectorstatus.StatusPending, false},

		// Never connected, automatic: Odigos is still building it, so the reconciler's word stands.
		// Reporting Degraded here would call every Collector broken for the minute it takes to create.
		{collectorstatus.StatusDeploying, LivenessNeverConnected, spec.ModeAutomatic, collectorstatus.StatusDeploying, false},
		{collectorstatus.StatusPending, LivenessNeverConnected, spec.ModeAutomatic, collectorstatus.StatusPending, false},
		{collectorstatus.StatusError, LivenessNeverConnected, spec.ModeAutomatic, collectorstatus.StatusError, false},

		// Never connected, manual: only the customer can act.
		{collectorstatus.StatusPending, LivenessNeverConnected, spec.ModeManual, collectorstatus.StatusAwaitingCustomer, false},
		{collectorstatus.StatusReady, LivenessNeverConnected, spec.ModeManual, collectorstatus.StatusAwaitingCustomer, false},
	}

	for _, tc := range cases {
		name := string(tc.recorded) + "/" + string(tc.liveness) + "/" + tc.mode
		t.Run(name, func(t *testing.T) {
			in := running(func(in *Input) {
				in.Status = tc.recorded
				in.Mode = tc.mode
				switch tc.liveness {
				case LivenessNeverConnected:
					in.OpAMPLastSeenAt = nil
				case LivenessStale:
					in.OpAMPLastSeenAt = at(-10 * time.Minute)
				}
			})
			got := Build(in)
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Healthy != tc.wantHealthy {
				t.Errorf("Healthy = %v, want %v", got.Healthy, tc.wantHealthy)
			}
		})
	}
}

// TestHealthyRequiresReadyAndLive: a Collector that is Ready but silent is not healthy. Odigos cannot
// change its configuration, and calling that healthy hides the failure this reporting path exists for.
func TestHealthyRequiresReadyAndLive(t *testing.T) {
	for _, status := range []collectorstatus.Status{
		collectorstatus.StatusPending, collectorstatus.StatusDeploying, collectorstatus.StatusReady,
		collectorstatus.StatusDegraded, collectorstatus.StatusError, collectorstatus.StatusAwaitingCustomer,
	} {
		for _, liveness := range []Liveness{LivenessNeverConnected, LivenessLive, LivenessStale} {
			want := status == collectorstatus.StatusReady && liveness == LivenessLive
			if got := Healthy(status, liveness); got != want {
				t.Errorf("Healthy(%q, %q) = %v, want %v", status, liveness, got, want)
			}
		}
	}
}

// TestAnErrorProblemOutranksLiveness: nothing liveness says is worse than a refused configuration or a
// broken credential, so an Error must not be softened to Degraded by a stale session.
func TestAnErrorProblemOutranksLiveness(t *testing.T) {
	for _, liveness := range []*time.Time{nil, at(-10 * time.Second), at(-10 * time.Minute)} {
		got := Build(running(func(in *Input) {
			in.OpAMPLastSeenAt = liveness
			in.Problems = []collectorstatus.Problem{{
				Code: "CredentialRevoked", Severity: collectorstatus.SeverityError, Message: "credential revoked",
			}}
		}))
		if got.Status != collectorstatus.StatusError {
			t.Errorf("Status = %q with an Error problem, want Error", got.Status)
		}
	}
}

// TestUIDChurnDoesNotCreateLogicalCollectors is the criterion. A replaced task gets a new OpAMP instance
// uid (§5.3); the identity that matters is the network. A report keyed on the uid would show one
// Collector as a stream of unrelated ones.
func TestUIDChurnDoesNotCreateLogicalCollectors(t *testing.T) {
	before := Build(running(func(in *Input) { in.OpAMPInstanceUID = "01ab" }))
	after := Build(running(func(in *Input) { in.OpAMPInstanceUID = "ff99" }))

	if before.Name != after.Name {
		t.Errorf("the name changed with the instance uid: %q then %q", before.Name, after.Name)
	}
	if before.Status != after.Status || before.Healthy != after.Healthy {
		t.Errorf("a task replacement changed the status: %q/%v then %q/%v",
			before.Status, before.Healthy, after.Status, after.Healthy)
	}
	if before.Message != after.Message {
		t.Errorf("a task replacement changed the message:\n%s\n%s", before.Message, after.Message)
	}
	// The uid is still carried, for a caller that wants it — just not part of the identity.
	if after.InstanceUID != "ff99" {
		t.Errorf("InstanceUID = %q", after.InstanceUID)
	}

	// The name is region-only, so host-network metadata cannot move it.
	if got := Name(region); got != "eu-west-1" {
		t.Errorf("Name = %q", got)
	}

	// And a fleet is as many entries as there are Collectors, however many uids they have got through.
	fleet := BuildAll([]Input{
		running(func(in *Input) { in.OpAMPInstanceUID = "aaaa" }),
		running(func(in *Input) { in.NetworkID = "vpc-other"; in.OpAMPInstanceUID = "bbbb" }),
	})
	if len(fleet) != 2 {
		t.Fatalf("fleet = %d entries, want 2", len(fleet))
	}
	if fleet[0].Name != fleet[1].Name {
		t.Error("the same regional Collector was reported under different names")
	}
}

// TestManualAwaitingClearsOnConnection is the criterion: a non-error transient state that clears on its
// own when the Collector connects, the direct analogue of the workload WaitingForCustomer issue (§9.3).
func TestManualAwaitingClearsOnConnection(t *testing.T) {
	waiting := Build(running(func(in *Input) {
		in.Mode = spec.ModeManual
		in.Origin = "customer"
		in.Status = collectorstatus.StatusPending
		in.OpAMPLastSeenAt = nil
		in.EndpointGRPC = ""
		in.DesiredCount, in.RunningCount = 0, 0
		in.ConfigPushedAt = nil
	}))

	if waiting.Status != collectorstatus.StatusAwaitingCustomer {
		t.Fatalf("Status = %q, want %q", waiting.Status, collectorstatus.StatusAwaitingCustomer)
	}
	if waiting.Healthy {
		t.Error("a Collector nobody has deployed reported healthy")
	}
	for _, want := range []string{"manual", "waiting for you to deploy", "deployment instructions"} {
		if !strings.Contains(waiting.Message, want) {
			t.Errorf("the message does not say %q: %s", want, waiting.Message)
		}
	}
	// Nothing is invented about a Collector Odigos has never seen: no task counts, no endpoint.
	for _, unwanted := range []string{"tasks", "plaintext", "config pushed"} {
		if strings.Contains(waiting.Message, unwanted) {
			t.Errorf("the message claims %q about a Collector that does not exist yet: %s", unwanted, waiting.Message)
		}
	}

	// The customer deploys it and it calls home. The state clears with no other input — nothing had to
	// notice, nothing had to write a status.
	connected := Build(running(func(in *Input) {
		in.Mode = spec.ModeManual
		in.Origin = "customer"
		in.Status = collectorstatus.StatusAwaitingCustomer
		in.OpAMPLastSeenAt = at(-5 * time.Second)
	}))
	if connected.Status != collectorstatus.StatusReady {
		t.Errorf("Status = %q after connecting, want Ready", connected.Status)
	}
	if !connected.Healthy {
		t.Error("a connected manual Collector is not healthy")
	}

	// And it goes back to awaiting only if it was never seen — a Collector that connected and went quiet
	// is stale, not un-deployed, because the customer already did their part.
	quiet := Build(running(func(in *Input) {
		in.Mode = spec.ModeManual
		in.Status = collectorstatus.StatusReady
		in.OpAMPLastSeenAt = at(-10 * time.Minute)
	}))
	if quiet.Status != collectorstatus.StatusDegraded {
		t.Errorf("Status = %q for a manual Collector that went quiet, want Degraded not AwaitingCustomer", quiet.Status)
	}
}

// TestOwnershipIsInTheMessage: mode and origin together are the only thing that answers "will Odigos
// remove this?", and that is the one thing a customer cannot infer from the infrastructure (§9.3).
func TestOwnershipIsInTheMessage(t *testing.T) {
	cases := []struct {
		name          string
		mode, origin  string
		adopted       bool
		wantSubstring string
	}{
		{name: "managed", mode: spec.ModeAutomatic, origin: "odigos", wantSubstring: "removed at teardown"},
		{name: "adopted", mode: spec.ModeAutomatic, origin: "odigos", adopted: true, wantSubstring: "will not delete it"},
		{name: "customer-created, converged", mode: spec.ModeAutomatic, origin: "customer", wantSubstring: "will not delete it"},
		{name: "handed over", mode: spec.ModeManual, origin: "odigos", wantSubstring: "handed over"},
		{name: "the customer's", mode: spec.ModeManual, origin: "customer", wantSubstring: "never touches"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(running(func(in *Input) {
				in.Mode, in.Origin, in.Adopted = tc.mode, tc.origin, tc.adopted
			}))
			if !strings.Contains(got.Message, tc.wantSubstring) {
				t.Errorf("the message does not say %q: %s", tc.wantSubstring, got.Message)
			}
			if !strings.HasPrefix(got.Message, tc.mode) {
				t.Errorf("the message does not lead with the mode: %s", got.Message)
			}
		})
	}
}

// TestStandingPosturesAreReportedEveryPass: a posture reported once, at the moment it was applied, is
// invisible to everyone who looks afterwards — which is everyone who matters (§9.3). The plaintext
// default rides in the message and never in the status; the network-CIDR fallback sets Degraded.
func TestStandingPosturesAreReportedEveryPass(t *testing.T) {
	plaintext := Build(running(func(in *Input) { in.TLSMode = collectoringress.TLSOff }))
	if !strings.Contains(plaintext.Message, "(plaintext)") {
		t.Errorf("the plaintext hop is not stated: %s", plaintext.Message)
	}
	if plaintext.Status != collectorstatus.StatusReady {
		t.Errorf("the plaintext default set status %q; it rides in the message only", plaintext.Status)
	}

	fallback := Build(running(func(in *Input) {
		in.IngressTier = collectoringress.TierNetworkCIDR
		in.Problems = []collectorstatus.Problem{{
			Code:     "CollectorIngressNetworkCIDRFallback",
			Severity: collectorstatus.SeverityDegraded,
			Message:  "admits the whole network's range",
		}}
	}))
	if fallback.Status != collectorstatus.StatusDegraded {
		t.Errorf("the network-CIDR fallback left status %q, want Degraded", fallback.Status)
	}
	if !strings.Contains(fallback.Message, "fallback") || !strings.Contains(fallback.Message, "whole network") {
		t.Errorf("the fallback is not explained: %s", fallback.Message)
	}

	// Repeated reads of unchanged state say the same thing, every time — that is what "every pass" means.
	for i := 0; i < 10; i++ {
		if got := Build(running(func(in *Input) { in.IngressTier = collectoringress.TierNetworkCIDR })); got.Message != fallbackMessage(t) {
			t.Fatalf("the message changed between reads on pass %d", i)
		}
	}
}

func fallbackMessage(t *testing.T) string {
	t.Helper()
	return Build(running(func(in *Input) { in.IngressTier = collectoringress.TierNetworkCIDR })).Message
}

// TestReportIsDeterministic: the report is read on a timer, so two reads of unchanged state must produce
// the same bytes rather than a string that churns on sub-second precision or map order.
func TestReportIsDeterministic(t *testing.T) {
	in := running(func(i *Input) {
		i.Problems = []collectorstatus.Problem{
			{Code: "A", Severity: collectorstatus.SeverityInfo, Message: "first"},
			{Code: "B", Severity: collectorstatus.SeverityDegraded, Message: "second"},
		}
	})
	first := Build(in)
	for i := 0; i < 50; i++ {
		again := Build(in)
		if again != first {
			t.Fatalf("the report changed between reads:\n%+v\n%+v", first, again)
		}
	}
	// Problem order is the caller's and is preserved, so a reader sees them in the order they were found.
	if strings.Index(first.Message, "first") > strings.Index(first.Message, "second") {
		t.Errorf("problem order was not preserved: %s", first.Message)
	}
}

// TestBuildAllIsSorted: two reads of unchanged state must produce the same list rather than a reshuffle.
func TestBuildAllIsSorted(t *testing.T) {
	reports := BuildAll([]Input{
		running(func(in *Input) { in.NetworkID = "vpc-c" }),
		running(func(in *Input) { in.NetworkID = "vpc-a" }),
		running(func(in *Input) { in.NetworkID = "vpc-b" }),
	})
	for i := 1; i < len(reports); i++ {
		if reports[i-1].Name > reports[i].Name {
			t.Fatalf("not sorted: %v", []string{reports[0].Name, reports[1].Name, reports[2].Name})
		}
	}
	if len(BuildAll(nil)) != 0 {
		t.Error("an empty fleet produced entries")
	}
}

// TestAZeroInputDoesNotPanicOrLie: rows are read from a database and a column can be empty. The report
// must degrade to something honest rather than claiming a healthy Collector.
func TestAZeroInputDoesNotPanicOrLie(t *testing.T) {
	got := Build(Input{Now: now})

	if got.Healthy {
		t.Error("a Collector with no recorded state reported healthy")
	}
	if got.Status == collectorstatus.StatusReady {
		t.Errorf("Status = %q for an empty row", got.Status)
	}
	if got.Message == "" {
		t.Error("an empty row produced no message at all")
	}
	if got.Liveness != LivenessNeverConnected {
		t.Errorf("Liveness = %q", got.Liveness)
	}
}

// TestTheRecordedDiagnosisReachesTheReader. managed_collectors.message is the only channel for a
// finding this package cannot re-derive from the row — the Collector's verbatim rejection of a
// configuration, or the ECS reason a task will not start. A report that composed everything except that
// column would leave the most specific sentence anyone wrote about a Collector visible only in the
// database.
func TestTheRecordedDiagnosisReachesTheReader(t *testing.T) {
	verbatim := `the Collector rejected this configuration and is still running the last one that ` +
		`applied cleanly. It reported: error decoding 'processors': unknown type: "batchh"`
	got := Build(running(func(in *Input) {
		in.Status = collectorstatus.StatusDegraded
		in.RecordedMessage = verbatim
	}))
	if !strings.Contains(got.Message, verbatim) {
		t.Fatalf("the recorded diagnosis is not in the report: %q", got.Message)
	}

	// Not repeated when it only says what the agent already said. Two writers reach that column and one
	// of them writes the agent's own health into it, so the guard is against a sentence twice rather
	// than against a missing one.
	echo := Build(running(func(in *Input) {
		in.AgentHealth = "Unhealthy: exporter otlp failed"
		in.RecordedMessage = "Unhealthy: exporter otlp failed"
	}))
	if strings.Count(echo.Message, "Unhealthy: exporter otlp failed") != 1 {
		t.Errorf("the agent health is repeated: %q", echo.Message)
	}

	// And an empty column adds nothing — no stray separator, and the message a Collector nobody has
	// judged yet gets is unchanged.
	plain := Build(running(nil))
	if strings.Contains(plain.Message, " ·  · ") || strings.HasSuffix(plain.Message, " · ") {
		t.Errorf("an empty recorded message left a gap: %q", plain.Message)
	}
}
