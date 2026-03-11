package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/odigos-io/central-backend/graph"
	"github.com/odigos-io/central-backend/internal/bootstrap"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/k8s"
	"github.com/odigos-io/central-backend/internal/keycloak"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/metrics"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/services"
	"github.com/odigos-io/central-backend/internal/snapshotworker"
	"github.com/odigos-io/central-backend/internal/ssehub"
	"github.com/odigos-io/central-backend/internal/utils"
	websocketserver "github.com/odigos-io/central-backend/internal/websocket"
	"github.com/odigos-io/odigos-enterprise/odigosauth"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

func getLogLevel() string {
	if lvl := os.Getenv("ODIGOS_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "info"
}

func main() {
	logger.InitLogger()
	logger.Log.Info("Starting Central Backend Service...")
	commonlogger.Init(getLogLevel(), "central-backend")
	odigosauth.AssertEnterpriseOdigosToken(commonlogger.LoggerCompat())

	ctx := context.Background()
	shutdownCh := bootstrap.GetshutdownChannel()

	k8sClient, err := k8s.CreateClient()
	if err != nil {
		logger.Log.Error("Failed to create k8s client", "error", err)
		k8sClient = nil
	}

	credentialsService, err := services.NewCredentialsService(k8sClient)
	if err != nil {
		logger.Fatal("Failed to initialize credentials service", err)
	}
	adminCredentials, err := credentialsService.GetAdminCredentials(ctx)
	if err != nil {
		logger.Fatal("Failed to get admin credentials", err)
	}

	// Block startup until Keycloak is reachable (infinite retries).
	adminToken, err := keycloak.GetKeycloakAdminTokenWithRetry(ctx, shutdownCh, adminCredentials.Username, adminCredentials.Password)
	if err != nil {
		logger.Log.Info("Shutting down before Keycloak is ready", "error", err)
		return
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

	centralSSEHub := ssehub.NewWithRedis(redisClient, config.Config.InstanceID)
	centralSSEHub.StartRedisSSEBroadcast(ctx)

	// Create and start snapshot worker pool for multi-cluster state aggregation
	snapshotPool := snapshotworker.NewWorkerPool(redisClient, 10, 500) // 10 workers, queue size 500
	snapshotPool.Start()

	go handleShutdown(shutdownCh, redisClient, snapshotPool, expirationChan)

	wsServer := websocketserver.NewWebSocketServer(redisClient, centralSSEHub, snapshotPool)

	// Set up Redis reconnect callback to resync proxy keys after Redis restarts
	redisClient.SetOnReconnect(func(ctx context.Context) {
		wsServer.ResyncAllProxiesToRedis(ctx)
	})

	adminService := services.NewAdminService(adminCredentials)
	userService := services.NewUserService(adminService)
	teamService := services.NewTeamService(adminService)
	authService := services.NewAuthService(adminService, userService)
	ConfigService := services.NewConfigService(k8sClient)
	healthService := services.NewHealthService(redisClient, adminService, centralSSEHub)
	computePlatformService := services.NewComputePlatformService(redisClient)

	gqlHandler := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		RedisClient:            redisClient,
		WebSocketServer:        wsServer,
		AdminService:           adminService,
		AuthService:            authService,
		UserService:            userService,
		TeamService:            teamService,
		HealthService:          healthService,
		ComputePlatformService: computePlatformService,
		ConfigService:          ConfigService,
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

	// Reverse proxy to Keycloak for browser-facing auth endpoints.
	// This keeps Keycloak fully internal — the browser only talks to the central-backend.
	kcProxy := newKeycloakReverseProxy(consts.GetKeycloakHost())
	router.Any("/realms/*path", gin.WrapH(kcProxy))
	router.Any("/resources/*path", gin.WrapH(kcProxy))

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

func handleShutdown(shutdownCh <-chan struct{}, redisClient *redisclient.RedisClient, snapshotPool *snapshotworker.WorkerPool, tokenExpirationChan <-chan time.Time) {

	select {
	case <-tokenExpirationChan:
		logger.Log.Info("token has expired, shutting down server. Please contact Odigos support to renew your token.")
	case <-shutdownCh:
		logger.Log.Info("got shutdown signal, shutting down server...")
	}

	// Stop snapshot worker pool
	if snapshotPool != nil {
		snapshotPool.Stop()
	}

	err := redisClient.Close()
	if err != nil {
		logger.Log.Error("Failed to close Redis connection", "error", err)
	}
	logger.Log.Info("Redis connection closed")

	os.Exit(0)
}

// newKeycloakReverseProxy creates a reverse proxy that forwards requests to the internal Keycloak service.
// This allows the browser to interact with Keycloak through the central-backend without exposing Keycloak externally.
func newKeycloakReverseProxy(keycloakHost string) http.Handler {
	target, err := url.Parse(keycloakHost)
	if err != nil {
		logger.Fatal("Failed to parse Keycloak host URL", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Wrap the default Director to add X-Forwarded-* headers.
	// With KC_PROXY=edge, Keycloak trusts these to build browser-facing URLs
	// (e.g. the /broker/{alias}/login redirect during OIDC flows).
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		proto := req.Header.Get("X-Forwarded-Proto")
		if proto == "" {
			proto = "http"
		}
		req.Header.Set("X-Forwarded-Proto", proto)
		req.Header.Set("X-Forwarded-Host", req.Host)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		// Rewrite Location headers that Keycloak may generate with its internal host,
		// converting them to relative paths so the browser stays on the same origin.
		location := resp.Header.Get("Location")
		if location != "" {
			location = strings.Replace(location, keycloakHost, "", 1)
			resp.Header.Set("Location", location)
		}
		return nil
	}

	return proxy
}
