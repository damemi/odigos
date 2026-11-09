package k8s

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// args.go holds the small helpers every tool handler uses to read values out of an
// MCP call request. Each "Arg" helper pulls one typed value from the request's
// argument map, returning a caller-supplied default when the argument is absent —
// so handlers don't repeat the same type-assertion boilerplate. The "Ptr" variants
// return nil when the argument was not provided, for the cases where "unset" must
// be distinguished from the zero value.

// getToolRequestArgument returns the request's argument map, or nil if the request
// carried no arguments (or an unexpected shape).
func getToolRequestArgument(req mcp.CallToolRequest) map[string]any {
	if argMap, ok := req.Params.Arguments.(map[string]any); ok {
		return argMap
	}
	return nil
}

// stringArg returns the string argument named key, or def if it is absent.
func stringArg(req mcp.CallToolRequest, key, def string) string {
	if v, err := req.RequireString(key); err == nil {
		return v
	}
	return def
}

// boolArg returns the bool argument named key, or def if it is absent.
func boolArg(req mcp.CallToolRequest, key string, def bool) bool {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return def
	}
	if v, ok := argMap[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// boolArgPtr returns a *bool only if the caller explicitly provided key. Needed
// where "unset" must be distinguished from "false".
func boolArgPtr(req mcp.CallToolRequest, key string) *bool {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return nil
	}
	if v, ok := argMap[key]; ok {
		if b, ok := v.(bool); ok {
			return &b
		}
	}
	return nil
}

// intArg returns the int argument named key, or def if it is absent.
func intArg(req mcp.CallToolRequest, key string, def int) int {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return def
	}
	if v, ok := argMap[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return def
}

// int64ArgPtr returns a *int64 only if the caller explicitly provided key.
func int64ArgPtr(req mcp.CallToolRequest, key string) *int64 {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return nil
	}
	if v, ok := argMap[key]; ok {
		if f, ok := v.(float64); ok {
			i := int64(f)
			return &i
		}
	}
	return nil
}

// floatArg returns the float64 argument named key, or def if it is absent.
func floatArg(req mcp.CallToolRequest, key string, def float64) float64 {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return def
	}
	if v, ok := argMap[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return def
}

// stringSliceArg returns the []string argument named key, or nil if it is absent.
func stringSliceArg(req mcp.CallToolRequest, key string) []string {
	if s, err := req.RequireStringSlice(key); err == nil {
		return s
	}
	return nil
}

// requireStringMap parses an object argument whose values are all strings.
func requireStringMap(req mcp.CallToolRequest, key string) (map[string]string, error) {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return nil, fmt.Errorf("missing arguments")
	}
	raw, ok := argMap[key]
	if !ok {
		return nil, fmt.Errorf("missing required parameter %q", key)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parameter %q must be an object of string->string", key)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q must be a string", k)
		}
		out[k] = s
	}
	return out, nil
}

// decodeArg JSON round-trips a raw argument into a typed Go struct so handlers can
// accept complex nested config (action fields, sampler filters) and reuse the real
// API types for validation rather than hand-mapping every field. Returns false if
// the key was absent.
func decodeArg(req mcp.CallToolRequest, key string, out any) (bool, error) {
	argMap := getToolRequestArgument(req)
	if argMap == nil {
		return false, nil
	}
	raw, ok := argMap[key]
	if !ok || raw == nil {
		return false, nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(buf, out); err != nil {
		return false, fmt.Errorf("invalid %q: %w", key, err)
	}
	return true, nil
}
