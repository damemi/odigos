package opamp

import (
	"errors"
	"strings"
	"testing"
)

// TestResolveAccepts covers the endpoints a Central installation actually publishes, including the
// staging and production spellings this feature is configured with per environment.
func TestResolveAccepts(t *testing.T) {
	cases := map[string]string{
		"production":                  "wss://central.odigos.io/v1/opamp",
		"staging":                     "wss://central.staging.odigos.io/v1/opamp",
		"a self-hosted Central":       "wss://odigos.internal.example.com/v1/opamp",
		"an explicit port":            "wss://central.odigos.io:8443/v1/opamp",
		"the plain-HTTP transport":    "https://central.odigos.io/v1/opamp",
		"a path behind a shared host": "wss://central.odigos.io/odigos/v1/opamp",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Resolve(raw)
			if err != nil {
				t.Fatalf("Resolve(%q) = %v", raw, err)
			}
			if got != raw {
				t.Errorf("Resolve(%q) = %q; a well-formed endpoint must survive unchanged", raw, got)
			}
		})
	}
}

// TestResolveNormalizes: two spellings of the same endpoint must compare equal, or a cosmetic edit in
// the chart reads as an endpoint move and redeploys every Collector in the fleet.
func TestResolveNormalizes(t *testing.T) {
	const want = "wss://central.odigos.io/v1/opamp"

	for _, raw := range []string{
		"  wss://central.odigos.io/v1/opamp  ",
		"WSS://central.odigos.io/v1/opamp",
		"wss://CENTRAL.ODIGOS.IO/v1/opamp",
		// Host only: OpAMP's default path is filled in. An operator who configured the host has said
		// everything Odigos needs, and the path is not theirs to choose — the gateway serves this one.
		"wss://central.odigos.io",
		"wss://central.odigos.io/",
	} {
		got, err := Resolve(raw)
		if err != nil {
			t.Fatalf("Resolve(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestResolveRejects covers every way the endpoint can be configured wrong. Each is a value an operator
// could plausibly put in the chart, and each would otherwise surface as a Collector that deploys and
// never connects — the failure lists as the one the connector cannot diagnose.
func TestResolveRejects(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{
			// The whole reason this cannot be plaintext: the Collector presents a bearer credential on
			// this connection, and that credential reads the destination's own credential out of a
			// configuration push.
			name: "plaintext ws to a real host", raw: "ws://central.odigos.io/v1/opamp",
			want: "destination's own credential",
		},
		{name: "plaintext http to a real host", raw: "http://central.odigos.io/v1/opamp", want: "Use wss://"},
		{name: "no scheme", raw: "central.odigos.io/v1/opamp", want: "no scheme"},
		{name: "a scheme the Supervisor rejects", raw: "grpc://central.odigos.io/v1/opamp", want: "Supervisor accepts"},
		{name: "a scheme that looks right but is not", raw: "opamp://central.odigos.io/v1/opamp", want: "Supervisor accepts"},
		{name: "no host", raw: "wss:///v1/opamp", want: "no host"},
		{
			// Would be copied into every Collector's workload definition — the one place requires a
			// credential never to appear.
			name: "credentials in the URL", raw: "wss://user:secret@central.odigos.io/v1/opamp",
			want: "must not contain credentials",
		},
		{name: "a query string", raw: "wss://central.odigos.io/v1/opamp?token=abc", want: "query string or fragment"},
		{name: "a fragment", raw: "wss://central.odigos.io/v1/opamp#frag", want: "query string or fragment"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.raw)
			if err == nil {
				t.Fatalf("Resolve(%q) = %q, want an error", tc.raw, got)
			}
			if got != "" {
				t.Errorf("a rejected endpoint still returned %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestPlaintextIsAllowedOnlyOnLoopback: a port-forwarded gateway is how the managed-Collector path is
// developed against, and a connection that cannot leave the machine cannot leak the credential.
func TestPlaintextIsAllowedOnlyOnLoopback(t *testing.T) {
	for _, raw := range []string{
		"ws://localhost:8080/v1/opamp",
		"ws://127.0.0.1:8080/v1/opamp",
		"ws://[::1]:8080/v1/opamp",
		"http://localhost:8080/v1/opamp",
		"ws://gateway.localhost:8080/v1/opamp",
	} {
		if _, err := Resolve(raw); err != nil {
			t.Errorf("Resolve(%q) = %v; a loopback endpoint cannot leave the machine", raw, err)
		}
	}

	// An in-cluster Service name is not loopback and is not reachable from a customer's cloud account
	// either, so it is refused rather than quietly accepted as "internal enough".
	for _, raw := range []string{
		"ws://connector-opamp-gateway.odigos-system.svc:8080/v1/opamp",
		"ws://10.0.0.5:8080/v1/opamp",
		"ws://localhost.attacker.example.com/v1/opamp",
	} {
		if _, err := Resolve(raw); err == nil {
			t.Errorf("Resolve(%q) was accepted as loopback", raw)
		}
	}
}

// TestNotConfiguredIsItsOwnAnswer: nothing is broken when no endpoint is set — the feature is simply
// off, and a Collector is not deployed rather than deployed wrongly. The two call for different words.
func TestNotConfiguredIsItsOwnAnswer(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		_, err := Resolve(raw)
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("Resolve(%q) = %v, want ErrNotConfigured", raw, err)
		}
		if Configured(raw) {
			t.Errorf("Configured(%q) is true", raw)
		}
	}
	if !Configured("wss://central.odigos.io/v1/opamp") {
		t.Error("Configured is false for a real endpoint")
	}
	// Configured judges presence, not correctness — that separation is what lets a caller say
	// "misconfigured" rather than "not configured" for a value somebody did set.
	if !Configured("nonsense") {
		t.Error("Configured is false for a set-but-invalid endpoint")
	}
}

// TestChanged is the safe-endpoint-change property. A Collector holds the endpoint injected into its
// workload definition at deploy time, exactly as it holds its credential, so moving Central does not
// move the Collectors already out there — and telling a real move from a re-spelling is what decides
// whether an edit costs a fleet-wide redeploy.
func TestChanged(t *testing.T) {
	const prod = "wss://central.odigos.io/v1/opamp"

	cases := []struct {
		name, deployed, configured string
		want                       bool
	}{
		{name: "identical", deployed: prod, configured: prod},
		{name: "re-spelled", deployed: prod, configured: "WSS://CENTRAL.ODIGOS.IO/v1/opamp"},
		{name: "default path made explicit", deployed: "wss://central.odigos.io", configured: prod},
		{name: "whitespace", deployed: prod, configured: "  " + prod + " "},
		{name: "staging to production", deployed: "wss://central.staging.odigos.io/v1/opamp", configured: prod, want: true},
		{name: "a port added", deployed: prod, configured: "wss://central.odigos.io:8443/v1/opamp", want: true},
		{name: "a path changed", deployed: prod, configured: "wss://central.odigos.io/odigos/v1/opamp", want: true},
		{
			// Refusing to act on a broken configuration is what keeps a typo in the chart from
			// redeploying every Collector in the fleet.
			name: "the new value is unusable", deployed: prod, configured: "ws://central.odigos.io/v1/opamp",
		},
		{name: "the new value is empty", deployed: prod, configured: ""},
		{name: "nothing was deployed against", deployed: "", configured: prod},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Changed(tc.deployed, tc.configured); got != tc.want {
				t.Errorf("Changed(%q, %q) = %v, want %v", tc.deployed, tc.configured, got, tc.want)
			}
		})
	}
}

// TestResolveIsIdempotent: the stamped value is read back and re-resolved on every reconcile, so a
// second pass must not keep changing it — that would be an endpoint that reports as changed for ever.
func TestResolveIsIdempotent(t *testing.T) {
	for _, raw := range []string{
		"wss://central.odigos.io",
		"  WSS://Central.Odigos.IO/v1/opamp ",
		"https://central.odigos.io/v1/opamp",
		"ws://localhost:8080",
	} {
		once, err := Resolve(raw)
		if err != nil {
			t.Fatalf("Resolve(%q) = %v", raw, err)
		}
		twice, err := Resolve(once)
		if err != nil {
			t.Fatalf("Resolve(%q) = %v", once, err)
		}
		if once != twice {
			t.Errorf("Resolve is not idempotent for %q: %q then %q", raw, once, twice)
		}
	}
}

// TestOdigosAcceptsOnlyWhatTheSupervisorDoes: anything stamped has to load in the Collector's own
// Supervisor config, or the Collector refuses its configuration at startup and nothing upstream knows
// why. Odigos's set is deliberately narrower — never wider.
func TestOdigosAcceptsOnlyWhatTheSupervisorDoes(t *testing.T) {
	for scheme := range secureSchemes {
		if !supervisorSchemes[scheme] {
			t.Errorf("Odigos accepts scheme %q, which the Supervisor rejects", scheme)
		}
	}
	// And the plaintext pair must stay in the Supervisor's set too, since loopback development uses them.
	for _, scheme := range []string{"ws", "http"} {
		if !supervisorSchemes[scheme] {
			t.Errorf("scheme %q is used for loopback development but is not in the Supervisor's set", scheme)
		}
	}
}
