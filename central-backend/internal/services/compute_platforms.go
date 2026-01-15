package services

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
)

// ComputePlatformService provides helpers around compute platform state derived from proxy connection & metadata stored in Redis.
type ComputePlatformService struct {
	redis *redisclient.RedisClient
}

func NewComputePlatformService(redis *redisclient.RedisClient) *ComputePlatformService {
	return &ComputePlatformService{redis: redis}
}

func (svc *ComputePlatformService) ComputePlatforms(
	ctx context.Context,
	userService *UserService,
	teamService *TeamService,
	wsServer *websocketserver.WebSocketServer,
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

	for id, cp := range cpMap {
		meta := proxyMetadata[id]
		if meta != nil {
			applyProxyMetaToComputePlatform(cp, meta)
		}
	}

	return toComputePlatformsSlice(cpMap), nil
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

		platformType := model.PlatformTypeK8s
		if pType, ok := wsServer.GetPlatformType(proxyID); ok {
			if mapped, ok := toModelPlatformType(string(pType)); ok {
				platformType = mapped
			} else {
				logger.Log.Warn("Unknown platform type, defaulting to k8s", "proxyID", proxyID, "platformType", pType)
				platformType = model.PlatformTypeK8s
			}
		}

		odigosVersion := consts.UnknownValue
		if version, ok := wsServer.GetOdigosVersion(proxyID); ok {
			odigosVersion = version
		}

		cpMap[proxyID] = &model.ComputePlatform{
			ID:            proxyID,
			Name:          platformName,
			OdigosVersion: odigosVersion,
			Type:          platformType,
			Status:        string(meta.Status),
			Users:         []*model.User{},
			Teams:         []*model.Team{},
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
		Type:          model.PlatformTypeK8s,
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
			PlatformName:  value[consts.ProxyMetaFieldPlatformName],
			PlatformType:  value[consts.ProxyMetaFieldPlatformType],
			OdigosVersion: value[consts.ProxyMetaFieldOdigosVersion],
			Status:        consts.ProxyConnectionStatus(value[consts.ProxyMetaFieldStatus]),
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
	if mapped, ok := toModelPlatformType(string(meta.PlatformType)); ok {
		cp.Type = mapped
	}
	if meta.ConnectedAt > 0 {
		connectedAt := int(meta.ConnectedAt * 1000)
		cp.ConnectedAt = &connectedAt
	}
	if meta.LastSeenAt > 0 {
		lastSeenAt := int(meta.LastSeenAt * 1000)
		cp.LastSeenAt = &lastSeenAt
	}
}

func toModelPlatformType(pType string) (model.PlatformType, bool) {
	switch pType {
	case string(consts.PlatformTypeK8s):
		return model.PlatformTypeK8s, true
	case string(consts.PlatformTypeVM):
		return model.PlatformTypeVM, true
	default:
		return model.PlatformTypeK8s, false
	}
}

func extractProxyIDFromProxyMetaKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}
