package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/odigos-io/central-backend/graph"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/repositories/keycloak_repositories"
	"github.com/odigos-io/central-backend/internal/services"
	"github.com/odigos-io/central-backend/internal/utils"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
)

func main() {
	logger.InitLogger()
	logger.Log.Info("Starting Central Backend Service...")
	// odigosauth.AssertEnterpriseOdigosToken(logr.FromSlogHandler(logger.Log.Handler()))

	ctx := context.Background()

	credentialsService, err := services.NewCredentialsService()
	if err != nil {
		logger.Fatal("Failed to initialize credentials service", err)
	}

	credentials, err := credentialsService.GetAdminCredentials(ctx)
	if err != nil {
		logger.Fatal("Failed to get admin credentials", err)
	}

	adminToken, err := keycloak.GetKeycloakAccessToken(consts.GetKeycloakHost(), credentials.Username, credentials.Password)
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

	redisClient, err := redisclient.NewRedisClient(ctx)
	if err != nil {
		logger.Fatal("Failed to connect to Redis", err)
	}

	// Ensure system starts in uninitialized state
	err = redisClient.SetSystemInitialized(ctx, false)
	if err != nil {
		logger.Log.Error("Failed to set system initialization status", "error", err)
		// Don't fail startup if this fails, just log it
	}

	go handleShutdown(ctx, redisClient)

	wsServer := websocketserver.NewWebSocketServer(redisClient)

	authRepo := keycloak_repositories.NewAuthRepository(credentialsService)

	adminService := services.NewAdminService(redisClient, authRepo)

	userRepo := keycloak_repositories.NewUserRepository(redisClient, credentialsService, adminService)
	teamRepo := keycloak_repositories.NewTeamRepository(redisClient, credentialsService, adminService)

	authService := services.NewAuthService(redisClient, userRepo, authRepo)
	userService := services.NewUserService(redisClient, userRepo, authRepo)
	teamService := services.NewTeamService(redisClient, teamRepo)

	gqlHandler := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		RedisClient:     redisClient,
		WebSocketServer: wsServer,
		AuthService:     authService,
		UserService:     userService,
		TeamService:     teamService,
	}}))

	router := gin.Default()
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	router.GET("/ws/proxy", func(c *gin.Context) {
		wsServer.HandleConnection(c.Writer, c.Request)
	})
	router.POST("/graphql", func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			ctx := utils.SetTokenInContext(c.Request.Context(), token)
			c.Request = c.Request.WithContext(ctx)
		}

		gqlHandler.ServeHTTP(c.Writer, c.Request)
	})

	port := config.Config.ServerPort

	logger.Log.Info(fmt.Sprintf("Centralized Backend Service started on port %s", port))
	err = router.Run(":" + port)
	if err != nil {
		logger.Fatal("Failed to start server", err)
	}

}

// remove all active proxy connections from Redis and close the Redis connection
func handleShutdown(ctx context.Context, redisClient *redisclient.RedisClient) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	logger.Log.Info("Shutting down server...")

	if err := redisClient.MarkAllConnectionsAsDisconnected(ctx); err != nil {
		logger.Log.Error("Failed to mark all connections as disconnected", "error", err)
	}

	err := redisClient.Close()
	if err != nil {
		logger.Log.Error("Failed to close Redis connection", "error", err)
	}

	logger.Log.Info("Redis connection closed")

	os.Exit(0)
}
