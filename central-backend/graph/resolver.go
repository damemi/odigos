package graph

import (
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
)

type Resolver struct {
	RedisClient     *redisclient.RedisClient
	WebSocketServer *websocketserver.WebSocketServer
}
