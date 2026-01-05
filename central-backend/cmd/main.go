package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/odigos-io/central-backend/graph"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/metrics"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/services"
	"github.com/odigos-io/central-backend/internal/ssehub"
	"github.com/odigos-io/central-backend/internal/utils"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
	"github.com/odigos-io/odigos-enterprise/odigosauth"
)

func main() {
	logger.InitLogger()
	logger.Log.Info("Starting Central Backend Service...")
	odigosauth.AssertEnterpriseOdigosToken(logr.FromSlogHandler(logger.Log.Handler()))

	ctx := context.Background()

	credentialsService, err := services.NewCredentialsService()
	if err != nil {
		logger.Fatal("Failed to initialize credentials service", err)
	}
	adminCredentials, err := credentialsService.GetAdminCredentials(ctx)
	if err != nil {
		logger.Fatal("Failed to get admin credentials", err)
	}

	adminToken, err := keycloak.NewKeycloakAdminService().GetAdminToken(adminCredentials.Username, adminCredentials.Password)
	if err != nil {
		logger.Fatal("Failed to get admin token", err)
	}

	err = keycloak.CreateOdigosUIClient(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm)
	if err != nil {
		logger.Fatal("Failed to create odigos-ui client", err)
	}

	err = keycloak.EnsureRequiredRoles(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm)
	if err != nil {
		logger.Fatal("Failed to ensure required roles", err)
	}

	if err := keycloak.EnsureAccessTokenLifespan(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, consts.GetAccessTokenLifespanSeconds()); err != nil {
		logger.Log.Error("Failed to ensure access token lifespan", "error", err)
	}

	redisClient, err := redisclient.NewRedisClient(ctx)
	if err != nil {
		logger.Fatal("Failed to connect to Redis", err)
	}

	expirationChan, err := odigosauth.TokenExpirationChannel()
	if err != nil {
		logger.Fatal("Failed to get token expiration channel", err)
	}

	go handleShutdown(ctx, redisClient, expirationChan)

	centralSSEHub := ssehub.NewWithRedis(redisClient, config.Config.CentralizedBackendID)
	centralSSEHub.StartRedisSSEBroadcast(ctx)
	wsServer := websocketserver.NewWebSocketServer(redisClient, centralSSEHub)

	// Set up Redis reconnect callback to resync proxy keys after Redis restarts
	redisClient.SetOnReconnect(func(ctx context.Context) {
		wsServer.ResyncAllProxiesToRedis(ctx)
	})

	adminService := services.NewAdminService(adminCredentials)
	userService := services.NewUserService(adminService)
	teamService := services.NewTeamService(adminService)
	authService := services.NewAuthService(adminService, userService)
	healthService := services.NewHealthService(redisClient, adminService, centralSSEHub)

	gqlHandler := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		RedisClient:     redisClient,
		WebSocketServer: wsServer,
		AdminService:    adminService,
		AuthService:     authService,
		UserService:     userService,
		TeamService:     teamService,
		HealthService:   healthService,
	}}))

	router := newRouter(gqlHandler, wsServer, centralSSEHub)

	// Initialize Prometheus metrics server (runs in background goroutine)
	metrics.InitMetrics()

	port := config.Config.ServerPort

	logger.Log.Info(fmt.Sprintf("Centralized Backend Service started on port %s", port))
	err = router.Run(":" + port)
	if err != nil {
		logger.Fatal("Failed to start server", err)
	}

}

func customLogger() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/healthz", "/graphql"},
	})
}

func newRouter(gqlHandler *handler.Server, wsServer *websocketserver.WebSocketServer, sseHub *ssehub.Hub) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(customLogger())
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	router.GET("/healthz", func(c *gin.Context) { c.Status(200) })
	router.GET("/ws/proxy", func(c *gin.Context) { wsServer.HandleConnection(c.Writer, c.Request) })
	router.GET("/sse/events", func(c *gin.Context) { sseHub.SSEHandler(c.Writer, c.Request) })
	router.POST("/graphql", authContextMiddleware(), func(c *gin.Context) { gqlHandler.ServeHTTP(c.Writer, c.Request) })

	return router
}

func authContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			ctx := utils.SetTokenInContext(c.Request.Context(), token)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}

func handleShutdown(ctx context.Context, redisClient *redisclient.RedisClient, tokenExpirationChan <-chan time.Time) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-tokenExpirationChan:
		logger.Log.Info("token has expired, shutting down server. Please contact Odigos support to renew your token.")
	case <-sigChan:
		logger.Log.Info("got shutdown signal, shutting down server...")
	}

	// Clean up proxy keys for this backend instance
	currentBackendID := config.Config.CentralizedBackendID
	if err := redisClient.DeleteProxyKeysForBackend(ctx, currentBackendID); err != nil {
		logger.Log.Error("Failed to delete proxy keys for backend", "backendID", currentBackendID, "error", err)
	}

	err := redisClient.Close()
	if err != nil {
		logger.Log.Error("Failed to close Redis connection", "error", err)
	}

	logger.Log.Info("Redis connection closed")

	os.Exit(0)
}
