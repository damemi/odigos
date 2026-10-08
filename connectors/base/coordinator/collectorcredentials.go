package coordinator

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// IssueManagedCollectorCredential delegates the one-time manual-mode secret issuance to the provider,
// which knows the account and host-network binding. It never invokes a Collector deployer.
func (c *Coordinator) IssueManagedCollectorCredential(ctx context.Context, input api.ManualCollectorCredentialInput) (api.ManualCollectorCredentialIssue, error) {
	issuer, ok := c.provider.(api.ManualCollectorCredentialIssuer)
	if !ok {
		return api.ManualCollectorCredentialIssue{}, fmt.Errorf("manual Collector credentials are not supported by this provider")
	}
	issued, err := issuer.IssueManualCollectorCredential(ctx, input)
	if err != nil {
		return api.ManualCollectorCredentialIssue{}, err
	}
	c.TriggerCollectorReconcile()
	return issued, nil
}
