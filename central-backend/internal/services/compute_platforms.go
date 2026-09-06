package services

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
)

type proxyMetaReader interface {
	GetProxyMetaKeyValues(ctx context.Context) (redisclient.RedisHashFieldValuesByKey, error)
}

// ComputePlatformService provides helpers around compute platform state derived from proxy connection & metadata stored in Redis.
type ComputePlatformService struct {
	redis proxyMetaReader
}

func NewComputePlatformService(redis *redisclient.RedisClient) *ComputePlatformService {
	return &ComputePlatformService{redis: redis}
}

func (svc *ComputePlatformService) ComputePlatforms(
	ctx context.Context,
	userService *UserService,
	teamService *TeamService,
	wsServer *websocketserver.WebSocketServer,
	cloudConnectorService *CloudConnectorService,
) ([]*model.ComputePlatform, error) {
	proxyMetadata, err := svc.GetAllProxyMetadata(ctx)
	if err != nil {
		logger.Log.Error("Failed to restore proxy metadata from redis", "error", err)
		return nil, err
	}

	cpMap := buildComputePlatformsFromProxyConnections(proxyMetadata, wsServer)

	if err := appendUsers(ctx, cpMap, userService); err != nil {
		return nil, err
	}
	if err := appendTeams(ctx, cpMap, teamService); err != nil {
		return nil, err
	}

	// Cloud connectors are folded into the same list via the "connector" platform
	// type. A listing failure (e.g. missing CRD/RBAC) must not fail the whole
	// connections query, so it is logged and skipped.
	appendCloudConnectors(ctx, cpMap, cloudConnectorService)

	for id, cp := range cpMap {
		meta := proxyMetadata[id]
		if meta != nil {
			applyProxyMetaToComputePlatform(cp, meta)
		}
	}

	return toComputePlatformsSlice(cpMap), nil
}

// appendCloudConnectors lists OdigosCloudConnector CRs and folds them into the
// compute platforms map as "connector"-typed entries.
func appendCloudConnectors(ctx context.Context, cpMap map[string]*model.ComputePlatform, cloudConnectorService *CloudConnectorService) {
	if cloudConnectorService == nil {
		return
	}
	connectors, err := cloudConnectorService.ListConnectors(ctx)
	if err != nil {
		logger.Log.Warn("Failed to list cloud connectors", "error", err)
		return
	}
	for i := range connectors {
		cc := connectors[i]
		name := cc.Spec.DisplayName
		if name == "" {
			name = cc.Name
		}

		// The connector runtime registers over the reverse WebSocket using its CR
		// UID as the routing proxyID (ODIGOS_CONNECTOR_ID = cr.UID). So when a
		// runtime is connected, buildComputePlatformsFromProxyConnections already
		// produced a routable, connector-typed row keyed by that UID. Enrich it
		// with the CR's display name (the WS only reports the CR name) and keep the
		// routable id + graphqlSchemaVersion + live connection status from the
		// registration. Without a connected runtime, fall back to a non-routable
		// CR row that surfaces the connector's lifecycle phase (pending/connecting).
		routableID := string(cc.UID)
		if existing, ok := cpMap[routableID]; ok {
			existing.Type = string(consts.PlatformTypeConnector)
			existing.Name = name
			continue
		}

		cpMap[routableID] = &model.ComputePlatform{
			ID:            routableID,
			Name:          name,
			OdigosVersion: cloudConnectorVersion(cc),
			Type:          string(consts.PlatformTypeConnector),
			Status:        mapCloudConnectorPhase(cc.Status.Phase),
			Users:         []*model.User{},
			Teams:         []*model.Team{},
		}
	}
}

// cloudConnectorVersion reports the connector runtime version from the CR status.
// A connector has no proxy/agent Odigos version like k8s/vm; its version is the
// runtime reported at registration. Until a runtime registers there is none, so
// we return "-" rather than an alarming "unknown".
func cloudConnectorVersion(cc cloudconnectorsv1.OdigosCloudConnector) string {
	if cc.Status.Runtime != nil && cc.Status.Runtime.Version != "" {
		return cc.Status.Runtime.Version
	}
	return "-"
}

// mapCloudConnectorPhase maps the CRD lifecycle phase to a UI StatusType value.
func mapCloudConnectorPhase(phase cloudconnectorsv1.CloudConnectorPhase) string {
	switch phase {
	case cloudconnectorsv1.CloudConnectorPhaseConnected:
		return string(consts.StatusConnected) // "success"
	case cloudconnectorsv1.CloudConnectorPhaseDegraded:
		return "warning"
	case cloudconnectorsv1.CloudConnectorPhaseError:
		return string(consts.StatusDisconnected) // "error"
	case cloudconnectorsv1.CloudConnectorPhaseDisabled:
		return "disabled"
	case cloudconnectorsv1.CloudConnectorPhasePending, cloudconnectorsv1.CloudConnectorPhaseStarting, cloudconnectorsv1.CloudConnectorPhaseDeleting:
		return "info"
	default:
		return "info"
	}
}

func buildComputePlatformsFromProxyConnections(
	restoredMeta map[string]*redisclient.ProxyMetadata,
	wsServer *websocketserver.WebSocketServer,
) map[string]*model.ComputePlatform {
	cpMap := make(map[string]*model.ComputePlatform, len(restoredMeta))
	for proxyID, meta := range restoredMeta {
		if meta == nil {
			continue
		}

		platformName := consts.UnknownValue
		if name, ok := wsServer.GetPlatformName(proxyID); ok {
			platformName = name
		}

		platformType := consts.PlatformTypeK8s
		if pType, ok := wsServer.GetPlatformType(proxyID); ok {
			platformType = pType
		}

		odigosVersion := consts.UnknownValue
		if version, ok := wsServer.GetOdigosVersion(proxyID); ok {
			odigosVersion = version
		}

		var graphqlSchemaVersion *string
		if v, ok := wsServer.GetGraphQLSchemaVersion(proxyID); ok && v != "" {
			schemaVersion := v
			graphqlSchemaVersion = &schemaVersion
		}

		cpMap[proxyID] = &model.ComputePlatform{
			ID:                   proxyID,
			Name:                 platformName,
			OdigosVersion:        odigosVersion,
			GraphqlSchemaVersion: graphqlSchemaVersion,
			Type:                 string(platformType),
			Status:               string(meta.Status),
			Users:                []*model.User{},
			Teams:                []*model.Team{},
		}
	}
	return cpMap
}

func appendUsers(ctx context.Context, cpMap map[string]*model.ComputePlatform, userService *UserService) error {
	users, err := userService.GetUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		for _, cp := range user.ComputePlatforms {
			existing := ensureComputePlatformExists(cpMap, cp.ID)
			if !slices.Contains(existing.Users, user) {
				existing.Users = append(existing.Users, user)
			}
		}
	}
	return nil
}

func appendTeams(ctx context.Context, cpMap map[string]*model.ComputePlatform, teamService *TeamService) error {
	teams, err := teamService.GetTeams(ctx)
	if err != nil {
		return err
	}
	for _, team := range teams {
		for _, cp := range team.ComputePlatforms {
			existing := ensureComputePlatformExists(cpMap, cp.ID)
			if !slices.Contains(existing.Teams, team) {
				existing.Teams = append(existing.Teams, team)
			}
		}
	}
	return nil
}

func ensureComputePlatformExists(cpMap map[string]*model.ComputePlatform, id string) *model.ComputePlatform {
	if existing, ok := cpMap[id]; ok {
		return existing
	}
	cp := &model.ComputePlatform{
		ID:            id,
		Name:          consts.UnknownValue,
		OdigosVersion: consts.UnknownValue,
		Type:          string(consts.PlatformTypeK8s),
		Status:        string(consts.StatusDisconnected),
		Users:         []*model.User{},
		Teams:         []*model.Team{},
	}
	cpMap[id] = cp
	return cp
}

func toComputePlatformsSlice(cpMap map[string]*model.ComputePlatform) []*model.ComputePlatform {
	cps := make([]*model.ComputePlatform, 0, len(cpMap))
	for _, cp := range cpMap {
		cps = append(cps, cp)
	}
	// Go map iteration order is randomized, so sort to keep the connection list
	// stable across requests. Sort by name (what the user sees) with ID as a
	// stable tie-breaker.
	sort.Slice(cps, func(i, j int) bool {
		if cps[i].Name != cps[j].Name {
			return cps[i].Name < cps[j].Name
		}
		return cps[i].ID < cps[j].ID
	})
	return cps
}

func (svc *ComputePlatformService) GetAllProxyMetadata(ctx context.Context) (map[string]*redisclient.ProxyMetadata, error) {
	raw, err := svc.redis.GetProxyMetaKeyValues(ctx)
	if err != nil {
		return nil, err
	}

	metadataSlice := make(map[string]*redisclient.ProxyMetadata, len(raw))
	for key, value := range raw {
		proxyID := extractProxyIDFromProxyMetaKey(key)
		if proxyID == "" {
			continue
		}

		builtMetadata := &redisclient.ProxyMetadata{
			PlatformName:         value[consts.ProxyMetaFieldPlatformName],
			PlatformType:         consts.PlatformType(value[consts.ProxyMetaFieldPlatformType]),
			OdigosVersion:        value[consts.ProxyMetaFieldOdigosVersion],
			GraphQLSchemaVersion: value[consts.ProxyMetaFieldGraphQLSchemaVersion],
			Status:               consts.ProxyConnectionStatus(value[consts.ProxyMetaFieldStatus]),
		}
		if connectedAtValue := value[consts.ProxyMetaFieldConnectedAt]; connectedAtValue != "" {
			if connectedAt, err := strconv.ParseInt(connectedAtValue, 10, 64); err == nil {
				builtMetadata.ConnectedAt = connectedAt
			}
		}
		if lastSeenAtValue := value[consts.ProxyMetaFieldLastSeenAt]; lastSeenAtValue != "" {
			if lastSeenAt, err := strconv.ParseInt(lastSeenAtValue, 10, 64); err == nil {
				builtMetadata.LastSeenAt = lastSeenAt
			}
		}

		metadataSlice[proxyID] = builtMetadata
	}

	return metadataSlice, nil
}

// applyProxyMetaToComputePlatform mutates the given compute platform with values from proxy metadata.
func applyProxyMetaToComputePlatform(cp *model.ComputePlatform, meta *redisclient.ProxyMetadata) {
	if cp == nil || meta == nil {
		return
	}
	if cp.Name == consts.UnknownValue && meta.PlatformName != "" {
		cp.Name = meta.PlatformName
	}
	if cp.OdigosVersion == consts.UnknownValue && meta.OdigosVersion != "" {
		cp.OdigosVersion = meta.OdigosVersion
	}
	if cp.GraphqlSchemaVersion == nil && meta.GraphQLSchemaVersion != "" {
		schemaVersion := meta.GraphQLSchemaVersion
		cp.GraphqlSchemaVersion = &schemaVersion
	}
	cp.Type = string(meta.PlatformType)
	if meta.ConnectedAt > 0 {
		connectedAt := int(meta.ConnectedAt * 1000)
		cp.ConnectedAt = &connectedAt
	}
	if meta.LastSeenAt > 0 {
		lastSeenAt := int(meta.LastSeenAt * 1000)
		cp.LastSeenAt = &lastSeenAt
	}
}

func extractProxyIDFromProxyMetaKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}
