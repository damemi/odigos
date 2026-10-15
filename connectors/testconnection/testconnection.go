package testconnection

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Input describes the connection to validate. The create-connector flow fills it per provider before
// any Secret or connector pod exists.
type Input struct {
	// Provider selects the registered Tester (e.g. "aws"). Required.
	Provider string
	// BootstrapRegion controls initial identity and region enumeration calls.
	BootstrapRegion string
	// Credentials holds the in-memory credentials under test, keyed by the provider spec's auth field
	// names (auth.methods[].fields[].name — e.g. "accessKeyId", "roleArn"). Provider-agnostic: the
	// tester reads the keys it understands and auto-resolves the auth method by which keys are present.
	// Empty means use the ambient/default credential chain.
	Credentials map[string]string
	// ExpectedAccountID is the provider account boundary id validated against the resolved identity.
	ExpectedAccountID string
	// Capabilities is the customer's per-type allow-list, split by domain (workloads/computePlatforms).
	// The tester probes only the active discovery types and validates exactly their required permissions.
	// All regions enabled in the account are scanned, independently of the bootstrap region.
	Capabilities spec.Capabilities
}

// Tester validates a single provider's credentials/permissions. Implementations register themselves
// in init() via Register so the create-connector flow can dispatch by provider without a hand-written
// switch. A Tester returns a non-nil error only for unexpected internal failures; expected outcomes
// (bad credentials, missing permissions) are reported as failed checks in the Result.
type Tester interface {
	// Provider returns the provider key this Tester handles (e.g. "aws").
	Provider() string
	Test(ctx context.Context, in Input) (*Result, error)
}

var registry = map[string]Tester{}

// Register adds a provider Tester to the dispatch registry. Called from each provider file's init().
func Register(t Tester) { registry[t.Provider()] = t }

// Run dispatches to the registered Tester for in.Provider. It returns an error for an unknown provider
// or an internal failure; per-check outcomes are carried in the Result.
func Run(ctx context.Context, in Input) (*Result, error) {
	t, ok := registry[in.Provider]
	if !ok {
		return nil, fmt.Errorf("unsupported connector provider %q", in.Provider)
	}
	region, err := spec.MustGet(in.Provider).Connector.BootstrapRegion.Resolve(in.BootstrapRegion)
	if err != nil {
		return nil, err
	}
	in.BootstrapRegion = region
	return t.Test(ctx, in)
}
