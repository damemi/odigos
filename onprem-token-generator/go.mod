module github.com/odigos-io/odigos-enterprise/onprem-token-generator

go 1.26.4

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/odigos-io/odigos/common v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/profiles v0.0.0-00010101000000-000000000000
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
)

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.37.0-pre2

replace github.com/odigos-io/odigos/profiles => github.com/odigos-io/odigos/profiles v1.37.0-pre2
