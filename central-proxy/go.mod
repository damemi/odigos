module github.com/odigos-io/central-proxy

go 1.26.4

require (
	github.com/google/uuid v1.6.0
	github.com/odigos-io/odigos/api v1.39.0-pre1.0.20261009090907-46338c3d981a
	github.com/odigos-io/odigos/frontend v1.39.0-pre1.0.20261009090907-46338c3d981a
	github.com/odigos-io/odigos/k8sutils v1.39.0-pre1.0.20261009090907-46338c3d981a
	k8s.io/apimachinery v0.35.4
	k8s.io/client-go v0.35.4
)

require (
	github.com/99designs/gqlgen v0.17.70 // indirect
	github.com/argoproj/argo-rollouts v1.9.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/go-openapi/swag/cmdutils v0.28.0 // indirect
	github.com/go-openapi/swag/conv v0.28.0 // indirect
	github.com/go-openapi/swag/fileutils v0.28.0 // indirect
	github.com/go-openapi/swag/jsonutils v0.28.0 // indirect
	github.com/go-openapi/swag/loading v0.28.0 // indirect
	github.com/go-openapi/swag/mangling v0.28.0 // indirect
	github.com/go-openapi/swag/netutils v0.28.0 // indirect
	github.com/go-openapi/swag/pools v0.28.0 // indirect
	github.com/go-openapi/swag/stringutils v0.28.0 // indirect
	github.com/go-openapi/swag/typeutils v0.28.0 // indirect
	github.com/go-openapi/swag/yamlutils v0.28.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/odigos-io/odigos/status v0.0.0-00010101000000-000000000000 // indirect
	github.com/openshift/api v3.9.0+incompatible // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/sosodev/duration v1.3.1 // indirect
	github.com/vektah/gqlparser/v2 v2.5.27 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	sigs.k8s.io/controller-runtime v0.23.3 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.3.2-0.20260122202528-d9cc6641c482 // indirect
)

require (
	github.com/coder/websocket v1.8.12
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.12.2 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.28.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/odigos-io/odigos/common v1.39.0-pre1.0.20261009090907-46338c3d981a
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/api v0.35.4
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20250910181357-589584f1c912 // indirect
	k8s.io/utils v0.0.0-20251002143259-bc988d571ff4 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/yaml v1.6.0
)

// k8sutils (required above) requires these via local-only replaces in the odigos-io/odigos repo, which
// don't propagate across repos. Pinned to the same commit already used for api/k8sutils/common here.
replace github.com/odigos-io/odigos/actions => github.com/odigos-io/odigos/actions v1.39.0-pre1.0.20261009090907-46338c3d981a

// frontend (required above) requires this the same way, via k8sutils.
replace github.com/odigos-io/odigos/destinations => github.com/odigos-io/odigos/destinations v1.39.0-pre1.0.20261009090907-46338c3d981a

// Same as actions above, required transitively via k8sutils.
replace github.com/odigos-io/odigos/odigosauth => github.com/odigos-io/odigos/odigosauth v1.39.0-pre1.0.20261009090907-46338c3d981a

replace github.com/odigos-io/odigos/status => github.com/odigos-io/odigos/status v1.39.0-pre1.0.20261009090907-46338c3d981a

// argo-rollouts@v1.9.1 requires this via its own internal-only replace, which doesn't propagate to us.
replace k8s.io/kubelet => k8s.io/kubelet v0.34.1
