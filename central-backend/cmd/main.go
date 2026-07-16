package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/odigos-io/central-backend/graph"
	"github.com/odigos-io/central-backend/graph/model"
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

// handlerHolder keeps a consistent concrete type in atomic.Value so we can swap
// from the bootstrap ServeMux to the Gin engine without panicking
// ("store of inconsistently typed value into Value").
type handlerHolder struct {
	h http.Handler
}

// atomicHandler allows swapping the active HTTP handler after bootstrap completes
// without closing the listening socket (so probes keep working across the swap).
type atomicHandler struct {
	h atomic.Value // *handlerHolder
}

func (a *atomicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.h.Load().(*handlerHolder).h.ServeHTTP(w, r)
}

func (a *atomicHandler) store(handler http.Handler) {
	a.h.Store(&handlerHolder{h: handler})
}

// newBootstrapHandler serves probe endpoints while Keycloak/Redis init is still in progress.
// /healthz is always 200 so kubelet liveness does not kill the process during Keycloak cold start.
// /readyz stays 503 until the full router is installed.
func newBootstrapHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "starting", http.StatusServiceUnavailable)
	})
	return mux
}

func main() {
	logger.InitLogger()
	logger.Log.Info("Starting Central Backend Service...")
	commonlogger.Init(getLogLevel(), "central-backend")
	odigosauth.AssertEnterpriseOdigosToken(commonlogger.LoggerCompat())

	ctx := context.Background()
	shutdownCh := bootstrap.GetshutdownChannel()

	port := config.Config.ServerPort
	rootHandler := &atomicHandler{}
	rootHandler.store(newBootstrapHandler())
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           rootHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Log.Info("Serving bootstrap health endpoints while initializing", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", err)
		}
	}()

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
	// The HTTP server above keeps /healthz alive so liveness probes do not restart us
	// while Keycloak is still cold-starting.
	_, err = keycloak.GetKeycloakAdminTokenWithRetry(ctx, shutdownCh, adminCredentials.Username, adminCredentials.Password)
	if err != nil {
		logger.Log.Info("Shutting down before Keycloak is ready", "error", err)
		return
	}

	if err := ensureKeycloakState(adminCredentials.Username, adminCredentials.Password); err != nil {
		logger.Fatal("Failed to initialize Keycloak state", err)
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

	go watchKeycloakHealth(ctx, shutdownCh, adminCredentials.Username, adminCredentials.Password)

	go handleShutdown(shutdownCh, redisClient, snapshotPool, expirationChan)

	wsServer := websocketserver.NewWebSocketServer(redisClient, centralSSEHub, snapshotPool)

	// Set up Redis reconnect callback to resync proxy keys after Redis restarts
	redisClient.SetOnReconnect(func(ctx context.Context) {
		wsServer.ResyncAllProxiesToRedis(ctx)
	})

	adminService := services.NewAdminService(adminCredentials)
	userService := services.NewUserService(adminService)
	teamService := services.NewTeamService(adminService)
	roleMappingService := services.NewRoleMappingService(adminService)
	// Best-effort migration of any legacy per-group IdP mappers into the aggregated
	// per-(claimName, role) format. Idempotent; safe to run on every startup.
	go roleMappingService.ReconcileExistingMappers()
	authService := services.NewAuthService(adminService, userService, roleMappingService)
	ConfigService := services.NewConfigService(k8sClient)
	healthService := services.NewHealthService(redisClient, adminService, centralSSEHub)
	computePlatformService := services.NewComputePlatformService(redisClient)
	cloudConnectorService := services.NewCloudConnectorService(k8sClient)

	gqlHandler := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		RedisClient:            redisClient,
		WebSocketServer:        wsServer,
		AdminService:           adminService,
		AuthService:            authService,
		UserService:            userService,
		TeamService:            teamService,
		RoleMappingService:     roleMappingService,
		HealthService:          healthService,
		ComputePlatformService: computePlatformService,
		ConfigService:          ConfigService,
		CloudConnectorService:  cloudConnectorService,
	}}))

	router := newRouter(gqlHandler, wsServer, centralSSEHub)

	// Initialize Prometheus metrics server (runs in background goroutine)
	metrics.InitMetrics()

	// Swap in the full application router; /readyz becomes 200 after this point.
	rootHandler.store(router)
	logger.Log.Info(fmt.Sprintf("Centralized Backend Service ready on port %s", port))

	// ListenAndServe is already running; handleShutdown exits the process on signal/token expiry.
	select {}
}

func customLogger() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/healthz", "/readyz", "/graphql"},
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
	router.GET("/readyz", func(c *gin.Context) { c.Status(200) })
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

func ensureKeycloakState(username, password string) error {
	kc := keycloak.NewKeycloakAdminService()
	adminToken, err := kc.GetAdminToken(username, password)
	if err != nil {
		return fmt.Errorf("failed to get admin token: %w", err)
	}
	if err := keycloak.CreateOdigosUIClient(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm); err != nil {
		return fmt.Errorf("failed to ensure client: %w", err)
	}
	if err := keycloak.EnsureRequiredRoles(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm); err != nil {
		return fmt.Errorf("failed to ensure roles: %w", err)
	}
	if err := keycloak.EnsureAccessTokenLifespan(adminToken, consts.GetKeycloakHost(), consts.KeycloakRealm, consts.GetAccessTokenLifespanSeconds()); err != nil {
		return fmt.Errorf("failed to ensure token lifespan: %w", err)
	}
	return nil
}

// watchKeycloakHealth monitors Keycloak and re-runs setup when it recovers after being down.
// This handles the case where Keycloak restarts and loses its ephemeral H2 state.
func watchKeycloakHealth(ctx context.Context, shutdownCh <-chan struct{}, username, password string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	kc := keycloak.NewKeycloakAdminService()
	wasDown := false
	for {
		select {
		case <-ticker.C:
			status := kc.HealthCheck(ctx)
			if status.Status != model.HealthStatusHealthy {
				if !wasDown {
					logger.Log.Warn("Keycloak became unreachable")
				}
				wasDown = true
				continue
			}
			if wasDown {
				logger.Log.Info("Keycloak recovered, re-running setup")
				if err := ensureKeycloakState(username, password); err != nil {
					logger.Log.Error("Failed to re-initialize Keycloak state after recovery", "error", err)
				} else {
					logger.Log.Info("Keycloak state re-initialized successfully")
				}
				wasDown = false
			}
		case <-shutdownCh:
			return
		}
	}
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
