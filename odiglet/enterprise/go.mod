module github.com/odigos-io/odigos-enterprise/odiglet

go 1.26.4

require (
	github.com/cilium/ebpf v0.22.0
	github.com/container-storage-interface/spec v1.10.0
	github.com/odigos-io/ebpf-core v0.32.0
	github.com/odigos-io/ebpf-generic-instrumentation v0.0.14
	github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2 v2.56.0
	github.com/odigos-io/ebpf-java-instrumentation/v2 v2.56.0
	github.com/odigos-io/ebpf-nodejs-instrumentation v0.16.0
	github.com/odigos-io/mysql-instrumentation v0.0.0-20250724082230-018ecf4e9752
	github.com/odigos-io/odigos-enterprise/distros v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos-enterprise/ebpfshare v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos-enterprise/odigosauth v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/api v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/common v1.39.0-pre1.0.20261004141513-994d2da2959b
	github.com/odigos-io/odigos/deviceplugin v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/distros v1.39.0-pre1.0.20261004141513-994d2da2959b
	github.com/odigos-io/odigos/instrumentation v1.39.0-pre1.0.20261004141513-994d2da2959b
	github.com/odigos-io/odigos/k8sutils v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/procdiscovery v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
	go.opentelemetry.io/collector/consumer v1.67.0
	go.opentelemetry.io/collector/consumer/consumertest v0.161.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.46.0
	google.golang.org/grpc v1.83.2
	k8s.io/client-go v0.35.4
	k8s.io/kubelet v0.35.2
	sigs.k8s.io/controller-runtime v0.23.3
)

require (
	github.com/argoproj/argo-rollouts v1.9.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.24 // indirect
	github.com/caarlos0/env/v11 v11.4.1 // indirect
	github.com/coreos/go-systemd/v22 v22.6.0 // indirect
	github.com/cyphar/filepath-securejoin v0.4.1 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/go-openapi/swag/cmdutils v0.28.0 // indirect
	github.com/go-openapi/swag/conv v0.28.0 // indirect
	github.com/go-openapi/swag/fileutils v0.28.0 // indirect
	github.com/go-openapi/swag/jsonutils v0.28.0 // indirect
	github.com/go-openapi/swag/loading v0.28.0 // indirect
	github.com/go-openapi/swag/mangling v0.28.0 // indirect
	github.com/go-openapi/swag/netutils v0.28.0 // indirect
	github.com/go-openapi/swag/stringutils v0.28.0 // indirect
	github.com/go-openapi/swag/typeutils v0.28.0 // indirect
	github.com/go-openapi/swag/yamlutils v0.28.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/opencontainers/cgroups v0.0.6 // indirect
	github.com/openshift/api v3.9.0+incompatible // indirect
	github.com/oschwald/maxminddb-golang/v2 v2.2.0 // indirect
	github.com/shirou/gopsutil/v4 v4.26.6 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/AlessandroPomponio/go-gibberish v0.0.0-20191004143433-a2d4156f0396 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp v1.33.0 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/andybalholm/brotli v1.2.2 // indirect
	github.com/aws/aws-sdk-go-v2 v1.41.7 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.18 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.17 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.23 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.42.1 // indirect
	github.com/aws/smithy-go v1.26.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bluele/gcache v0.0.2 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cenkalti/backoff/v7 v7.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containers/common v0.64.2 // indirect
	github.com/cornelk/hashmap v1.0.8 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/elliotchance/orderedmap v1.6.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/foxboron/go-tpm-keyfiles v0.0.0-20251226215517-609e4778396f // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/gabriel-vasile/mimetype v1.4.13 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.28.0 // indirect
	github.com/go-openapi/swag/pools v0.28.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.3 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goburrow/cache v0.1.4 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/gocql/gocql v1.7.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/glog v1.2.5 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/pprof v0.0.0-20260202012954-cb029daf43ef // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/grafana/go-offsets-tracker v0.1.7 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/hailocab/go-hostpool v0.0.0-20160125115350-e80d13ce29ed // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/invopop/jsonschema v0.13.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/knadh/koanf/maps v0.1.3 // indirect
	github.com/knadh/koanf/providers/confmap v1.0.1 // indirect
	github.com/knadh/koanf/v2 v2.3.6 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20251013123823-9fd1530e3ec3 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/moby/api v1.55.0 // indirect
	github.com/moby/moby/client v0.5.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/odigos-io/odigos-device-plugin v1.19.8 // indirect
	github.com/odigos-io/odigos/odiglet v1.39.0-pre1.0.20261004141513-994d2da2959b
	github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks/obi v1.39.0-pre1.0.20261004141513-994d2da2959b // indirect
	github.com/odigos-io/odigos/opampserver v1.39.0-pre1.0.20261004141513-994d2da2959b // indirect
	github.com/odigos-io/runtime-detector v0.0.25 // indirect
	github.com/ohler55/ojg v1.28.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/parca-dev/parca-agent v0.9.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.29 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/rs/cors v1.11.1 // indirect
	github.com/samber/lo v1.51.0 // indirect
	github.com/samber/slog-common v0.19.0 // indirect
	github.com/samber/slog-multi v1.5.0 // indirect
	github.com/samber/slog-sampling v1.5.4 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/vektah/gqlparser/v2 v2.5.36 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xwb1989/sqlparser v0.0.0-20180606152119-120387863bf2 // indirect
	github.com/yl2chen/cidranger v1.0.2 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.mongodb.org/mongo-driver/v2 v2.6.0 // indirect
	go.opentelemetry.io/auto v0.24.0
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector v0.161.0 // indirect
	go.opentelemetry.io/collector/client v1.67.0 // indirect
	go.opentelemetry.io/collector/component v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configauth v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configcompression v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configgrpc v1.67.0 // indirect
	go.opentelemetry.io/collector/config/confighttp v0.161.0 // indirect
	go.opentelemetry.io/collector/config/configmiddleware v1.67.0 // indirect
	go.opentelemetry.io/collector/config/confignet v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configopaque v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configoptional v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configretry v1.67.0 // indirect
	go.opentelemetry.io/collector/config/configtelemetry v0.151.0 // indirect
	go.opentelemetry.io/collector/config/configtls v1.67.0 // indirect
	go.opentelemetry.io/collector/confmap v1.67.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.161.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror/xconsumererror v0.161.0 // indirect
	go.opentelemetry.io/collector/consumer/xconsumer v0.161.0 // indirect
	go.opentelemetry.io/collector/exporter v1.67.0 // indirect
	go.opentelemetry.io/collector/exporter/debugexporter v0.151.0 // indirect
	go.opentelemetry.io/collector/exporter/exporterhelper v0.161.0 // indirect
	go.opentelemetry.io/collector/exporter/exporterhelper/xexporterhelper v0.161.0 // indirect
	go.opentelemetry.io/collector/exporter/otlpexporter v0.161.0 // indirect
	go.opentelemetry.io/collector/exporter/otlphttpexporter v0.161.0 // indirect
	go.opentelemetry.io/collector/exporter/xexporter v0.161.0 // indirect
	go.opentelemetry.io/collector/extension v1.67.0 // indirect
	go.opentelemetry.io/collector/extension/extensionauth v1.67.0 // indirect
	go.opentelemetry.io/collector/extension/extensionmiddleware v0.161.0 // indirect
	go.opentelemetry.io/collector/extension/xextension v0.161.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.67.0 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.161.0 // indirect
	go.opentelemetry.io/collector/pdata v1.67.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.161.0 // indirect
	go.opentelemetry.io/collector/pdata/xpdata v0.161.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.67.0 // indirect
	go.opentelemetry.io/collector/pipeline/xpipeline v0.161.0 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.65.0 // indirect
	go.opentelemetry.io/contrib/detectors/aws/ec2/v2 v2.5.1 // indirect
	go.opentelemetry.io/contrib/detectors/aws/eks v1.44.0 // indirect
	go.opentelemetry.io/contrib/detectors/azure/azurevm v0.16.0 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.44.0 // indirect
	go.opentelemetry.io/contrib/exporters/autoexport v0.65.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.70.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/obi v0.10.1-0.20260706144415-c76a93c8775c // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.68.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.45.0 // indirect
	go.opentelemetry.io/otel/log v0.20.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.20.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/arch v0.28.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.4.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.35.4 // indirect
	k8s.io/apiextensions-apiserver v0.35.0 // indirect
	k8s.io/apimachinery v0.35.4 // indirect
	k8s.io/cri-api v0.35.2 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260520065146-aa012df4f4af // indirect
	k8s.io/utils v0.0.0-20260507154919-ff6756f316d2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.0 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

// currently, enterprise-go-instrumentation has no versioning or releases, so we pin it to main here.
// it would be better to import "github.com/odigos-io/enterprise-go-instrumentation" directly,
// but this is not possible at the moment since the enterprise fork still uses "go.opentelemetry.io/auto"
// for it's name, which needs to be changed first.
replace go.opentelemetry.io/auto => github.com/odigos-io/enterprise-go-instrumentation v1.32.0

replace github.com/odigos-io/odigos/procdiscovery => github.com/odigos-io/odigos/procdiscovery v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos/k8sutils => github.com/odigos-io/odigos/k8sutils v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos/opampserver => github.com/odigos-io/odigos/opampserver v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos/distros => github.com/odigos-io/odigos/distros v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos-enterprise/odigosauth => ../odigosauth

replace github.com/odigos-io/odigos-enterprise/distros => ../distros

// parca-agent pins cloud.google.com/go/compute v1.6.1, which still embeds cloud.google.com/go/compute/metadata.
// grpc and OTel GCP detectors use the standalone cloud.google.com/go/compute/metadata module — same import path, two modules.
// Newer compute releases dropped the nested metadata package; this replace removes the ambiguity.
// Added during OBI change
replace cloud.google.com/go/compute => cloud.google.com/go/compute v1.57.0

replace github.com/odigos-io/odigos/api => github.com/odigos-io/odigos/api v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos/common => github.com/odigos-io/odigos/common v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos/deviceplugin => github.com/odigos-io/odigos/deviceplugin v1.39.0-pre1.0.20261004141513-994d2da2959b

// k8sutils requires this via its own local-only replace, which doesn't propagate across repos.
replace github.com/odigos-io/odigos/actions => github.com/odigos-io/odigos/actions v1.39.0-pre1.0.20261004141513-994d2da2959b

// Same as actions above, required transitively via k8sutils.
replace github.com/odigos-io/odigos/odigosauth => github.com/odigos-io/odigos/odigosauth v1.39.0-pre1.0.20261004141513-994d2da2959b

replace github.com/odigos-io/odigos-enterprise/ebpfshare => ../ebpfshare
