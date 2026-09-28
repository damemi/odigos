package collectoringress

import (
	"fmt"
	"strings"
)

// TLSMode is where workload-to-Collector traffic is encrypted, and who holds the certificate.
// Per region (`collector.regions.<region>.tls.mode`). Same setting in both modes: automatic applies it,
// IaC takes it as a parameter, ResolveTransport produces the endpoint instrumentor and observer both read.
type TLSMode string

const (
	// TLSOff: plaintext OTLP; provider ingress is the whole boundary.
	TLSOff TLSMode = "off"
	// TLSPlatform: provider ingress terminates TLS with a certificate it manages.
	TLSPlatform TLSMode = "platform"
	// TLSLoadBalancer: LB terminates TLS with a customer-supplied cert for a hostname they own.
	// A publicly issued certificate means no trust material is distributed to workloads.
	TLSLoadBalancer TLSMode = "loadBalancer"
	// TLSCollector: Collector terminates TLS from a cert/key in the provider secret store.
	// Escape hatch for private PKI; every sender must already trust the issuing CA.
	TLSCollector TLSMode = "collector"
)

// Termination is where the TLS session ends. Distinct from TLSMode so readers can see which hop
// (if any) is still plaintext — e.g. LB→tasks after TLSLoadBalancer.
type Termination string

const (
	TerminationNone         Termination = "none"
	TerminationPlatform     Termination = "platform"
	TerminationLoadBalancer Termination = "load-balancer"
	TerminationCollector    Termination = "collector"
)

// Trust is what the sender needs to verify the Collector's certificate — the cost of a TLS mode,
// and why TLSCollector is never the default.
type Trust string

const (
	// TrustNone: no TLS, no verification.
	TrustNone Trust = "none"
	// TrustPublic: default trust store suffices; nothing to distribute.
	TrustPublic Trust = "public"
	// TrustCustom: workload must already trust a CA Odigos does not manage or distribute.
	TrustCustom Trust = "custom"
)

// Platform is what a provider's ingress can terminate. Passed in (not switched on provider name)
// so one resolver serves all providers.
type Platform struct {
	Name            string // for problem messages
	ManagedTLS      bool   // platform ingress terminates TLS with a cert it manages
	LoadBalancerTLS bool   // an LB in front can terminate TLS with a customer cert
}

// DefaultMode is platform TLS when the provider offers it free, plaintext otherwise.
func (p Platform) DefaultMode() TLSMode {
	if p.ManagedTLS {
		return TLSPlatform
	}
	return TLSOff
}

// TransportConfig is regional transport (`collector.regions.<region>.tls` + endpointOverride).
type TransportConfig struct {
	// Mode is empty for the platform default.
	Mode TLSMode
	// Hostname workloads connect to; required when presenting a customer certificate
	// (LB generated DNS names are not issuable).
	Hostname string
	// CertificateRef is the provider-specific certificate or secret reference. Empty when unused.
	CertificateRef string
	// EndpointOverride replaces the Odigos-created LB. When set, Odigos creates none and only
	// reads the scheme of what the customer pointed at.
	EndpointOverride string
}

// Transport is the resolved endpoint workloads are pointed at, and what that costs.
type Transport struct {
	Mode         TLSMode
	Termination  Termination
	Trust        Trust
	GRPCEndpoint string // host:port, no scheme — destination.OTLPTarget.Endpoint
	HTTPEndpoint string // URL — destination.OTLPTarget.HTTPEndpoint
	Insecure     bool   // destination.OTLPTarget.Insecure
}

// ResolveTransport turns transport config into the workload endpoint plus reportable posture.
// host is what Odigos would publish without an override (LB DNS or platform ingress FQDN).
//
// Refusals are config errors that name the key. Never "fixes" TLS by disabling verification —
// verified-nothing is the same exposure as plaintext wearing a padlock.
func ResolveTransport(cfg TransportConfig, p Platform, host string) (Transport, []Problem) {
	mode := cfg.Mode
	if mode == "" {
		mode = p.DefaultMode()
	}

	var problems []Problem
	refuse := func(code, msg string) (Transport, []Problem) {
		return Transport{Mode: mode, Termination: TerminationNone, Trust: TrustNone, Insecure: true},
			append(problems, Problem{Code: code, Severity: SeverityError, Message: msg})
	}

	switch mode {
	case TLSOff, TLSPlatform, TLSLoadBalancer, TLSCollector:
	default:
		return refuse("UnknownTLSMode", fmt.Sprintf(
			"collector.regions.<region>.tls.mode is %q; valid values are off, platform, loadBalancer and collector", mode))
	}

	if mode == TLSPlatform && !p.ManagedTLS {
		return refuse("PlatformTLSUnavailable", fmt.Sprintf(
			"collector.regions.<region>.tls.mode is platform, but %s has no ingress that terminates TLS with a "+
				"certificate it manages. Use loadBalancer with a provider certificate for a hostname you own, or "+
				"collector with a certificate in the secret store", p.Name))
	}
	if mode == TLSLoadBalancer {
		if !p.LoadBalancerTLS {
			return refuse("LoadBalancerTLSUnavailable", fmt.Sprintf(
				"collector.regions.<region>.tls.mode is loadBalancer, but %s does not put a TLS-terminating load "+
					"balancer in front of the Collector. Use collector mode instead", p.Name))
		}
		if cfg.EndpointOverride != "" {
			return refuse("TLSListenerWithoutLoadBalancer",
				"collector.regions.<region>.tls.mode is loadBalancer while endpointOverride is set, so Odigos "+
					"creates no load balancer to put the certificate on. Either clear endpointOverride, or use "+
					"collector mode so the Collector terminates TLS behind your own front")
		}
	}
	if mode == TLSLoadBalancer || mode == TLSCollector {
		if cfg.Hostname == "" {
			return refuse("TLSHostnameRequired", fmt.Sprintf(
				"collector.regions.<region>.tls.mode is %s but tls.hostname is empty. A certificate cannot be "+
					"issued for the load balancer's own generated DNS name, so you must supply a hostname you "+
					"control and point it at the Collector", mode))
		}
		if cfg.CertificateRef == "" {
			return refuse("TLSCertificateRequired", fmt.Sprintf(
				"collector.regions.<region>.tls.mode is %s but tls.certificateRef is empty; set it to the provider "+
					"certificate reference or the secret holding the certificate and key", mode))
		}
	}

	// Override replaces the endpoint entirely — what it says outranks anything Odigos would configure.
	if cfg.EndpointOverride != "" {
		return overrideTransport(cfg, mode, problems)
	}

	h := host
	if cfg.Hostname != "" {
		h = cfg.Hostname
	}
	if h == "" {
		// Pending, not a refusal: address does not exist yet. Callers gate on empty GRPCEndpoint.
		return Transport{Mode: mode, Termination: TerminationNone, Trust: TrustNone, Insecure: true},
			append(problems, Problem{
				Code:     "EndpointUnknown",
				Severity: SeverityPending,
				Message: "the Collector has no published address yet, so no workload can be pointed at it; " +
					"this clears once its load balancer or platform ingress exists",
			})
	}

	t := Transport{
		Mode:         mode,
		GRPCEndpoint: fmt.Sprintf("%s:%d", h, PortOTLPGRPC),
	}
	switch mode {
	case TLSOff:
		t.Termination, t.Trust, t.Insecure = TerminationNone, TrustNone, true
		t.HTTPEndpoint = fmt.Sprintf("http://%s:%d", h, PortOTLPHTTP)
		problems = append(problems, Problem{
			Code:     "PlaintextTelemetry",
			Severity: SeverityInfo,
			Message: "workloads export to this Collector in plaintext; its network rules are the only thing " +
				"restricting who can write to it, and it forwards under your destination credential. Set " +
				"collector.regions.<region>.tls to encrypt the hop",
		})
	case TLSPlatform:
		t.Termination, t.Trust = TerminationPlatform, TrustPublic
		t.HTTPEndpoint = fmt.Sprintf("https://%s:%d", h, PortOTLPHTTP)
	case TLSLoadBalancer:
		t.Termination, t.Trust = TerminationLoadBalancer, TrustPublic
		t.HTTPEndpoint = fmt.Sprintf("https://%s:%d", h, PortOTLPHTTP)
		problems = append(problems, Problem{
			Code:     "TLSTerminatesAtLoadBalancer",
			Severity: SeverityInfo,
			Message: "TLS terminates at the load balancer; the hop from it to the Collector's tasks stays " +
				"inside the network in plaintext. Use collector mode to encrypt that hop too",
		})
	case TLSCollector:
		t.Termination, t.Trust = TerminationCollector, TrustCustom
		t.HTTPEndpoint = fmt.Sprintf("https://%s:%d", h, PortOTLPHTTP)
		problems = append(problems, Problem{
			Code:     "TLSRequiresClientTrust",
			Severity: SeverityInfo,
			Message: "every workload sending to this Collector must already trust the CA that issued " +
				"tls.certificateRef. Odigos does not distribute trust material and will not disable " +
				"verification to compensate, so a workload that does not trust it fails to export",
		})
	}
	return t, problems
}

// overrideTransport reports what the customer's front appears to do from its scheme.
// Odigos created nothing here and does not guess past what it can read.
func overrideTransport(cfg TransportConfig, mode TLSMode, problems []Problem) (Transport, []Problem) {
	ep := cfg.EndpointOverride
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://"), "/")
	secure := strings.HasPrefix(ep, "https://") || mode == TLSCollector

	t := Transport{Mode: mode, Insecure: !secure, GRPCEndpoint: host}
	switch {
	case mode == TLSCollector:
		t.Termination, t.Trust = TerminationCollector, TrustCustom
	case secure:
		t.Termination, t.Trust = TerminationLoadBalancer, TrustPublic
	default:
		t.Termination, t.Trust = TerminationNone, TrustNone
	}
	if secure {
		t.HTTPEndpoint = "https://" + host
	} else {
		t.HTTPEndpoint = "http://" + host
	}

	if !secure {
		problems = append(problems, Problem{
			Code:     "OverrideEndpointPlaintext",
			Severity: SeverityInfo,
			Message: fmt.Sprintf("collector.regions.<region>.endpointOverride is %q, which Odigos publishes as "+
				"plaintext. Odigos creates nothing here and cannot see what your endpoint does; use an https "+
				"scheme if it terminates TLS", ep),
		})
	}
	return t, problems
}

// Summary is the one-line status description. Callers branch on Transport fields, never on this string.
func (t Transport) Summary() string {
	switch t.Mode {
	case TLSOff:
		return fmt.Sprintf("otlp://%s (plaintext)", t.GRPCEndpoint)
	case TLSPlatform:
		return fmt.Sprintf("otlps://%s (TLS, platform certificate)", t.GRPCEndpoint)
	case TLSLoadBalancer:
		return fmt.Sprintf("otlps://%s (TLS to the load balancer)", t.GRPCEndpoint)
	case TLSCollector:
		return fmt.Sprintf("otlps://%s (TLS to the Collector, your CA)", t.GRPCEndpoint)
	}
	return t.GRPCEndpoint
}
