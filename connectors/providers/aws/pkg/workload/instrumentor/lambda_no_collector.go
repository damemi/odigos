package instrumentor

import (
	"fmt"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
)

const lambdaNoCollectorNote = "This distro runs without the in-sandbox collector: the SDK exports straight to the destination before the function returns its response, so a slow or unreachable destination delays every invocation (each wait is capped at one second), and exports that fail or take longer are dropped rather than retried. It requires an OTLP/HTTP destination reachable from the function, and does not collect function output (stdout/stderr) logs."

// validateLambdaNoCollectorTarget rejects a collector-less distro for a destination without an OTLP/HTTP
// endpoint: every bundled SDK is configured for OTLP/HTTP, and only the collector can translate to gRPC.
func validateLambdaNoCollectorTarget(language, distro string, target domaindest.OTLPTarget) error {
	if recipe := recipeFor(language, distro); recipe.NoCollector && target.HTTPEndpoint == "" {
		return fmt.Errorf("%w: Lambda distro %s exports without a collector and requires a destination with an OTLP/HTTP endpoint", ErrUnsupportedInstrumentation, recipe.Name)
	}
	return nil
}

// lambdaExportTarget is the destination as the function exports to it. A collector-less SDK speaks only
// OTLP/HTTP, so its target drops any gRPC endpoint and every rendered configuration and comparison follows.
func lambdaExportTarget(language, distro string, target domaindest.OTLPTarget) domaindest.OTLPTarget {
	if recipeFor(language, distro).NoCollector {
		target.Endpoint = ""
		target.Insecure = false
	}
	return target
}

// lambdaFunctionLogsEnabled reports whether the collector captures function output (stdout/stderr) as logs:
// only for SDKs that cannot bridge logging frameworks, and only when the function runs a collector.
func lambdaFunctionLogsEnabled(target domaindest.OTLPTarget, webServer bool, runtime, distro string) bool {
	language := LanguageFromRuntime(runtime)
	return target.Logs && !webServer && !recipeFor(language, distro).NoCollector && lambdaFunctionLogs(language, execWrapperForRuntime(runtime, distro))
}
