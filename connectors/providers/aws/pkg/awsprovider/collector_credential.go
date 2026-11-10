package awsprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var _ baseapi.ManualCollectorCredentialIssuer = (*Provider)(nil)

// IssueManualCollectorCredential binds one customer-owned regional Collector to this connector and
// reveals a credential exactly once. The only writes are to connector state; the customer creates and
// fills the Secrets Manager secret named by SecretRef in their own deployment process.
func (p *Provider) IssueManualCollectorCredential(ctx context.Context, input baseapi.ManualCollectorCredentialInput) (baseapi.ManualCollectorCredentialIssue, error) {
	var zero baseapi.ManualCollectorCredentialIssue
	if p.bstore == nil || !p.cfg.PlatformIDFromCR {
		return zero, fmt.Errorf("manual Collector credentials require a running connector with a unique CR identity")
	}
	if p.cfg.Capabilities.CollectorMode(collectorType) != spec.ModeManual {
		return zero, fmt.Errorf("manual Collector credentials require the aws.fargate-collector manual mode")
	}
	if strings.TrimSpace(input.Region) == "" || strings.TrimSpace(input.NetworkID) == "" {
		return zero, fmt.Errorf("region and networkId are required for a manual Collector credential")
	}
	settings := collectorsettings.Read(p.cfgMgr.Effective())
	configured, ok := settings.Regions[input.Region]
	if !settings.Enabled || !ok || !configured.Enabled {
		return zero, fmt.Errorf("region %s has no enabled managed Collector", input.Region)
	}
	if configured.HostNetworkID == "" || configured.HostNetworkID != input.NetworkID {
		return zero, fmt.Errorf("networkId must match the explicit host network configured for the Collector in %s", input.Region)
	}
	secretARN, err := arn.Parse(input.SecretRef)
	if err != nil || secretARN.Service != "secretsmanager" || secretARN.Region != input.Region ||
		secretARN.AccountID != p.cfg.AccountID || !strings.HasPrefix(secretARN.Resource, "secret:") {
		return zero, fmt.Errorf("secretRef must be a Secrets Manager secret ARN in account %s and region %s", p.cfg.AccountID, input.Region)
	}

	p.manualCredentialMu.Lock()
	defer p.manualCredentialMu.Unlock()
	key := basestore.CollectorKey{Region: input.Region}
	rec, err := p.bstore.GetManagedCollector(ctx, key)
	if err != nil && !errors.Is(err, basestore.ErrCollectorNotFound) {
		return zero, fmt.Errorf("read the Collector identity: %w", err)
	}
	if err == nil && rec.NetworkID != "" && rec.NetworkID != input.NetworkID {
		return zero, fmt.Errorf("the Collector in %s is already bound to network %s", input.Region, rec.NetworkID)
	}
	if err == nil && rec.ActiveCredentialID != "" && !input.Rotate {
		return zero, fmt.Errorf("the Collector in %s already has a credential; rotate it rather than issuing a second identity", input.Region)
	}
	if input.Rotate && (err != nil || rec.ActiveCredentialID == "") {
		return zero, fmt.Errorf("the Collector in %s has no active credential to rotate", input.Region)
	}
	if err := p.bstore.UpsertManagedCollector(ctx, key, input.NetworkID, collectorType, spec.ModeManual,
		string(collectorownership.OriginCustomer), nil); err != nil {
		return zero, fmt.Errorf("record the customer-owned Collector: %w", err)
	}
	req := collectorcredential.IssueRequest{
		Binding: collectorcredential.Binding{
			CollectorID: rec.CollectorID, ConnectorUID: p.cfg.PlatformID, Provider: p.cfg.Provider,
			AccountID: p.cfg.AccountID, Region: input.Region, NetworkID: input.NetworkID,
		},
		SecretRef: input.SecretRef, Actor: collectorcredential.ActorCentral,
	}
	var issued collectorcredential.Issued
	if input.Rotate {
		issued, err = p.bstore.ReplaceCollectorCredential(ctx, rec.ActiveCredentialID, req)
	} else {
		issued, err = p.bstore.IssueCollectorCredential(ctx, req)
	}
	if err != nil {
		return zero, fmt.Errorf("issue the manual Collector credential: %w", err)
	}
	if err := p.bstore.RecordCollectorIdentity(ctx, key, issued.Record.Binding.CollectorID,
		input.SecretRef, issued.Record.CredentialID); err != nil {
		return zero, fmt.Errorf("record the issued Collector identity: %w", err)
	}
	// In manual mode the one-time reveal is the handoff to the customer's secret store. Mark it active
	// immediately before returning; the gateway still verifies the immutable full binding on connect.
	if err := p.bstore.MarkCollectorCredentialActive(ctx, issued.Record.Binding.CollectorID,
		issued.Record.CredentialID, "manual-revealed"); err != nil {
		return zero, fmt.Errorf("activate the issued Collector credential: %w", err)
	}
	return baseapi.ManualCollectorCredentialIssue{
		CollectorID: issued.Record.Binding.CollectorID, CredentialID: issued.Record.CredentialID,
		SecretRef: input.SecretRef, AuthorizationHeader: collectorgateway.AuthorizationHeader(issued.Minted.Secret),
	}, nil
}
