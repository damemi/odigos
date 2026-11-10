package config

import (
	"net"
	"os"
	"time"

	"github.com/odigos-io/odigos/central-backend/internal/utils"
)

type AppConfig struct {
	RedisAddr            string
	ServerPort           string
	MetricsPort          string
	InstanceID string
}

var Config AppConfig

func init() {
	Config = AppConfig{
		RedisAddr:            getRedisAddr(),
		ServerPort:           GetEnv("SERVER_PORT", "8081"),
		MetricsPort:          GetEnv("METRICS_PORT", "8082"),
		InstanceID: utils.GenerateUUID(),
	}
}

func getRedisAddr() string {
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		return addr
	}

	if isRedisAvailable("localhost:6379") {
		return "localhost:6379"
	}

	return "redis:6379"
}

func isRedisAvailable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func GetEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
