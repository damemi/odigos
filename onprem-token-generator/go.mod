module github.com/odigos-io/odigos-enterprise/onprem-token-generator

go 1.23.0

require github.com/odigos-io/odigos/profiles v1.0.143

require (
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/odigos-io/odigos/common v0.0.0
	go.opentelemetry.io/otel v1.34.0 // indirect
	go.opentelemetry.io/otel/trace v1.34.0 // indirect
)

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.0.159-0.20250228203422-40adcb13abeb
