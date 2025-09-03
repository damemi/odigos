package config

import (
	"os"
	"strings"
)

const (
	FrontendLocalDefaultBase     = "http://localhost:8085/"
	FrontendInClusterDefaultBase = "http://ui.odigos-system:3000/"
	FrontendGraphQLPath          = "graphql"
	FrontendEventsPath           = "api/events"
)

func FrontendBaseURL() string {
	val := os.Getenv("FRONTEND_BACKEND_URL")
	if val != "" {

		val = strings.TrimSuffix(val, "/graphql")
		if !strings.HasSuffix(val, "/") {
			val += "/"
		}
		return val
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return FrontendLocalDefaultBase
	}
	return FrontendInClusterDefaultBase
}

func FrontendGraphQLURL() string {
	val := os.Getenv("FRONTEND_BACKEND_URL")
	if val != "" {
		if strings.HasSuffix(val, "/graphql") {
			return val
		}
		if !strings.HasSuffix(val, "/") {
			val += "/"
		}
		return val + FrontendGraphQLPath
	}
	return FrontendBaseURL() + FrontendGraphQLPath
}

// FrontendEventsURL returns the SSE events endpoint URL for the frontend backend.
func FrontendEventsURL() string {
	base := FrontendBaseURL()
	return base + FrontendEventsPath
}
