package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d2Egress scores against the set of {host, port} peers reached by CLIENT or
// PRODUCER spans; new pairs are deviations (the service is talking somewhere new).
//
// JSONB shape:
//
//	{"peers": [{"host":"stripe.com","port":443}, ...]}
type d2Egress struct{}

type peer struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Owner and External are annotations for guardrail egress profiles (and
	// live forbiddenEgress on the current trace). They are NOT part of D2
	// Score/Merge identity - see coalesceD2Peers.
	Owner    string `json:"owner,omitempty"`
	External bool   `json:"external,omitempty"`
}

// peerKey is the identity D2 scores on - the {host,port} tuple, ignoring the
// service-scoped Owner/External annotations.
type peerKey struct {
	Host string
	Port int
}

func (p peer) key() peerKey { return peerKey{Host: p.Host, Port: p.Port} }

type d2Data struct {
	Peers []peer `json:"peers"`
}

func (d2Egress) Class() repo.DeviationClass { return repo.D2Egress }

func (s d2Egress) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.decodeBaseline(current).score(t)
}

// decodeBaseline implements baselineDecoder.
func (d2Egress) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD2(raw)
	return d
}

// score implements decodedBaseline; it only reads baseline.
func (baseline d2Data) score(t *Trace) (bool, json.RawMessage, error) {
	known := map[peerKey]struct{}{}
	for _, p := range baseline.Peers {
		known[p.key()] = struct{}{}
	}
	var newPeers []peer
	for _, p := range peersFromTrace(t) {
		if _, ok := known[p.key()]; !ok {
			newPeers = append(newPeers, p)
			known[p.key()] = struct{}{}
		}
	}
	if len(newPeers) == 0 {
		return false, nil, nil
	}
	ev, err := json.Marshal(map[string]any{"new_peers": newPeers})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

// D2 enricher-list keys (see the risk catalog for their defaults + UI wiring).
const (
	d2MetadataEndpointsKey repo.EnricherListKey = "metadata_endpoints"
	d2DangerousPortsKey    repo.EnricherListKey = "dangerous_ports"
)

// EnrichRisk implements RiskEnricher: it grades a D2 deviation by what the new
// peer is, escalating above the low base "new egress" signal for two sub-cases
// (both configurable per scope via cfg.list):
//
//   - Cloud metadata address (169.254.169.254 & friends): a hallmark of SSRF
//     used to steal instance credentials.
//   - Suspicious port (reverse-shell / C2 / tunneling, e.g. 4444): an attacker
//     channel. Since D2 keys on {host,port}, a known host on a new dangerous
//     port is itself a new peer, so this catches it too.
func (d2Egress) EnrichRisk(evidence json.RawMessage, cfg enrichConfig) []risk.Signal {
	if len(evidence) == 0 {
		return nil
	}
	var ev struct {
		NewPeers []peer `json:"new_peers"`
	}
	if err := json.Unmarshal(evidence, &ev); err != nil {
		return nil
	}

	var signals []risk.Signal
	if hits := metadataHits(ev.NewPeers, cfg); len(hits) > 0 {
		signals = append(signals, enricherSignal("d2_metadata_endpoint", risk.CategoryExfiltration,
			"new egress to cloud metadata endpoint(s): "+strings.Join(hits, ", ")))
	}
	if hits := dangerousPortHits(ev.NewPeers, cfg); len(hits) > 0 {
		signals = append(signals, enricherSignal("d2_dangerous_port", risk.CategoryExfiltration,
			"new egress on suspicious port(s): "+strings.Join(hits, ", ")))
	}
	return signals
}

// metadataHits returns the distinct new-peer hosts that exactly match a known
// cloud metadata address. Used by EnrichRisk.
func metadataHits(peers []peer, cfg enrichConfig) []string {
	known := cfg.list(d2MetadataEndpointsKey, risk.DefaultList(d2MetadataEndpointsKey))
	var hits []string
	seen := map[string]struct{}{}
	for _, p := range peers {
		if !isMetadataEndpoint(p.Host, known) {
			continue
		}
		if _, dup := seen[p.Host]; !dup {
			seen[p.Host] = struct{}{}
			hits = append(hits, p.Host)
		}
	}
	return hits
}

// dangerousPortHits returns the distinct new "host:port" descriptors whose port
// is in the scope's suspicious-port set. Used by EnrichRisk.
func dangerousPortHits(peers []peer, cfg enrichConfig) []string {
	ports := parsePortSet(cfg.list(d2DangerousPortsKey, risk.DefaultList(d2DangerousPortsKey)))
	if len(ports) == 0 {
		return nil
	}
	var hits []string
	seen := map[string]struct{}{}
	for _, p := range peers {
		if _, bad := ports[p.Port]; !bad {
			continue
		}
		desc := fmt.Sprintf("%s:%d", p.Host, p.Port)
		if _, dup := seen[desc]; !dup {
			seen[desc] = struct{}{}
			hits = append(hits, desc)
		}
	}
	return hits
}

// isMetadataEndpoint reports whether host exactly matches (case-insensitively)
// a known metadata address. Exact, not substring, so a peer like
// "my-169.254.169.254-proxy.example.com" doesn't trip it.
func isMetadataEndpoint(host string, known []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, k := range known {
		if host == strings.ToLower(strings.TrimSpace(k)) {
			return true
		}
	}
	return false
}

// parsePortSet turns textual port numbers into a lookup set, skipping blanks and
// non-numeric / non-positive entries (port 0 = "unknown" therefore never matches).
func parsePortSet(ports []string) map[int]struct{} {
	set := make(map[int]struct{}, len(ports))
	for _, raw := range ports {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			set[n] = struct{}{}
		}
	}
	return set
}

func (d2Egress) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	baseline, _ := decodeD2(current)
	peers := make([]peer, 0, len(baseline.Peers)+8)
	peers = append(peers, baseline.Peers...)
	peers = append(peers, peersFromTrace(t)...)
	return json.Marshal(d2Data{Peers: coalesceD2Peers(peers)})
}

// d2Learning holds D2's peers. Unlike D1/D5 a merge can rewrite an existing
// peer (coalesceD2Peers folds Owner/External), so change is a slice compare.
type d2Learning struct {
	peers  []peer
	synced bool
}

func (d2Egress) decodeLearning(raw json.RawMessage) learningBaseline {
	d, _ := decodeD2(raw)
	return &d2Learning{peers: d.Peers}
}

func (l *d2Learning) merge(t *Trace) (bool, bool) {
	traced := peersFromTrace(t)
	if l.synced && len(traced) == 0 {
		return false, false
	}
	// Same input order as d2Egress.Merge: coalescing Owner depends on it.
	merged := coalesceD2Peers(append(slices.Clone(l.peers), traced...))
	changed := !l.synced || !slices.Equal(merged, l.peers)
	l.peers = merged
	return changed, false
}

func (l *d2Learning) encode() (json.RawMessage, error) {
	out, err := json.Marshal(d2Data{Peers: l.peers})
	if err == nil {
		l.synced = true
	}
	return out, err
}

func (l *d2Learning) members() int { return len(l.peers) }

// coalesceD2Peers collapses peers to Score identity ({host,port}). Owner and
// External are annotations for guardrail profiles - they must not create extra
// set members or the baseline "grows" forever when the same call is sometimes
// flagged external. External sticks to internal once any observation saw the
// call as in-mesh; Owner prefers an in-mesh observation's owner, else a
// deterministic lexicographic pick.
func coalesceD2Peers(peers []peer) []peer {
	by := make(map[peerKey]peer, len(peers))
	for _, p := range peers {
		k := p.key()
		prev, ok := by[k]
		if !ok {
			by[k] = p
			continue
		}
		by[k] = peer{
			Host:     k.Host,
			Port:     k.Port,
			Owner:    coalesceD2Owner(prev, p),
			External: prev.External && p.External,
		}
	}
	out := make([]peer, 0, len(by))
	for _, p := range by {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return lessD2Peer(out[i], out[j]) })
	return out
}

func coalesceD2Owner(a, b peer) string {
	switch {
	case a.Owner == "":
		return b.Owner
	case b.Owner == "":
		return a.Owner
	case !a.External && b.External:
		return a.Owner
	case a.External && !b.External:
		return b.Owner
	case a.Owner < b.Owner:
		return a.Owner
	default:
		return b.Owner
	}
}

func lessD2Peer(a, b peer) bool {
	if a.Host != b.Host {
		return a.Host < b.Host
	}
	return a.Port < b.Port
}

func decodeD2(raw json.RawMessage) (d2Data, error) {
	if len(raw) == 0 {
		return d2Data{}, nil
	}
	var d d2Data
	err := json.Unmarshal(raw, &d)
	return d, err
}

// peersFromTrace extracts {host, port, owner, external} peers from outbound
// (CLIENT/PRODUCER) spans, trying OTel attrs in priority order and defaulting
// port to 0 when absent. Used by Score and Merge.
func peersFromTrace(t *Trace) []peer {
	serviceNames := serviceEntryNames(t)
	var out []peer
	for _, s := range t.Spans {
		if s.Kind != tracepb.Span_SPAN_KIND_CLIENT && s.Kind != tracepb.Span_SPAN_KIND_PRODUCER {
			continue
		}
		host := firstNonEmpty(
			s.Attr(semconv.ServerAddress),
			s.Attr(semconv.LegacyNetPeerName),
			s.Attr(semconv.LegacyHTTPHost),
			s.Attr(semconv.LegacyDBConnectionString), // crude, but better than nothing for DB peers
		)
		if host == "" {
			continue
		}
		port := atoiOrZero(firstNonEmpty(
			s.Attr(semconv.ServerPort),
			s.Attr(semconv.LegacyNetPeerPort),
		))
		// A call is external only if it neither reached an instrumented service
		// in-trace nor names one by host - see externalPeer.
		out = append(out, peer{
			Host:     host,
			Port:     port,
			Owner:    s.Service,
			External: externalPeer(s, host, serviceNames),
		})
	}
	return out
}

// externalPeer reports whether an outbound call is a DIRECT external
// destination rather than an internal service edge. A call is internal when its
// callee's SERVER span nests under it (hasDescendantServiceEntry, the strong
// signal) or its host names an instrumented service in this trace (isInMeshHost,
// the fallback for un-nested callees). A raw IP or third-party host matches
// neither and stays external.
func externalPeer(s *Span, host string, serviceNames map[string]struct{}) bool {
	return !hasDescendantServiceEntry(s) && !isInMeshHost(host, serviceNames)
}

// hasDescendantServiceEntry reports whether an outbound span reached another
// instrumented service in-trace (any descendant is a SERVER/CONSUMER entry).
// Such a call is an internal edge (D1's job), not external egress.
func hasDescendantServiceEntry(s *Span) bool {
	for _, child := range s.Children {
		if isServiceEntry(child) || hasDescendantServiceEntry(child) {
			return true
		}
	}
	return false
}

// serviceEntryNames collects the lowercased names of every service-entry
// (SERVER/CONSUMER) span in the trace, so isInMeshHost can reclassify a client
// peer as internal when its host names one of them.
func serviceEntryNames(t *Trace) map[string]struct{} {
	names := map[string]struct{}{}
	for _, s := range t.Spans {
		if isServiceEntry(s) && s.Service != "" {
			names[strings.ToLower(s.Service)] = struct{}{}
		}
	}
	return names
}

// isInMeshHost reports whether host names an instrumented service in this trace.
// Matches the full host or its first DNS label (so both "geolocation" and
// "geolocation.default.svc.cluster.local" match service "geolocation"),
// case-insensitively. A raw IP has no matching label and won't match.
func isInMeshHost(host string, serviceNames map[string]struct{}) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if _, ok := serviceNames[host]; ok {
		return true
	}
	if label, _, found := strings.Cut(host, "."); found {
		if _, ok := serviceNames[label]; ok {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
