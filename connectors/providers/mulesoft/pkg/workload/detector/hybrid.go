package detector

import (
	"context"
	"errors"
	"strconv"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// HybridDetector discovers applications deployed through Runtime Manager to customer-managed servers.
//
// The runtime version comes from the server an application runs on, because that is where it is reported; an
// application on a server group or cluster reports no version. Odigos markers are not read here: on hybrid,
// Direct Telemetry Stream is configured per server, so a hybrid application's instrumentation is observed at
// the server level once manual mode covers it.
type HybridDetector struct {
	client *anypoint.Client
	orgID  string
	store  inventory
}

// NewHybridDetector builds the hybrid application detector.
func NewHybridDetector(client *anypoint.Client, orgID string, st inventory) *HybridDetector {
	return &HybridDetector{client: client, orgID: orgID, store: st}
}

// HybridExternalID is the source identity of a hybrid application. Runtime Manager application ids are scoped
// by environment, so the environment id is part of the identity.
func HybridExternalID(envID string, appID int) string { return envID + "/" + strconv.Itoa(appID) }

func (d *HybridDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
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
		apps, err := d.client.ListHybridApplications(ctx, d.orgID, env.ID)
		envRead := true
		if errors.Is(err, anypoint.ErrIncomplete) {
			log.Warn("hybrid application list is incomplete; discovering what was returned without pruning", "environment", env.Name, "error", err)
			envRead, err = false, nil
		}
		if err != nil {
			log.Error("list hybrid applications failed", "environment", env.Name, "error", anypoint.Summarize(err))
			continue
		}
		versions := map[int]string{}
		if servers, err := d.client.ListHybridServers(ctx, d.orgID, env.ID); err != nil {
			log.Warn("list hybrid servers failed; runtime versions unknown", "environment", env.Name, "error", anypoint.Summarize(err))
		} else {
			for _, s := range servers {
				versions[s.ID] = s.MuleVersion
			}
		}
		for _, a := range apps {
			app := store.Application{
				ExternalID:      HybridExternalID(env.ID, a.ID),
				Kind:            store.KindHybrid,
				AppName:         a.Name,
				EnvironmentID:   env.ID,
				EnvironmentName: env.Name,
				TargetID:        strconv.Itoa(a.Target.ID),
				TargetName:      a.Target.Name,
				TargetType:      a.Target.Type,
				RuntimeVersion:  versions[a.Target.ID],
				Status:          a.LastReportedStatus,
				Metadata:        map[string]any{"desiredStatus": a.DesiredStatus},
			}
			if err := d.store.UpsertApplication(ctx, app); err != nil {
				log.Error("upsert hybrid application failed", "app", a.Name, "environment", env.Name, "error", err)
				envRead = false
				continue
			}
			count++
		}
		read[env.ID] = envRead
		if envRead {
			_ = d.store.UpdateDiscoveryState(ctx, env.ID, store.KindHybrid, start)
		}
	}
	scope := pruneScope(envs, read, listComplete, clockOK)
	finish(ctx, d.store, store.KindHybrid, scope, start)
	log.Info("hybrid application discovery complete", "count", count, "environmentsRead", len(scope.Complete), "environments", len(envs))
	return baseapi.DetectResult{Authenticated: true}, nil
}
