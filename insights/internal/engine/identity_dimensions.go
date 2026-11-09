package engine

import (
	"strings"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// applyIdentityDimensions appends enabled low-cardinality dimensions from the
// sub-transaction entry span (root) onto Operation. Missing values are omitted
// (no synthetic "unknown"). Format: "POST /buy [key=value]" with deterministic
// escaping so values cannot break the bracket structure.
func applyIdentityDimensions(operation string, root *Span, dims []repo.TransactionIdentityDimension) string {
	if root == nil || len(dims) == 0 {
		return operation
	}
	var b strings.Builder
	b.WriteString(operation)
	wroteSep := false
	for _, d := range dims {
		if !d.Enabled || d.Key == "" {
			continue
		}
		val := resolveIdentityDimension(root, d.Key)
		if val == "" {
			continue
		}
		if !wroteSep {
			b.WriteByte(' ')
			wroteSep = true
		}
		b.WriteByte('[')
		b.WriteString(repo.EscapeIdentityDimToken(d.Key))
		b.WriteByte('=')
		b.WriteString(repo.EscapeIdentityDimToken(val))
		b.WriteByte(']')
	}
	return b.String()
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
