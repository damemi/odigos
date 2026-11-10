package destination

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connectorsRoot is the whole connectors tree — this module, the provider modules, and the spec. Scoped
// there because that is where every plan, instrumentor and observer lives; nothing outside it resolves a
// workload's export endpoint.
const connectorsRoot = "../../.."

// resolveDirectly may be called only where routing does not apply. Everywhere else must go through the
// network-aware resolution, or the endpoint Odigos applies and the endpoint Odigos compares against stop
// being one value.
var resolveDirectly = []string{"ResolveOTLPTarget(", "ResolveOTLPHeaders("}

// allowedDirectCallers are the files that may resolve without a network, each for a stated reason.
var allowedDirectCallers = map[string]string{
	// The definitions themselves, and the network-aware resolution built on top of them.
	"base/domain/destination/otlp.go":    "defines it",
	"base/domain/destination/headers.go": "defines it",
	"base/domain/destination/validate.go": "validates the destination set before routing",
	"base/domain/destination/network.go": "the network-aware resolution is built on it",
	// The Collector's OWN exporter. The Collector IS the egress tier: it exports straight to the
	// customer's destination and is never routed through itself, so there is no network to resolve for.
	"base/domain/collectorconfig/config.go": "renders the Collector's own export, which is never routed",
}

// TestEveryConsumerResolvesThroughOneFunction is the "all consumers agree" property, checked over the
// source rather than trusted.
//
// A plan, an instrumentor and an observer that resolved separately would drift the moment a Collector
// entered the picture: the instrumentor would point a workload at the Collector while the observer
// compared it against the destination, and every correctly-routed workload would be reported as drifted.
// Routing through one function makes that impossible; this keeps a second path from appearing.
func TestEveryConsumerResolvesThroughOneFunction(t *testing.T) {
	root, err := filepath.Abs(connectorsRoot)
	if err != nil {
		t.Fatalf("resolve %s: %v", connectorsRoot, err)
	}
	if _, err := os.Stat(filepath.Join(root, "base", "domain", "destination", "otlp.go")); err != nil {
		// Fatal rather than skipped: a silent skip turns this guard off exactly when the layout changes.
		t.Fatalf("%s does not look like the connectors tree: %v", root, err)
	}

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Tests are not consumers — they exercise both functions on purpose.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, allowed := allowedDirectCallers[rel]; allowed {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, call := range resolveDirectly {
			// Skip the "ForNetwork" siblings, which are the ones callers are supposed to use.
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, call) || strings.Contains(line, "ForNetwork") {
					continue
				}
				// Comments naming the function are how the code explains itself.
				if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
					continue
				}
				offenders = append(offenders, rel+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for _, o := range offenders {
		t.Errorf("resolves the export endpoint without a network: %s\n\tuse the store's OTLPRouting / "+
			"OTLPExportTargetForNetwork, or add this file to allowedDirectCallers with a reason", o)
	}
}

// TestAllowedDirectCallersStillExist: an exemption for a file that has been moved or deleted is an
// exemption nobody notices has stopped meaning anything.
func TestAllowedDirectCallersStillExist(t *testing.T) {
	root, err := filepath.Abs(connectorsRoot)
	if err != nil {
		t.Fatal(err)
	}
	for rel, reason := range allowedDirectCallers {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("allowedDirectCallers exempts %s (%q), which does not exist", rel, reason)
		}
	}
}
