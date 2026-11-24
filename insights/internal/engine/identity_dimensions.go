package engine

import (
	"strings"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// applyIdentityDimensions appends the identity attributes (see
// identityAttributes) onto Operation, in their order. Format:
// "POST /buy [key=value]" with deterministic escaping so values cannot break
// the bracket structure.
func applyIdentityDimensions(operation string, attrs []repo.TransactionIdentityValue) string {
	if len(attrs) == 0 {
		return operation
	}
	var b strings.Builder
	b.WriteString(operation)
	b.WriteByte(' ')
	for _, a := range attrs {
		b.WriteByte('[')
		b.WriteString(repo.EscapeIdentityDimToken(a.Key))
		b.WriteByte('=')
		b.WriteString(repo.EscapeIdentityDimToken(a.Value))
		b.WriteByte(']')
	}
	return b.String()
}

// identityAttributes reads the enabled low-cardinality dimensions from the
// sub-transaction entry span (root), in settings order. Missing values are
// omitted (no synthetic "unknown").
func identityAttributes(root *Span, dims []repo.TransactionIdentityDimension) []repo.TransactionIdentityValue {
	attrs := []repo.TransactionIdentityValue{}
	if root == nil {
		return attrs
	}
	for _, d := range dims {
		if !d.Enabled || d.Key == "" {
			continue
		}
		if val := resolveIdentityDimension(root, d.Key); val != "" {
			attrs = append(attrs, repo.TransactionIdentityValue{Key: d.Key, Value: val})
		}
	}
	return attrs
}

func resolveIdentityDimension(root *Span, key string) string {
	switch key {
	case repo.IdentityDimHTTPStatusCode:
		return firstNonEmpty(root.Attr(semconv.HTTPResponseStatusCode), root.Attr(semconv.LegacyHTTPStatusCode))
	case repo.IdentityDimRPCGRPCStatus:
		return root.Attr(semconv.RPCGRPCStatusCode)
	default:
		return root.Attr(key)
	}
}
