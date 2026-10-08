module github.com/odigos-io/odigos-enterprise/connectors/providers/aws

go 1.26.2

replace github.com/odigos-io/odigos-enterprise/connectors => ../..

replace github.com/odigos-io/odigos-enterprise/connectors/base => ../../base

require (
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/config v1.32.25
	github.com/aws/aws-sdk-go-v2/credentials v1.19.24
	github.com/aws/aws-sdk-go-v2/service/applicationautoscaling v1.49.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.86.0
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.308.0
	github.com/aws/aws-sdk-go-v2/service/ecr v1.60.1
	github.com/aws/aws-sdk-go-v2/service/ecs v1.84.0
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.62.0
	github.com/aws/aws-sdk-go-v2/service/lambda v1.92.3
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.48.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.43.3
	github.com/aws/smithy-go v1.28.1
	github.com/google/go-containerregistry v0.21.7
	github.com/jackc/pgx/v5 v5.10.0
	github.com/odigos-io/odigos-enterprise/connectors v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos-enterprise/connectors/base v0.0.0-00010101000000-000000000000
	github.com/odigos-io/odigos/common v1.29.0
	github.com/odigos-io/odigos/distros v1.33.0-pre4
	go.uber.org/zap v1.28.0
)

require (
	github.com/99designs/gqlgen v0.17.91 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.29 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.31.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.6 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/docker/cli v29.5.3+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/gabriel-vasile/mimetype v1.4.13 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.3 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/odigos-io/agents-api/go/server v0.24.0 // indirect
	github.com/odigos-io/odigos/destinations v1.29.0 // indirect
	github.com/open-telemetry/opamp-go v0.23.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	github.com/urfave/cli/v3 v3.9.0 // indirect
	github.com/vektah/gqlparser/v2 v2.5.34 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gotest.tools/v3 v3.5.2 // indirect
	sigs.k8s.io/controller-runtime v0.23.3 // indirect
)
