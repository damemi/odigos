package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

const (
	FrontendLocalDefaultBase = "http://localhost:8085/"
	FrontendGraphQLPath      = "graphql"
	FrontendEventsPath       = "api/events"
)

func frontendInClusterBaseURL() string {
	ns := env.GetCurrentNamespace()
	return fmt.Sprintf("http://ui.%s:3000/", ns)
}

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
	return frontendInClusterBaseURL()
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
