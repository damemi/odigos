module github.com/odigos-io/odigos-enterprise/onprem-token-generator

go 1.26.1

require (
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/odigos-io/odigos/common v0.0.0
	github.com/odigos-io/odigos/profiles v0.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/hashicorp/go-version v1.8.0 // indirect
	go.opentelemetry.io/otel v1.42.0 // indirect
	go.opentelemetry.io/otel/trace v1.42.0 // indirect
)

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.23.0-pre8.0.20260412075128-97339bfbcd17

replace github.com/odigos-io/odigos/profiles => github.com/odigos-io/odigos/profiles v1.23.0-pre8.0.20260412075128-97339bfbcd17
