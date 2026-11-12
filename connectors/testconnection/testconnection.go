package testconnection

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/spec"
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

// Leave time for persistence and a structured response before the UI proxy's
// 120-second deadline. Provider SDK retries must not leave onboarding unbounded.
const VerificationTimeout = 90 * time.Second

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
	ctx, cancel := context.WithTimeout(ctx, VerificationTimeout)
	defer cancel()
	res, err := t.Test(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("%s verification failed: %s", in.Provider, SafeMessage(in.Provider, in.Credentials, err.Error()))
	}
	if res == nil {
		return nil, fmt.Errorf("%s verification returned no result", in.Provider)
	}
	if ctx.Err() != nil {
		res.add(CheckResult{Name: "verification_timeout", DisplayName: "Verification deadline", Message: "Provider verification did not complete: " + ctx.Err().Error(), Remediation: "check connectivity from Central to the provider APIs and retry"})
	}
	for i := range res.Checks {
		res.Checks[i].Message = SafeMessage(in.Provider, in.Credentials, res.Checks[i].Message)
		res.Checks[i].Remediation = SafeMessage(in.Provider, in.Credentials, res.Checks[i].Remediation)
	}
	res.finalize()
	return res, nil
}
