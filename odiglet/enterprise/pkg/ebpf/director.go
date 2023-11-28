package ebpf

import (
	"context"
	"fmt"

	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	odigosebpf "github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	apitrace "go.opentelemetry.io/otel/trace"
)

type OtelEbpfSdkEnterprise interface {
	odigosebpf.OtelEbpfSdk
	ConfigBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error
}

type InstrumentationFactoryEnterprise[T OtelEbpfSdkEnterprise] interface {
	CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload) (T, error)
}

type DirectorEnterprise interface {
	odigosebpf.Director

	ApplyInstrumentationConfig(instrumentationConfig *odigosv1.InstrumentationConfig) error
}

type EbpfDirectorEnterprise[T OtelEbpfSdkEnterprise] struct {
	*odigosebpf.EbpfDirector[T]
}

func NewEbpfDirectorEnterprise[T OtelEbpfSdkEnterprise](language common.ProgrammingLanguage, instrumentationFactory InstrumentationFactoryEnterprise[T]) DirectorEnterprise {
	return &EbpfDirectorEnterprise[T]{
		EbpfDirector: odigosebpf.NewEbpfDirector[T](language, instrumentationFactory),
	}
}

func (d *EbpfDirectorEnterprise[T]) ApplyInstrumentationConfig(instrumentationConfig *odigosv1.InstrumentationConfig) error {

	log.Logger.V(0).Info("Applying instrumentation config", "instrumentationConfig", instrumentationConfig)

	if len(instrumentationConfig.OwnerReferences) != 1 {
		return fmt.Errorf("expected instrumentation config object to have exactly 1 owner reference, got %d", len(instrumentationConfig.OwnerReferences))
	}

	workload := common.PodWorkload{
		Namespace: instrumentationConfig.Namespace,
		Kind:      instrumentationConfig.OwnerReferences[0].Kind,
		Name:      instrumentationConfig.OwnerReferences[0].Name,
	}

	insts := d.GetWorkloadInstrumentations(workload)

	log.Logger.V(0).Info("Applying instrumentation config", "num insts", len(insts))

	var retErr []error
	for _, inst := range insts {
		err := applyConfigToInst(&instrumentationConfig.Spec, inst, d.Language())
		if err != nil {
			retErr = append(retErr, err)
		}
	}
	if len(retErr) > 0 {
		return fmt.Errorf("failed to apply config to %d instrumentations", len(retErr))
	}
	return nil
}
