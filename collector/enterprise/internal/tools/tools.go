//go:build tools

// Package tools pins the code generators used by the collector build. mdatagen's own go.mod carries
// replace directives, so `go install` can only resolve it as a dependency of a module such as this.
package tools

import (
	_ "go.opentelemetry.io/collector/cmd/mdatagen"
	_ "golang.org/x/tools/cmd/goimports"
)
