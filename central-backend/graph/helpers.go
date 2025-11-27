package graph

import (
	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

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
	switch meta.PlatformType {
	case string(consts.PlatformTypeVM):
		cp.Type = model.PlatformTypeVM
	case string(consts.PlatformTypeK8s):
		cp.Type = model.PlatformTypeK8s
	}
	if meta.ConnectedAt > 0 {
		iv := int(meta.ConnectedAt * 1000)
		cp.ConnectedAt = &iv
	}
	if meta.LastSeenAt > 0 {
		iv := int(meta.LastSeenAt * 1000)
		cp.LastSeenAt = &iv
	}
}


