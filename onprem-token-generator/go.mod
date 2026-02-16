module github.com/odigos-io/odigos-enterprise/onprem-token-generator

go 1.25.0

require (
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/odigos-io/odigos/common v0.0.0
	github.com/odigos-io/odigos/profiles v0.0.0
)

require (
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	go.opentelemetry.io/otel v1.38.0 // indirect
	go.opentelemetry.io/otel/trace v1.38.0 // indirect
)

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.18.0-pre0.0.20260216093902-f2a46092c35f

replace github.com/odigos-io/odigos/profiles => github.com/odigos-io/odigos/profiles v1.18.0-pre0.0.20260216093902-f2a46092c35f
