package graph

import (
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type Resolver struct {
	RedisClient *redisclient.RedisClient
}
