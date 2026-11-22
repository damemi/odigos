module github.com/odigos-io/odigos/odigosauth

go 1.27.0

require (
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/odigos-io/odigos/common v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/odigos-io/odigos/common => ../common
