package config

import (
	"os"

	"github.com/odigos-io/central-backend/internal/utils"
)

type AppConfig struct {
	RedisAddr            string
	ServerPort           string
	CentralizedBackendID string
}

var Config AppConfig

func init() {
	Config = AppConfig{
		RedisAddr:            getEnv("REDIS_ADDR", "redis:6379"),
		ServerPort:           getEnv("SERVER_PORT", "8081"),
		CentralizedBackendID: utils.GenerateUUID(),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
