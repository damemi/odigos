package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/99designs/gqlgen/graphql/handler"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/odigos-io/centralized-backend/graph"
	"github.com/odigos-io/centralized-backend/internal/config"
	"github.com/odigos-io/centralized-backend/internal/logger"
	redisclient "github.com/odigos-io/centralized-backend/internal/redis"
	websocketserver "github.com/odigos-io/centralized-backend/internal/websocket"
)

func main() {

	logger.InitLogger()
	logger.Log.Info("Starting Centralized Backend Service...")

	ctx := context.Background()

	redisClient, err := redisclient.NewRedisClient(ctx)

	if err != nil {
		logger.Fatal("Failed to connect to Redis", err)
	}

	go handleShutdown(ctx, redisClient)

	wsServer := websocketserver.NewWebSocketServer(redisClient)
	gqlHandler := handler.NewDefaultServer(graph.NewExecutableSchema(graph.Config{Resolvers: &graph.Resolver{
		RedisClient: redisClient,
	}}))

	router := gin.Default()
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type"},
		AllowCredentials: true,
	}))

	router.GET("/ws/proxy", func(c *gin.Context) {
		wsServer.HandleConnection(c.Writer, c.Request)
	})
	router.POST("/graphql", func(c *gin.Context) {
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
