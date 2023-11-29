package ebpf

import (
	"fmt"

	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	odigosebpf "github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
)

type DirectorEnterprise interface {
	odigosebpf.Director

	ApplyInstrumentationConfig(workload *common.PodWorkload, instrumentationConfig *odigosv1.InstrumentationConfig) error
}

type EbpfDirectorEnterprise[T OtelEbpfSdkEnterprise] struct {
	*odigosebpf.EbpfDirector[T]
}

func NewEbpfDirectorEnterprise[T OtelEbpfSdkEnterprise](language common.ProgrammingLanguage, instrumentationFactory InstrumentationFactoryEnterprise[T]) DirectorEnterprise {
	return &EbpfDirectorEnterprise[T]{
		EbpfDirector: odigosebpf.NewEbpfDirector[T](language, instrumentationFactory),
	}
}

func (d *EbpfDirectorEnterprise[T]) ApplyInstrumentationConfig(workload *common.PodWorkload, instrumentationConfig *odigosv1.InstrumentationConfig) error {

	insts := d.GetWorkloadInstrumentations(workload)

	log.Logger.V(3).Info("Applying config to instrumentations after CRD change", "instrumentationConfig", instrumentationConfig, "workload", workload, "SDKs count", len(insts))

	var retErr []error
	for _, inst := range insts {

		var spec *odigosv1.InstrumentationConfigSpec
		if instrumentationConfig != nil {
			spec = &instrumentationConfig.Spec
		}

		err := applyConfigToInst(spec, inst, d.Language())
		if err != nil {
			retErr = append(retErr, err)
		}
	}
	if len(retErr) > 0 {
		return fmt.Errorf("failed to apply config to %d instrumentations", len(retErr))
	}
	return nil
}
