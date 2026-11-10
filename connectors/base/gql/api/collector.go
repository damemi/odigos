package gqlapi

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectormetrics"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var collectorAPILog = connlog.Subsystem("gql-managed-collector")

// ManagedCollectorAPI maps the GraphQL managed-Collector operations to the coordinator.
//
// Every operation is addressed by REGION, which is a Collector's identity. Create and update return the
// report rather than a boolean because neither builds infrastructure synchronously: they write
// configuration and the reconcile loop converges it, so what the caller gets back is the Collector as it
// stands right now — normally Pending — and the thing to watch is its status.
type ManagedCollectorAPI struct {
	Central centralapi.CentralInterface
}

func (a *ManagedCollectorAPI) ManagedCollectorReports(ctx context.Context) ([]*model.ManagedCollectorReport, error) {
	collectorAPILog.Debug("list managed collectors")
	inputs, err := a.Central.ManagedCollectorReports(ctx)
	if err != nil {
		collectorAPILog.Error("list managed collectors failed", "error", err)
		return nil, fmt.Errorf("managedCollectorReports: %w", err)
	}
	return collectorreport.ToModels(inputs), nil
}

func (a *ManagedCollectorAPI) ManagedCollectorReport(ctx context.Context, region string) (*model.ManagedCollectorReport, error) {
	collectorAPILog.Debug("get managed collector", "region", region)
	in, err := a.Central.ManagedCollectorReport(ctx, region)
	if err != nil {
		collectorAPILog.Error("get managed collector failed", "region", region, "error", err)
		return nil, fmt.Errorf("managedCollectorReport: %w", err)
	}
	if in == nil {
		return nil, nil
	}
	return collectorreport.ToModel(*in), nil
}

func (a *ManagedCollectorAPI) CreateManagedCollector(ctx context.Context, input model.CreateManagedCollectorInput) (*model.ManagedCollectorReport, error) {
	collectorAPILog.Info("create managed collector", "region", input.Region)
	if err := a.Central.CreateManagedCollector(ctx, input.Region, patchFromCreateInput(input)); err != nil {
		collectorAPILog.Error("create managed collector failed", "region", input.Region, "error", err)
		return nil, TypedManagedCollectorError(err)
	}
	return a.reportAfterWrite(ctx, input.Region, "createManagedCollector")
}

func (a *ManagedCollectorAPI) UpdateManagedCollector(ctx context.Context, input model.UpdateManagedCollectorInput) (*model.ManagedCollectorReport, error) {
	collectorAPILog.Info("update managed collector", "region", input.Region)
	if err := a.Central.UpdateManagedCollector(ctx, input.Region, patchFromUpdateInput(input)); err != nil {
		collectorAPILog.Error("update managed collector failed", "region", input.Region, "error", err)
		return nil, TypedManagedCollectorError(err)
	}
	return a.reportAfterWrite(ctx, input.Region, "updateManagedCollector")
}

// reportAfterWrite reads back the report a lifecycle mutation returns.
//
// A region with no row yet is the NORMAL outcome of a create: the row appears when the reconcile loop
// first converges the Collector, which is seconds away and not on this call's path. Rather than return
// null — which the schema's non-null result forbids, and which would read as "nothing was created" — it
// renders the configuration that was just written, with the Pending status that honestly describes a
// Collector nothing has built yet.
func (a *ManagedCollectorAPI) reportAfterWrite(ctx context.Context, region, op string) (*model.ManagedCollectorReport, error) {
	in, err := a.Central.ManagedCollectorReport(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("%s: reading the Collector back: %w", op, err)
	}
	if in != nil {
		return collectorreport.ToModel(*in), nil
	}

	return a.pendingReport(region), nil
}

// pendingReport renders a Collector that is configured but not yet built.
func (a *ManagedCollectorAPI) pendingReport(region string) *model.ManagedCollectorReport {
	now := time.Now()
	desired := a.Central.CollectorSettings().Region(region)
	return collectorreport.ToModel(collectorreport.ModelInput{
		Region:        region,
		HostNetworkID: desired.HostNetworkID,
		Desired:       desired,
		// The configuration was written just now, and nothing has observed it yet — which is exactly
		// what an empty status renders as once the report's state machine defaults it to Pending.
		UpdatedAt: now,
		Now:       now,
	})
}

func (a *ManagedCollectorAPI) TeardownManagedCollector(ctx context.Context, input model.TeardownManagedCollectorInput) (*model.TeardownManagedCollectorPayload, error) {
	collectorAPILog.Info("teardown managed collector", "region", input.Region)
	result, err := a.Central.TeardownManagedCollector(ctx, input.Region)
	if err != nil {
		collectorAPILog.Error("teardown managed collector failed", "region", input.Region, "error", err)
		return nil, fmt.Errorf("teardownManagedCollector: %w", err)
	}
	out := &model.TeardownManagedCollectorPayload{
		Region:   input.Region,
		Deleted:  result.Deleted,
		Retained: result.Retained,
	}
	if result.Message != "" {
		out.Message = &result.Message
	}
	if out.Retained == nil {
		out.Retained = []string{}
	}
	return out, nil
}

func (a *ManagedCollectorAPI) ManagedCollectorMetrics(ctx context.Context, region string, refresh *bool) (*model.ManagedCollectorMetrics, error) {
	collectorAPILog.Debug("get managed collector metrics", "region", region)
	rec, found, err := a.Central.ManagedCollectorMetrics(ctx, region, refresh != nil && *refresh)
	if err != nil {
		collectorAPILog.Error("get managed collector metrics failed", "region", region, "error", err)
		return nil, fmt.Errorf("managedCollectorMetrics: %w", err)
	}
	if !found {
		// Null rather than a snapshot of zeroes: this Collector has reported nothing, and zeroes would
		// claim it has handled no telemetry — a statement about a running Collector, not about one
		// nobody has heard from.
		return nil, nil
	}
	return collectormetrics.ToModel(region, rec), nil
}

// patchFromCreateInput and patchFromUpdateInput are the same mapping, over two generated input types
// that differ only in which fields are optional. Duplicated rather than unified behind reflection or a
// shared interface: the generated types are regenerated from the schema, and a mapping that spells each
// field out fails to compile when one is renamed, where a reflective one would silently drop it.

func patchFromCreateInput(in model.CreateManagedCollectorInput) collectorsettings.Patch {
	provider, providerIngress := providerOverridesPatch(in.ProviderOverrides)
	return collectorsettings.Patch{
		HostNetworkID:           in.NetworkID,
		Provider:                provider,
		CPU:                     in.CPU,
		Memory:                  in.Memory,
		LogRetentionDays:        in.LogRetentionDays,
		Arch:                    in.Arch,
		ImageRepositoryOverride: in.ImageRepositoryOverride,
		EndpointOverride:        in.EndpointOverride,
		RolloutBatchSize:        in.RolloutBatchSize,
		Scaling:                 scalingPatch(in.Scaling),
		Retention:               retentionPatch(in.Retention),
		TLS:                     transportPatch(in.Transport),
		Ingress:                 mergeIngressPatch(ingressPatch(in.Ingress), providerIngress),
	}
}

func patchFromUpdateInput(in model.UpdateManagedCollectorInput) collectorsettings.Patch {
	provider, providerIngress := providerOverridesPatch(in.ProviderOverrides)
	return collectorsettings.Patch{
		Enabled:                 in.Enabled,
		HostNetworkID:           in.NetworkID,
		Provider:                provider,
		CPU:                     in.CPU,
		Memory:                  in.Memory,
		LogRetentionDays:        in.LogRetentionDays,
		Arch:                    in.Arch,
		ImageRepositoryOverride: in.ImageRepositoryOverride,
		EndpointOverride:        in.EndpointOverride,
		RolloutBatchSize:        in.RolloutBatchSize,
		Scaling:                 scalingPatch(in.Scaling),
		Retention:               retentionPatch(in.Retention),
		TLS:                     transportPatch(in.Transport),
		Ingress:                 mergeIngressPatch(ingressPatch(in.Ingress), providerIngress),
	}
}

func scalingPatch(in *model.ManagedCollectorScalingInput) *collectorsettings.ScalingPatch {
	if in == nil {
		return nil
	}
	return &collectorsettings.ScalingPatch{
		Mode:                in.Mode,
		DesiredCount:        in.DesiredCount,
		MinCount:            in.MinCount,
		MaxCount:            in.MaxCount,
		TargetCPUPercent:    in.TargetCPUPercent,
		TargetMemoryPercent: in.TargetMemoryPercent,
	}
}

func retentionPatch(in *model.ManagedCollectorRetentionInput) *collectorsettings.RetentionPatch {
	if in == nil {
		return nil
	}
	return &collectorsettings.RetentionPatch{
		IdleScaleToZeroAfterHours: in.IdleScaleToZeroAfterHours,
		IdleDeleteAfterHours:      in.IdleDeleteAfterHours,
	}
}

func transportPatch(in *model.ManagedCollectorTransportInput) *collectorsettings.TransportPatch {
	if in == nil {
		return nil
	}
	return &collectorsettings.TransportPatch{
		Mode:           in.Mode,
		Hostname:       in.Hostname,
		CertificateRef: in.CertificateRef,
	}
}

func ingressPatch(in *model.ManagedCollectorIngressInput) *collectorsettings.IngressPatch {
	if in == nil {
		return nil
	}
	return &collectorsettings.IngressPatch{
		AdditionalCIDRs: slicePtr(in.AdditionalCidrs),
	}
}

// providerOverridesPatch maps the generated provider-specific API branch to the opaque settings map.
// The settings domain only merges this map; the selected provider interprets it.
func providerOverridesPatch(in *model.ManagedCollectorProviderOverridesInput) (map[string]any, *collectorsettings.IngressPatch) {
	if in == nil || in.AWS == nil {
		return nil, nil
	}
	aws := in.AWS
	provider := map[string]any{}
	setProviderStrings(provider, "subnetIds", aws.SubnetIds)
	setProviderStrings(provider, "securityGroupIds", aws.SecurityGroupIds)
	setProvider(provider, "clusterArn", aws.ClusterRef)
	setProvider(provider, "executionRoleArn", aws.ExecutionRoleArn)
	setProvider(provider, "assignPublicIp", aws.AssignPublicIP)
	setProvider(provider, "useFargateSpot", aws.UseFargateSpot)
	ingress := &collectorsettings.IngressPatch{
		AdditionalNetworkIdentityIDs: slicePtr(aws.AdditionalSecurityGroupIds),
		AllowNetworkCIDRFallback:     aws.AllowNetworkCidrFallback,
		MinFallbackPrefixLength:      aws.MinFallbackPrefixLength,
	}
	return provider, ingress
}

func mergeIngressPatch(base, overrides *collectorsettings.IngressPatch) *collectorsettings.IngressPatch {
	if base == nil {
		return overrides
	}
	if overrides == nil {
		return base
	}
	if overrides.AdditionalNetworkIdentityIDs != nil {
		base.AdditionalNetworkIdentityIDs = overrides.AdditionalNetworkIdentityIDs
	}
	if overrides.AllowNetworkCIDRFallback != nil {
		base.AllowNetworkCIDRFallback = overrides.AllowNetworkCIDRFallback
	}
	if overrides.MinFallbackPrefixLength != nil {
		base.MinFallbackPrefixLength = overrides.MinFallbackPrefixLength
	}
	return base
}

func setProvider[T any](provider map[string]any, key string, value *T) {
	if value != nil {
		provider[key] = *value
	}
}

func setProviderStrings(provider map[string]any, key string, values []string) {
	if values == nil {
		return
	}
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	provider[key] = items
}

// slicePtr distinguishes "field omitted" from "field set to the empty list".
//
// gqlgen decodes an omitted list to nil and an explicit [] to a non-nil
// zero-length slice. Preserve that distinction so [] clears a pinned list.
func slicePtr(v []string) *[]string {
	if v == nil {
		return nil
	}
	return &v
}
