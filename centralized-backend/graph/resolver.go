package graph

import (
	redisclient "github.com/odigos-io/centralized-backend/internal/redis"
)

type Resolver struct {
	RedisClient *redisclient.RedisClient
}
