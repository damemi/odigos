// Package opamp is the contract for the public OpAMP endpoint a managed Collector connects to.
//
// One endpoint serves the whole fleet across AWS, Azure and GCP — `wss://<central-host>/v1/opamp` —
// and it is per environment, not per connector: staging and production have different Central
// installations and therefore different endpoints. It is set once in the odigos-central chart, stamped
// onto every connector Deployment by connector-runtime, and stamped from there into each Collector's
// workload definition when the connector deploys one.
//
// Pure — no I/O. Lives in the root connectors module so connector-runtime (which stamps it) and
// connectors/base (which consumes it) validate against exactly the same rule. Two copies of a URL
// check is how a controller happily stamps an endpoint the connector then refuses.
//
// See collector/managed/OPAMP_ROUTING.md.
package opamp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	// EnvEndpoint is the variable carrying the endpoint, on both the connector Deployment and the
	// Collector's own workload (collector/managed/README.md).
	EnvEndpoint = "ODIGOS_OPAMP_ENDPOINT"
	// DefaultPath is OpAMP's default endpoint path, and the one connector-opamp-gateway serves.
	DefaultPath = "/v1/opamp"
)

// ErrNotConfigured means no endpoint was set at all.
//
// Distinguished from a malformed one because they call for different words: nothing is broken here,
// the feature is simply not configured, and a Collector is not deployed rather than deployed wrongly.
var ErrNotConfigured = errors.New("no public OpAMP endpoint is configured")

// Schemes the OpAMP Supervisor accepts for server::endpoint. Anything Odigos allows has to be in this
// set, or the Collector refuses its own configuration at startup and nothing upstream of it knows why.
var supervisorSchemes = map[string]bool{"wss": true, "ws": true, "https": true, "http": true}

// secureSchemes are the ones that may carry the credential across a network.
var secureSchemes = map[string]bool{"wss": true, "https": true}

// Resolve validates a configured endpoint and returns the normalized form to stamp.
//
// Normalizing rather than passing the string through means two spellings of the same endpoint compare
// equal, which is what lets Changed tell a real endpoint move from a cosmetic edit — and a cosmetic
// edit would otherwise redeploy every Collector in the fleet.
func Resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrNotConfigured
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		return "", fmt.Errorf("%q has no scheme; the public OpAMP endpoint is a URL, normally wss://<central-host>%s", raw, DefaultPath)
	}
	if !supervisorSchemes[scheme] {
		return "", fmt.Errorf("scheme %q is not one the Collector's Supervisor accepts (wss, ws, https, http); use wss", scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	if u.User != nil {
		// A credential in the endpoint would be copied into every Collector's workload definition, which
		// is the one place requires a credential never to appear in plaintext. The OpAMP credential
		// travels as a secret-backed Authorization header, never in the URL.
		return "", fmt.Errorf("the public OpAMP endpoint must not contain credentials; the Collector authenticates with a bearer credential injected from the platform's secret store")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		// Nothing reads either, so one is a typo — and a typo'd endpoint still connects, to the wrong place.
		return "", fmt.Errorf("the public OpAMP endpoint must not carry a query string or fragment, got %q", raw)
	}
	if !secureSchemes[scheme] && !isLoopback(u.Hostname()) {
		// The credential this endpoint carries buys the customer's destination credential out of a
		// configuration push. Plaintext is allowed only where the connection cannot leave the
		// machine — a port-forwarded gateway in development.
		return "", fmt.Errorf("refusing a plaintext %s:// endpoint to %q: the Collector presents a bearer credential on this connection, and that credential can read the destination's own credential out of a configuration push. Use wss://. Plaintext is accepted only for a loopback host in development",
			scheme, u.Hostname())
	}

	normalized := &url.URL{Scheme: scheme, Host: strings.ToLower(u.Host), Path: u.Path}
	if normalized.Path == "" || normalized.Path == "/" {
		// OpAMP's default, and what the gateway serves. Filled in rather than refused: an operator who
		// configures the host has said everything Odigos needs, and the path is not theirs to choose.
		normalized.Path = DefaultPath
	}
	return normalized.String(), nil
}

// Configured reports whether an endpoint was set, without judging it. Lets a caller separate "the
// managed-Collector feature was never turned on" from "it was configured wrongly".
func Configured(raw string) bool { return strings.TrimSpace(raw) != "" }

// Changed reports whether a Collector deployed against `deployed` would have to be redeployed to reach
// `configured`.
//
// Compared after normalization, so re-spelling the same endpoint is not a change. This is the check
// that decides whether an endpoint edit costs a fleet-wide redeploy: a Collector holds the endpoint
// injected into its workload definition at deploy time, exactly as it holds its credential, so moving
// Central does not move the Collectors already out there.
//
// An unresolvable value on either side counts as no change: refusing to act on a broken configuration
// is what keeps a typo in the chart from redeploying every Collector in the fleet.
func Changed(deployed, configured string) bool {
	was, err := Resolve(deployed)
	if err != nil {
		return false
	}
	now, err := Resolve(configured)
	if err != nil {
		return false
	}
	return was != now
}

// isLoopback reports whether a host can only be reached from the machine making the connection.
func isLoopback(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// How connector-opamp-gateway finds a connector's OpAMP server, and how that server is admitted to.
//
// These live in this module rather than in connectors/base because connector-runtime — which creates
// the Service, the label and the NetworkPolicy — must not depend on the base's whole surface to learn
// three strings. One definition, three consumers: the runtime that writes them, the gateway that reads
// them, and the base that serves behind them.
const (
	// LabelConnectorID carries the connector CR UID on the generated Deployment and Service.
	//
	// The gateway resolves the Service by this LABEL rather than by recomputing the connector's workload
	// name: the naming rule lives in connector-runtime, and a copy of it in the gateway would be correct
	// until the day it was not — with the failure being a Collector routed to a name nothing answers on
	// while every component reports healthy.
	//
	// Deliberately the same spelling as api.TagConnectorID on a Collector's cloud objects: both answer
	// "which connector owns this", on either side of the account boundary.
	LabelConnectorID = "odigos.io/connector-id"
	// ServicePortName is the connector Service port the gateway proxies to. Matched by NAME, because a
	// Service carries several ports and their order is not a contract.
	ServicePortName = "opamp"
	// ServerPort is the connector's OpAMP listener.
	ServerPort = 4320

	// LabelComponent / GatewayComponent select connector-opamp-gateway pods, which is what the
	// connector's NetworkPolicy admits. The gateway's own chart sets them.
	LabelComponent   = "odigos.io/component"
	GatewayComponent = "connector-opamp-gateway"
)
