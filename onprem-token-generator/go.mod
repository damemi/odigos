module github.com/odigos-io/odigos-enterprise/onprem-token-generator

go 1.24.0

require (
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/odigos-io/odigos/common v0.0.0
	github.com/odigos-io/odigos/profiles v0.0.0
)

require (
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	go.opentelemetry.io/otel v1.35.0 // indirect
	go.opentelemetry.io/otel/trace v1.35.0 // indirect
)

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.0.204-0.20250701072042-3f1cfed31ae8

replace github.com/odigos-io/odigos/profiles => github.com/odigos-io/odigos/profiles v1.0.204-0.20250701072042-3f1cfed31ae8
