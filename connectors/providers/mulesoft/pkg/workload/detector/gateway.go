package detector

import (
	"context"
	"errors"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// GatewayDetector discovers the Omni Gateways (formerly Flex Gateways) Gateway Manager knows in every visible
// environment, managed and self-managed.
//
// A managed gateway's configuration is read in full, because its tracing labels are its Odigos markers. A
// self-managed gateway's configuration lives on the customer's host, so it is discovered with no marker.
type GatewayDetector struct {
	client *anypoint.Client
	orgID  string
	store  inventory
}

// NewGatewayDetector builds the Omni Gateway detector.
func NewGatewayDetector(client *anypoint.Client, orgID string, st inventory) *GatewayDetector {
	return &GatewayDetector{client: client, orgID: orgID, store: st}
}

func (d *GatewayDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	start, clockOK := scanStart(ctx, d.store)
	envs, err := d.client.Environments(ctx, d.orgID)
	listComplete := true
	if errors.Is(err, anypoint.ErrIncomplete) {
		log.Warn("environment list is incomplete; discovering what was returned without pruning missing environments", "error", err)
		listComplete, err = false, nil
	}
	if err != nil {
		log.Error("list environments failed", "error", anypoint.Summarize(err))
		return baseapi.DetectResult{Authenticated: !anypoint.IsAuthErr(err)}, err
	}

	count := 0
	read := map[string]bool{}
	for _, env := range envs {
		gws, err := d.client.ListGateways(ctx, d.orgID, env.ID)
		envRead := true
		if errors.Is(err, anypoint.ErrIncomplete) {
			log.Warn("gateway list is incomplete; discovering what was returned without pruning", "environment", env.Name, "error", err)
			envRead, err = false, nil
		}
		if err != nil {
			log.Error("list Omni Gateways failed", "environment", env.Name, "error", anypoint.Summarize(err))
			continue
		}
		for _, g := range gws {
			app := gatewayApplication(env, g)
			if g.Managed() {
				detail, err := d.client.GetGateway(ctx, d.orgID, env.ID, g.ID)
				if err != nil {
					// Without the configuration the marker is unknown; writing the row would record it as
					// uninstrumented, so this one keeps its last state and the environment is not pruned.
					log.Error("get Omni Gateway failed", "gateway", g.Name, "environment", env.Name, "error", anypoint.Summarize(err))
					envRead = false
					continue
				}
				app.Origin = store.GatewayOrigin(detail.Tracing())
				// The listing leaves out what only the detail carries — the runtime version, on a live
				// organization — so the detail fills any gap and is fresher where both have a value.
				s := detail.Summary()
				if s.Status != "" {
					app.Status = s.Status
				}
				if s.RuntimeVersion != "" {
					app.RuntimeVersion = s.RuntimeVersion
				}
				if app.TargetName == "" {
					app.TargetName = s.TargetName
				}
			}
			if err := d.store.UpsertApplication(ctx, app); err != nil {
				log.Error("upsert Omni Gateway failed", "gateway", g.Name, "environment", env.Name, "error", err)
				envRead = false
				continue
			}
			count++
		}
		read[env.ID] = envRead
		if envRead {
			_ = d.store.UpdateDiscoveryState(ctx, env.ID, store.KindOmniGateway, start)
		}
	}
	scope := pruneScope(envs, read, listComplete, clockOK)
	finish(ctx, d.store, store.KindOmniGateway, scope, start)
	log.Info("Omni Gateway discovery complete", "count", count, "environmentsRead", len(scope.Complete), "environments", len(envs))
	return baseapi.DetectResult{Authenticated: true}, nil
}

func gatewayApplication(env anypoint.Environment, g anypoint.Gateway) store.Application {
	targetType := store.GatewaySelfManaged
	if g.Managed() {
		targetType = store.GatewayManaged
	}
	meta := map[string]any{"gatewayKind": g.Kind}
	if g.TargetType != "" {
		meta["spaceType"] = g.TargetType
	}
	return store.Application{
		ExternalID:      g.ID,
		Kind:            store.KindOmniGateway,
		AppName:         g.Name,
		EnvironmentID:   env.ID,
		EnvironmentName: env.Name,
		TargetID:        g.TargetID,
		TargetName:      g.TargetName,
		TargetType:      targetType,
		RuntimeVersion:  g.RuntimeVersion,
		Status:          g.Status,
		Metadata:        meta,
	}
}
