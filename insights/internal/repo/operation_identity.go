package repo

import (
	"strings"
)

// TransactionIdentityValue is one parsed key=value pair from a stored operation
// identity suffix (see SplitOperationIdentity).
type TransactionIdentityValue struct {
	Key   string
	Value string
}

// Built-in identity dimension keys (single source of truth for engine, defaults, UI).
const (
	IdentityDimHTTPStatusCode = "http.response.status_code"
	IdentityDimRPCGRPCStatus  = "rpc.grpc.status_code"
)

// SplitOperationIdentity splits a stored operation into the entry-span name and
// any appended [key=value] identity dimensions. When no suffix is present,
// returns the full string as the name and a nil slice.
func SplitOperationIdentity(operation string) (name string, dims []TransactionIdentityValue) {
	idx := strings.Index(operation, " [")
	if idx < 0 {
		return operation, nil
	}
	name = operation[:idx]
	rest := operation[idx+1:]
	for len(rest) > 0 && rest[0] == '[' {
		key, val, n, ok := parseIdentityBracketSegment(rest)
		if !ok {
			break
		}
		dims = append(dims, TransactionIdentityValue{Key: key, Value: val})
		rest = rest[n:]
	}
	return name, dims
}

func parseIdentityBracketSegment(s string) (key, val string, consumed int, ok bool) {
	if len(s) < 3 || s[0] != '[' {
		return "", "", 0, false
	}
	i := 1
	keyStart := i
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		if s[i] == '=' {
			key = unescapeIdentityDimToken(s[keyStart:i])
			i++
			valStart := i
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				if s[i] == ']' {
					val = unescapeIdentityDimToken(s[valStart:i])
					return key, val, i + 1, true
				}
				i++
			}
			return "", "", 0, false
		}
		i++
	}
	return "", "", 0, false
}

// EscapeIdentityDimToken escapes characters that would break [key=value] parsing.
func EscapeIdentityDimToken(s string) string {
	if !strings.ContainsAny(s, "[]=\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\\', '[', ']', '=':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func unescapeIdentityDimToken(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
