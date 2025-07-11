package graph

import (
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/services"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
)

type Resolver struct {
	RedisClient     *redisclient.RedisClient
	WebSocketServer *websocketserver.WebSocketServer
	AuthService     *services.AuthService
	UserService     *services.UserService
	TeamService     *services.TeamService
}
