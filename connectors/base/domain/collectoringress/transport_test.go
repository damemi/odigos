package collectoringress

import (
	"strings"
	"testing"
)

// externalLB represents a provider whose front end can use a customer certificate but does not manage
// a certificate for its generated DNS name.
var externalLB = Platform{Name: "external-LB provider", ManagedTLS: false, LoadBalancerTLS: true}

// managed stands for Azure Container Apps and GCP Cloud Run, whose internal ingress terminates TLS with a
// platform-managed certificate and no load balancer of its own to put one on.
var managed = Platform{Name: "managed-ingress provider", ManagedTLS: true, LoadBalancerTLS: false}

const lbHost = "collector.provider.internal"

func problemCodes(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

func anyError(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

// TestDefaultModeIsWhateverCostsNothing: platform TLS wherever the provider gives it away, plaintext
// where it does not. A default requiring a certificate the customer has not got is a default that breaks
// the first deploy.
func TestDefaultModeIsWhateverCostsNothing(t *testing.T) {
	if got := externalLB.DefaultMode(); got != TLSOff {
		t.Errorf("external-LB default = %q, want off", got)
	}
	if got := managed.DefaultMode(); got != TLSPlatform {
		t.Errorf("managed-ingress default = %q, want platform", got)
	}
	tr, ps := ResolveTransport(TransportConfig{}, managed, "collector.internal.azurecontainerapps.io")
	if tr.Mode != TLSPlatform || tr.Insecure || anyError(ps) {
		t.Errorf("empty config on a managed platform = %+v problems %v, want platform TLS", tr, problemCodes(ps))
	}
}

// TestPlaintextIsReportedButNotAFault: plaintext can be a provider default, so it belongs in the
// message and not in the status. Reporting every such Collector as Degraded for its whole life is how
// Degraded stops meaning anything.
func TestPlaintextIsReportedButNotAFault(t *testing.T) {
	tr, ps := ResolveTransport(TransportConfig{}, externalLB, lbHost)
	if tr.Mode != TLSOff || !tr.Insecure || tr.Trust != TrustNone || tr.Termination != TerminationNone {
		t.Fatalf("transport = %+v, want plaintext", tr)
	}
	if tr.GRPCEndpoint != lbHost+":4317" || tr.HTTPEndpoint != "http://"+lbHost+":4318" {
		t.Errorf("endpoints = %q / %q", tr.GRPCEndpoint, tr.HTTPEndpoint)
	}
	if len(ps) != 1 || ps[0].Code != "PlaintextTelemetry" || ps[0].Severity != SeverityInfo {
		t.Fatalf("problems = %v, want one informational note", problemCodes(ps))
	}
	if !strings.Contains(ps[0].Message, "collector.regions") {
		t.Errorf("the note does not say what to set: %s", ps[0].Message)
	}
	if !strings.Contains(ps[0].Message, "destination credential") {
		t.Errorf("the note does not say why it matters: %s", ps[0].Message)
	}
}

// TestTLSRefusals walks every configuration this refuses, and each refusal must name the key to fix.
func TestTLSRefusals(t *testing.T) {
	cases := []struct {
		name string
		cfg  TransportConfig
		p    Platform
		code string
	}{
		{"unknown mode", TransportConfig{Mode: "mtls"}, externalLB, "UnknownTLSMode"},
		{"platform TLS where there is none", TransportConfig{Mode: TLSPlatform}, externalLB, "PlatformTLSUnavailable"},
		{"load balancer TLS where there is no load balancer", TransportConfig{Mode: TLSLoadBalancer, Hostname: "h", CertificateRef: "c"}, managed, "LoadBalancerTLSUnavailable"},
		{"load balancer TLS with no hostname", TransportConfig{Mode: TLSLoadBalancer, CertificateRef: "provider://certificate/collector"}, externalLB, "TLSHostnameRequired"},
		{"load balancer TLS with no certificate", TransportConfig{Mode: TLSLoadBalancer, Hostname: "otlp.example.com"}, externalLB, "TLSCertificateRequired"},
		{"collector TLS with no hostname", TransportConfig{Mode: TLSCollector, CertificateRef: "secret"}, externalLB, "TLSHostnameRequired"},
		{"collector TLS with no certificate", TransportConfig{Mode: TLSCollector, Hostname: "otlp.example.com"}, externalLB, "TLSCertificateRequired"},
		{
			"a listener on a load balancer Odigos does not create",
			TransportConfig{Mode: TLSLoadBalancer, Hostname: "otlp.example.com", CertificateRef: "provider://certificate/collector", EndpointOverride: "https://mine.example.com:4317"},
			externalLB, "TLSListenerWithoutLoadBalancer",
		},
	}
	for _, tc := range cases {
		_, ps := ResolveTransport(tc.cfg, tc.p, lbHost)
		if !anyError(ps) {
			t.Errorf("%s: problems = %v, want a refusal", tc.name, problemCodes(ps))
			continue
		}
		var found bool
		for _, p := range ps {
			if p.Code == tc.code {
				found = true
				if !strings.Contains(p.Message, "collector.regions") {
					t.Errorf("%s: refusal does not name the key: %s", tc.name, p.Message)
				}
			}
		}
		if !found {
			t.Errorf("%s: problems = %v, want %s", tc.name, problemCodes(ps), tc.code)
		}
	}
}

// TestNoPublishedAddressIsPendingNotAFault: before the load balancer exists there is nothing to point a
// workload at, but nothing is misconfigured either. It must not arrive as an error telling someone to
// change a setting that is already correct.
func TestNoPublishedAddressIsPendingNotAFault(t *testing.T) {
	tr, ps := ResolveTransport(TransportConfig{}, externalLB, "")
	if anyError(ps) {
		t.Errorf("problems = %v, want a pending note rather than an error", problemCodes(ps))
	}
	if tr.GRPCEndpoint != "" {
		t.Errorf("endpoint = %q, want empty so callers gate on it", tr.GRPCEndpoint)
	}
	var pending bool
	for _, p := range ps {
		if p.Code == "EndpointUnknown" && p.Severity == SeverityPending {
			pending = true
		}
	}
	if !pending {
		t.Errorf("problems = %v, want EndpointUnknown as pending", problemCodes(ps))
	}
}

// TestHostnameRequiredForACustomerCertificate states the constraint in the form it is usually discovered
// the hard way: the load balancer's own DNS name cannot carry a certificate, so every mode that presents
// one needs a name the customer controls, and the published endpoint must use that name — not the load
// balancer's — or verification fails on every export.
func TestHostnameRequiredForACustomerCertificate(t *testing.T) {
	for _, mode := range []TLSMode{TLSLoadBalancer, TLSCollector} {
		tr, ps := ResolveTransport(TransportConfig{
			Mode: mode, Hostname: "otlp.example.com", CertificateRef: "ref",
		}, externalLB, lbHost)
		if anyError(ps) {
			t.Fatalf("%s: unexpected refusal %v", mode, problemCodes(ps))
		}
		if strings.Contains(tr.GRPCEndpoint, lbHost) {
			t.Errorf("%s: published %q, which no certificate can be valid for", mode, tr.GRPCEndpoint)
		}
		if tr.GRPCEndpoint != "otlp.example.com:4317" || tr.HTTPEndpoint != "https://otlp.example.com:4318" {
			t.Errorf("%s: endpoints = %q / %q", mode, tr.GRPCEndpoint, tr.HTTPEndpoint)
		}
	}
}

// TestTrustCostIsStated: the two modes backed by a public certificate cost the sending side nothing; the
// private-PKI mode costs a fleet-wide trust distribution, and that has to be said rather than discovered
// when exports start failing.
func TestTrustCostIsStated(t *testing.T) {
	lb, ps := ResolveTransport(TransportConfig{Mode: TLSLoadBalancer, Hostname: "h.example.com", CertificateRef: "ref"}, externalLB, lbHost)
	if lb.Trust != TrustPublic {
		t.Errorf("load balancer TLS trust = %q, want public", lb.Trust)
	}
	if lb.Termination != TerminationLoadBalancer {
		t.Errorf("load balancer TLS termination = %q", lb.Termination)
	}
	// The back leg stays in the clear and a reader deciding this needs to know.
	var backLeg bool
	for _, p := range ps {
		if p.Code == "TLSTerminatesAtLoadBalancer" {
			backLeg = true
		}
	}
	if !backLeg {
		t.Errorf("problems = %v, want the plaintext back leg reported", problemCodes(ps))
	}

	coll, ps := ResolveTransport(TransportConfig{Mode: TLSCollector, Hostname: "h.example.com", CertificateRef: "secret"}, externalLB, lbHost)
	if coll.Trust != TrustCustom || coll.Termination != TerminationCollector {
		t.Errorf("collector TLS = %+v, want custom trust terminating at the Collector", coll)
	}
	var stated bool
	for _, p := range ps {
		if p.Code == "TLSRequiresClientTrust" {
			stated = true
			if !strings.Contains(p.Message, "disable") {
				t.Errorf("the note does not rule out disabling verification: %s", p.Message)
			}
		}
	}
	if !stated {
		t.Errorf("problems = %v, want the client trust requirement stated", problemCodes(ps))
	}
}

// TestEndpointOverrideReportsWhatItCanSee: Odigos creates nothing behind an override and cannot see past
// the scheme, so it says what it reads and does not guess.
func TestEndpointOverrideReportsWhatItCanSee(t *testing.T) {
	plain, ps := ResolveTransport(TransportConfig{EndpointOverride: "http://otlp.internal:4317"}, externalLB, lbHost)
	if !plain.Insecure || plain.Termination != TerminationNone {
		t.Errorf("http override = %+v, want plaintext", plain)
	}
	if plain.GRPCEndpoint != "otlp.internal:4317" {
		t.Errorf("override endpoint = %q, want the scheme stripped for the gRPC exporter", plain.GRPCEndpoint)
	}
	var noted bool
	for _, p := range ps {
		if p.Code == "OverrideEndpointPlaintext" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("problems = %v, want the plaintext override reported", problemCodes(ps))
	}

	secure, ps := ResolveTransport(TransportConfig{EndpointOverride: "https://otlp.internal:4317"}, externalLB, lbHost)
	if secure.Insecure || secure.Trust != TrustPublic {
		t.Errorf("https override = %+v, want a secure transport", secure)
	}
	if anyError(ps) {
		t.Errorf("https override refused: %v", problemCodes(ps))
	}
}

// TestInsecureAlwaysMatchesTermination is the invariant that keeps the client side honest: the flag the
// exporter is configured with must agree with whether anything actually terminates TLS. A transport that
// reports Insecure=false with nothing terminating would tell every workload to verify a certificate that
// is not there; the reverse would send plaintext to a listener expecting TLS.
func TestInsecureAlwaysMatchesTermination(t *testing.T) {
	configs := []struct {
		cfg TransportConfig
		p   Platform
	}{
		{TransportConfig{}, externalLB},
		{TransportConfig{}, managed},
		{TransportConfig{Mode: TLSOff}, externalLB},
		{TransportConfig{Mode: TLSOff}, managed},
		{TransportConfig{Mode: TLSPlatform}, managed},
		{TransportConfig{Mode: TLSLoadBalancer, Hostname: "h.example.com", CertificateRef: "ref"}, externalLB},
		{TransportConfig{Mode: TLSCollector, Hostname: "h.example.com", CertificateRef: "secret"}, externalLB},
		{TransportConfig{EndpointOverride: "http://o:4317"}, externalLB},
		{TransportConfig{EndpointOverride: "https://o:4317"}, externalLB},
		{TransportConfig{Mode: TLSCollector, Hostname: "h.example.com", CertificateRef: "s", EndpointOverride: "http://o:4317"}, externalLB},
	}
	for _, c := range configs {
		tr, ps := ResolveTransport(c.cfg, c.p, lbHost)
		if anyError(ps) {
			continue
		}
		if tr.Insecure != (tr.Termination == TerminationNone) {
			t.Errorf("%+v: insecure = %v but termination = %q", c.cfg, tr.Insecure, tr.Termination)
		}
		if tr.Insecure != strings.HasPrefix(tr.HTTPEndpoint, "http://") {
			t.Errorf("%+v: insecure = %v disagrees with the HTTP scheme %q", c.cfg, tr.Insecure, tr.HTTPEndpoint)
		}
		if (tr.Trust == TrustNone) != tr.Insecure {
			t.Errorf("%+v: trust = %q with insecure = %v", c.cfg, tr.Trust, tr.Insecure)
		}
	}
}

// TestOneModelForBothPaths: the resolution takes the network's configuration, the platform and the
// published host — and nothing about WHO deployed the Collector. That is what makes the endpoint the
// automatic deployer configures, the endpoint the published artifacts document, and the endpoint the
// observer compares against one value by construction, the same property the routing table relies on.
//
// Every mode must yield a complete, usable pair of endpoints, because a manual-mode artifact has to
// render them into a document with no second resolver to fall back on.
func TestOneModelForBothPaths(t *testing.T) {
	cases := []struct {
		cfg TransportConfig
		p   Platform
	}{
		{TransportConfig{Mode: TLSOff}, externalLB},
		{TransportConfig{Mode: TLSPlatform}, managed},
		{TransportConfig{Mode: TLSLoadBalancer, Hostname: "h.example.com", CertificateRef: "ref"}, externalLB},
		{TransportConfig{Mode: TLSCollector, Hostname: "h.example.com", CertificateRef: "secret"}, externalLB},
	}
	for _, c := range cases {
		tr, ps := ResolveTransport(c.cfg, c.p, lbHost)
		if anyError(ps) {
			t.Fatalf("%v: refused with %v", c.cfg.Mode, problemCodes(ps))
		}
		if tr.GRPCEndpoint == "" || tr.HTTPEndpoint == "" {
			t.Errorf("%v: endpoints = %q / %q, want both populated for an artifact to render", c.cfg.Mode, tr.GRPCEndpoint, tr.HTTPEndpoint)
		}
		if tr.Summary() == "" {
			t.Errorf("%v: no summary for the status message", c.cfg.Mode)
		}
	}
}
