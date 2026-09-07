module github.com/odigos-io/odigos-enterprise/collector/receivers/odigosebpfreceiver

go 1.26.2

require (
	github.com/cilium/ebpf v0.20.0
	github.com/odigos-io/go-rtml v0.0.1
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/collector/component v1.65.0
	go.opentelemetry.io/collector/component/componenttest v0.159.0
	go.opentelemetry.io/collector/confmap v1.65.0
	go.opentelemetry.io/collector/consumer v1.65.0
	go.opentelemetry.io/collector/consumer/consumertest v0.159.0
	go.opentelemetry.io/collector/pdata v1.65.0
	go.opentelemetry.io/collector/receiver v1.65.0
	go.opentelemetry.io/collector/receiver/receivertest v0.159.0
	go.opentelemetry.io/proto/otlp v1.10.0
	go.uber.org/goleak v1.3.0
	go.uber.org/zap v1.28.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/odigos-io/odigos/common v1.37.0-pre1 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.159.0 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	golang.org/x/mod v0.35.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	sigs.k8s.io/controller-runtime v0.23.3 // indirect
)

require (
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/knadh/koanf/maps v0.1.3 // indirect
	github.com/knadh/koanf/providers/confmap v1.0.1 // indirect
	github.com/knadh/koanf/v2 v2.3.6 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/odigos-io/odigos-enterprise/ebpfshare v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.159.0 // indirect
	go.opentelemetry.io/collector/consumer/xconsumer v0.159.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.65.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.159.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.65.0 // indirect
	go.opentelemetry.io/collector/receiver/xreceiver v0.159.0 // indirect
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/metric v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.0 // indirect
)

replace github.com/odigos-io/odigos-enterprise/ebpfshare => ../../../ebpfshare
