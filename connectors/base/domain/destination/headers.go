package destination

import (
	"encoding/json"
	"strings"
)

// Per-request OTLP headers — in practice the destination's credential (Authorization: Bearer …, x-api-key,
// …). Agent-side instrumentation that exports straight to a destination needs them, and until now no
// connector read them at all, so an authenticated destination was silently unreachable from any workload
// the connector instruments.
//
// This lives in its own file, and returns a plain map rather than a new field on OTLPTarget, because
// OTLPTarget mirrors the destination definitions generated upstream in odigos: growing it here would fork a
// shape whose source of truth is another repository. Destination.Config is the generated surface, carried
// through verbatim, so the conversion belongs in code beside it instead — read the raw config outward, hand
// the instrumentation a map it can render.
const (
	// Same keys and the same wire format as odigos core's genericotlp/otlphttp config: a JSON array of
	// {key, value} objects rather than an object, because the UI edits headers as an ordered list of rows.
	configOTLPGRPCHeaders = "OTLP_GRPC_HEADERS"
	configOTLPHTTPHeaders = "OTLP_HTTP_HEADERS"
)

// ResolveOTLPHeaders returns the headers of the destination ResolveOTLPTarget selected, or nil when it needs
// none.
//
// It repeats that function's selection rule — the first destination carrying either endpoint — rather than
// sharing it, because sharing would mean changing otlp.go. TestResolveOTLPHeadersTracksTargetSelection pins
// the two to the same destination so the duplication cannot drift into reading a credential off one
// destination and exporting to another, which would leak a token to the wrong vendor.
func ResolveOTLPHeaders(dests []Destination) map[string]string {
	for _, d := range dests {
		if d.Disabled || isManagedCollectorType(d.Type) {
			continue
		}
		grpcEP := configString(d.Config, configOTLPGRPCEndpoint)
		httpEP := configString(d.Config, configOTLPHTTPEndpoint)
		if grpcEP == "" && httpEP == "" {
			continue
		}
		return parseOTLPHeaders(configString(d.Config, headersKeyFor(grpcEP)))
	}
	return nil
}

// headersKeyFor picks the headers key matching the transport the destination configured: one with a gRPC
// endpoint is an `otlp` destination and carries OTLP_GRPC_HEADERS, one without is `otlphttp` and carries
// OTLP_HTTP_HEADERS. Reading both and merging would attach an otlphttp destination's credential to a gRPC
// exporter.
func headersKeyFor(grpcEndpoint string) string {
	if grpcEndpoint != "" {
		return configOTLPGRPCHeaders
	}
	return configOTLPHTTPHeaders
}

// parseOTLPHeaders decodes the headers field.
//
// An absent, empty, malformed or entry-less value yields nil. Malformed is deliberately not an error: the
// alternative is refusing to instrument a workload over a header list we could not parse, and an export
// failing loudly against the destination is a far more legible symptom than instrumentation that never
// happens. An entry with an empty key is skipped rather than sent — an empty header name is rejected by
// gRPC and quietly dropped by most HTTP stacks.
func parseOTLPHeaders(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var list []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, h := range list {
		if h.Key == "" {
			continue
		}
		out[h.Key] = h.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
