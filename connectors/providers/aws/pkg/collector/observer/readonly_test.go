package observer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The guards in this file are the "manual observe-only" criterion turned into something that cannot be
// forgotten. The behavioural test (TestManualModeNeverWritesToAWS) proves this pass makes no write on the
// paths it exercises; these prove no write can be ADDED without somebody deciding to, which is the
// property that survives the next six changes to the package.

// sourceFiles are the package's non-test files.
func sourceFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = string(src)
	}
	if len(out) == 0 {
		t.Fatal("no source files found, so these guards would pass over nothing")
	}
	return out
}

// TestTheAWSClientInterfacesCanOnlyRead. The narrowing is the safety property: *ecs.Client can plainly
// delete a service, and an interface holding nothing but Describes and Lists cannot express it — so a
// mutating call is a compile error rather than a review comment.
func TestTheAWSClientInterfacesCanOnlyRead(t *testing.T) {
	src := sourceFiles(t)["describe.go"]
	if src == "" {
		t.Fatal("describe.go is gone; the client interfaces are unguarded")
	}
	for _, name := range []string{"ECSReader", "ELBReader"} {
		body := interfaceBody(t, src, name)
		methods := regexp.MustCompile(`(?m)^\s*([A-Z]\w*)\(`).FindAllStringSubmatch(body, -1)
		if len(methods) == 0 {
			t.Fatalf("%s declares no methods, so this guard covers nothing", name)
		}
		for _, m := range methods {
			if !strings.HasPrefix(m[1], "Describe") && !strings.HasPrefix(m[1], "List") {
				t.Errorf("%s.%s is not a read; this package must not be able to change a customer's "+
					"infrastructure, and in manual mode it is the only interface a Collector is reached through",
					name, m[1])
			}
		}
	}
}

// interfaceBody returns the declaration body of one interface.
func interfaceBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "type "+name+" interface {")
	if start < 0 {
		t.Fatalf("no %s interface in describe.go", name)
	}
	rest := src[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("%s's declaration does not end", name)
	}
	return rest[:end]
}

// TestNoMutatingAWSCallAppearsAnywhere. The interface guard above covers the calls this package declares;
// this covers the ones it could reach around them with — a second client built inline, or an SDK input
// type constructed directly.
func TestNoMutatingAWSCallAppearsAnywhere(t *testing.T) {
	// The SDK's own naming, which is what makes this checkable: every mutating operation on ECS and
	// ELBv2 begins with one of these, and every read begins with Describe or List.
	mutating := []string{
		"Create", "Update", "Delete", "Deregister", "Register", "Put", "Modify",
		"Authorize", "Revoke", "AddTags", "RemoveTags", "Tag", "Untag", "Stop", "Start", "Run",
	}
	// The two SDK package qualifiers a mutating call would have to come through.
	clients := regexp.MustCompile(`\b(ecs|elb|elasticloadbalancingv2)\.([A-Z]\w*)`)

	for name, src := range sourceFiles(t) {
		for _, match := range clients.FindAllStringSubmatch(src, -1) {
			symbol := match[2]
			for _, verb := range mutating {
				if strings.HasPrefix(symbol, verb) {
					t.Errorf("%s references %s.%s; nothing in this package may change a customer's AWS "+
						"account", name, match[1], symbol)
				}
			}
		}
	}
}

// TestTheObserverNeverCallsTheDeployer. The deployer is imported for its NAMES — the service name, the
// cluster, the container, the ports — and it has to be, because an observer that composed its own would
// look for objects the deployer never created. What it must never reach for is a converge: every
// Ensure* in that package writes to the customer's account.
func TestTheObserverNeverCallsTheDeployer(t *testing.T) {
	for name, src := range sourceFiles(t) {
		if strings.Contains(src, "deployer.Ensure") {
			t.Errorf("%s calls a deployer converge function; this package reads and never writes", name)
		}
		for _, forbidden := range []string{"deployer.NewClients", "deployer.Provisioner", "deployer.Clients"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s reaches for %s, which is the mutating half of the deployer", name, forbidden)
			}
		}
	}
}

// TestNothingHereRemovesACollectorsRow. A Collector that has disappeared from AWS is a finding to
// REPORT, not a row to remove: the row is what carries the report, and the refs in it are half of
// teardown's authority over the objects that are still standing. Deleting it would leak them
// silently and lose the only record that this network ever had a Collector.
func TestNothingHereRemovesACollectorsRow(t *testing.T) {
	for name, src := range sourceFiles(t) {
		if strings.Contains(src, "DeleteManagedCollector") {
			t.Errorf("%s can delete a Collector's row; a missing Collector is reported, never forgotten", name)
		}
	}
}

// TestTheStoreSurfaceIsOnlyWhatThisPassEstablishes. A narrow interface is what keeps the row's ownership
// division honest — a pass that could write the ingress tier or a credential would eventually write one,
// over a value only the converge or the credential lifecycle knows.
func TestTheStoreSurfaceIsOnlyWhatThisPassEstablishes(t *testing.T) {
	body := interfaceBody(t, sourceFiles(t)["observer.go"], "Store")
	allowed := map[string]bool{
		"GetManagedCollector":           true,
		"UpsertManagedCollector":        true,
		"RecordCollectorInfrastructure": true,
		"RecordCollectorEndpoint":       true,
		"RecordCollectorImage":          true,
		"RecordCollectorScaling":        true,
		"SetCollectorStatus":            true,
	}
	methods := regexp.MustCompile(`(?m)^\s*([A-Z]\w*)\(`).FindAllStringSubmatch(body, -1)
	if len(methods) != len(allowed) {
		t.Errorf("the Store interface has %d methods and %d are accounted for", len(methods), len(allowed))
	}
	for _, m := range methods {
		if !allowed[m[1]] {
			t.Errorf("Store.%s is not something an observation establishes; adding it is a decision about "+
				"who owns that column", m[1])
		}
	}
	// The two accessors this pass must NOT have, named so the reason is recorded rather than inferred
	// from an absence: a credential's lifetime is and the config hash is the OpAMP session's.
	for _, forbidden := range []string{"RecordCollectorIdentity", "RecordCollectorConfigPush", "RecordCollectorLiveness"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Store declares %s; that column belongs to the credential lifecycle or the OpAMP "+
				"session, not to an infrastructure observation", forbidden)
		}
	}
}
