package coordinator

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/spec"
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

// RevokeManagedCollectorCredential invalidates one manually issued credential.
// The region lookup fences the operation to this connector's Collector identity.
func (c *Coordinator) RevokeManagedCollectorCredential(ctx context.Context, region, credentialID string) error {
	if c.store == nil {
		return fmt.Errorf("Collector credential store is unavailable")
	}
	rec, err := c.collectorResourceStore().GetManagedCollector(ctx, store.CollectorKey{Region: region})
	if err != nil {
		return err
	}
	if rec.Mode != spec.ModeManual || rec.CollectorID == "" {
		return fmt.Errorf("region %s has no manually managed Collector credential", region)
	}
	if credentialID == "" {
		return fmt.Errorf("credentialId is required")
	}
	if err := c.store.RevokeCollectorCredential(ctx, rec.CollectorID, credentialID, collectorcredential.ReasonRotation, collectorcredential.ActorCentral); err != nil {
		return err
	}
	if rec.ActiveCredentialID == credentialID {
		if _, err := c.store.ClearCollectorCredential(ctx, rec.CollectorID); err != nil {
			return err
		}
	}
	c.TriggerCollectorReconcile()
	return nil
}
