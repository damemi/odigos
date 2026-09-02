// Package api embeds the OpenAPI contract (openapi.yaml) so the running service
// can serve it verbatim. The spec is the source of truth for the HTTP API; the
// Go wire types (internal/api/http/apitypes) and the frontend client are derived
// from it. Served by the http router at GET /api/v1/openapi.yaml.
package api

import _ "embed"

// OpenAPISpec is the raw openapi.yaml bytes, embedded at build time.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
