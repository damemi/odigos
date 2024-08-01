module github.com/odigos-io/odigos-enterprise/odiglet

go 1.22.0

require (
	github.com/kubevirt/device-plugin-manager v1.19.5
	github.com/odigos-io/ebpf-generic-instrumentation v0.0.0-20240723105257-baafa0aceb19
	github.com/odigos-io/ebpf-java-instrumentation v0.0.0-20240731105355-d9ee4e7b6a53
	github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension v0.0.0-20240731105355-d9ee4e7b6a53
	github.com/odigos-io/ebpf-nodejs-instrumentation v0.0.0-20240507052149-b6157ec78dc9
	github.com/odigos-io/mysql-instrumentation v0.0.0-20240801061800-241ec87ae161
	github.com/odigos-io/odigos-enterprise/odigosauth v0.0.0
	github.com/odigos-io/odigos/api v1.0.86
	github.com/odigos-io/odigos/common v1.0.86
	github.com/odigos-io/odigos/k8sutils v1.0.86
	github.com/odigos-io/odigos/odiglet v1.0.86
	go.opentelemetry.io/auto v0.14.0-alpha
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.28.0
	go.opentelemetry.io/otel/trace v1.28.0
	k8s.io/apimachinery v0.30.3
	k8s.io/client-go v0.30.3
	k8s.io/kubelet v0.30.3
	sigs.k8s.io/controller-runtime v0.18.4
)

require (
	github.com/agoda-com/opentelemetry-logs-go v0.4.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cilium/ebpf v0.16.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/elliotchance/orderedmap v1.6.0 // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/evanphx/json-patch/v5 v5.9.0 // indirect
	github.com/fatih/color v1.15.0 // indirect
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.19.6 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.22.3 // indirect
	github.com/goburrow/cache v0.1.4 // indirect
	github.com/goccy/go-yaml v1.11.3 // indirect
	github.com/gocql/gocql v1.6.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt v3.2.2+incompatible // indirect
	github.com/golang/glog v1.2.1 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/pprof v0.0.0-20220608213341-c488b8fa1db3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.20.0 // indirect
	github.com/hailocab/go-hostpool v0.0.0-20160125115350-e80d13ce29ed // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/moby/sys/mountinfo v0.7.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/odigos-io/odigos/procdiscovery v0.0.0 // indirect
	github.com/odigos-io/opentelemetry-zap-bridge v0.0.5 // indirect
	github.com/parca-dev/parca-agent v0.9.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.19.1 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.53.0 // indirect
	go.opentelemetry.io/contrib/exporters/autoexport v0.53.0 // indirect
	go.opentelemetry.io/otel v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.4.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.50.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.4.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.28.0 // indirect
	go.opentelemetry.io/otel/log v0.4.0 // indirect
	go.opentelemetry.io/otel/metric v1.28.0 // indirect
	go.opentelemetry.io/otel/sdk v1.28.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.4.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.28.0 // indirect
	go.opentelemetry.io/proto/otlp v1.3.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/arch v0.8.0 // indirect
	golang.org/x/exp v0.0.0-20230224173230-c95f2b4c22f2 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/oauth2 v0.21.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/term v0.22.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2 // indirect
	gomodules.xyz/jsonpatch/v2 v2.4.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
	google.golang.org/grpc v1.65.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.30.3 // indirect
	k8s.io/apiextensions-apiserver v0.30.3 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.0.0-20240228011516-70dd3763d340 // indirect
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8 // indirect
	rsc.io/binaryregexp v0.2.0 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.1 // indirect
	sigs.k8s.io/yaml v1.4.0 // indirect
)

// currently, enterprise-go-instrumentation has no versioning or releases, so we pin it to main here.
// it would be better to import "github.com/odigos-io/enterprise-go-instrumentation" directly,
// but this is not possible at the moment since the enterprise fork still uses "go.opentelemetry.io/auto"
// for it's name, which needs to be changed first.
replace go.opentelemetry.io/auto => github.com/odigos-io/enterprise-go-instrumentation v0.0.0-20240728105355-b11ba674d562

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.0.86

replace github.com/odigos-io/odigos/procdiscovery => github.com/odigos-io/odigos/procdiscovery v1.0.86

replace github.com/odigos-io/odigos/k8sutils => github.com/odigos-io/odigos/k8sutils v1.0.86

replace github.com/odigos-io/odigos/opampserver => github.com/odigos-io/odigos/opampserver v1.0.86

// Used for testing purposes
// replace github.com/odigos-io/odigos/odiglet => github.com/RonFed/odigos_fork/odiglet add_java_envs_to_envOverwrite

replace github.com/odigos-io/odigos-enterprise/odigosauth => ../odigosauth
