package ebpf

import (
	"context"

	"github.com/keyval-dev/odigos/common"
	odigosebpf "github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
	apitrace "go.opentelemetry.io/otel/trace"
)

type ConfigOption struct {
	OptionKey string
	SpanKind  apitrace.SpanKind
}

// this interface is used to represent a single OTEL eBPF SDK instance.
// it is expected to have one SDK per compatible pid.
// The pid is part of a pod, which is part of a workload.
// This interface is generic and does not assume any specific SDK or language,
// so it can be extended to support any SDK in the future (java, python, nodejs, etc.)
type OtelEbpfSdkEnterprise interface {
	odigosebpf.OtelEbpfSdk
	ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error
	GetConfigOptions() map[string][]ConfigOption
}

type InstrumentationFactoryEnterprise[T OtelEbpfSdkEnterprise] interface {
	CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload, containerName string, podName string) (T, error)
}
