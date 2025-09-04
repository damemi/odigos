module github.com/odigos-io/odigos-enterprise/odiglet

go 1.24.0

require (
	github.com/odigos-io/ebpf-generic-instrumentation v0.0.4-0.20250724082156-0c6161be6292
	github.com/odigos-io/ebpf-java-instrumentation v0.0.0-20250904112724-df9f3f0838a3
	github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension v0.0.0-20250904112724-df9f3f0838a3
	github.com/odigos-io/ebpf-nodejs-instrumentation v0.0.0-20250724082002-7250611b1a89
	github.com/odigos-io/mysql-instrumentation v0.0.0-20250105084718-c6cb16fc984d
	github.com/odigos-io/odigos-enterprise/distros v0.0.0
	github.com/odigos-io/odigos-enterprise/odigosauth v0.0.0
	github.com/odigos-io/odigos/api v1.2.0-pre1
	github.com/odigos-io/odigos/common v1.2.0-pre1
	github.com/odigos-io/odigos/instrumentation v1.2.0-pre1
	github.com/odigos-io/odigos/k8sutils v1.2.0-pre1
	github.com/odigos-io/odigos/odiglet v1.2.0-pre1.0.20250904070431-e4bde6f3494c
	github.com/odigos-io/odigos/procdiscovery v1.0.220-pre1.0.20250823160726-4e7fff8c09a6
	go.opentelemetry.io/auto v0.22.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.37.0
	go.opentelemetry.io/otel/trace v1.37.0
	k8s.io/client-go v0.33.1
	sigs.k8s.io/controller-runtime v0.20.4
)

require (
	github.com/agoda-com/opentelemetry-logs-go v0.5.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cilium/ebpf v0.19.1-0.20250815145053-c9de60689836 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/elliotchance/orderedmap v1.6.0 // indirect
	github.com/emicklei/go-restful/v3 v3.12.1 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.21.0 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/goburrow/cache v0.1.4 // indirect
	github.com/gocql/gocql v1.7.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt v3.2.2+incompatible // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/gnostic-models v0.6.9 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/pprof v0.0.0-20250630185457-6e76a2b096b5 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grafana/jvmtools v0.0.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.1 // indirect
	github.com/hailocab/go-hostpool v0.0.0-20160125115350-e80d13ce29ed // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/odigos-io/odigos/distros v1.2.0-pre1 // indirect
	github.com/odigos-io/odigos/opampserver v1.2.0-pre1 // indirect
	github.com/odigos-io/opentelemetry-zap-bridge v0.0.5 // indirect
	github.com/odigos-io/runtime-detector v0.0.19 // indirect
	github.com/parca-dev/parca-agent v0.9.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.22.0 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.65.0 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/spf13/pflag v1.0.6 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/collector/pdata v1.36.0 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.61.0 // indirect
	go.opentelemetry.io/contrib/exporters/autoexport v0.61.0 // indirect
	go.opentelemetry.io/otel v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.13.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.13.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.59.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.13.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.37.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.37.0 // indirect
	go.opentelemetry.io/otel/log v0.13.0 // indirect
	go.opentelemetry.io/otel/metric v1.37.0 // indirect
	go.opentelemetry.io/otel/sdk v1.37.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.13.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.37.0 // indirect
	go.opentelemetry.io/proto/otlp v1.7.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/arch v0.20.0 // indirect
	golang.org/x/exp v0.0.0-20250813145105-42675adae3e6 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/oauth2 v0.30.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/term v0.32.0 // indirect
	golang.org/x/text v0.27.0 // indirect
	golang.org/x/time v0.9.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.4.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250603155806-513f23925822 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250603155806-513f23925822 // indirect
	google.golang.org/grpc v1.73.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.33.1 // indirect
	k8s.io/apiextensions-apiserver v0.33.1 // indirect
	k8s.io/apimachinery v0.33.1 // indirect
	k8s.io/cri-api v0.33.1 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.0.0-20250318190949-c8a335a9a2ff // indirect
	k8s.io/utils v0.0.0-20241210054802-24370beab758 // indirect
	rsc.io/binaryregexp v0.2.0 // indirect
	sigs.k8s.io/json v0.0.0-20241014173422-cfa47c3a1cc8 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.7.0 // indirect
	sigs.k8s.io/yaml v1.5.0 // indirect
)

// currently, enterprise-go-instrumentation has no versioning or releases, so we pin it to main here.
// it would be better to import "github.com/odigos-io/enterprise-go-instrumentation" directly,
// but this is not possible at the moment since the enterprise fork still uses "go.opentelemetry.io/auto"
// for it's name, which needs to be changed first.
replace go.opentelemetry.io/auto => github.com/odigos-io/enterprise-go-instrumentation v0.0.0-20250904065027-8ed3fe89baa6

replace github.com/odigos-io/odigos/api => github.com/odigos-io/odigos/api v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/procdiscovery => github.com/odigos-io/odigos/procdiscovery v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/k8sutils => github.com/odigos-io/odigos/k8sutils v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/opampserver => github.com/odigos-io/odigos/opampserver v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/instrumentation => github.com/odigos-io/odigos/instrumentation v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos/distros => github.com/odigos-io/odigos/distros v1.2.0-pre1.0.20250904070431-e4bde6f3494c

replace github.com/odigos-io/odigos-enterprise/odigosauth => ../odigosauth

replace github.com/odigos-io/odigos-enterprise/distros => ../distros
