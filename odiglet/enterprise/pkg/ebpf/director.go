package ebpf

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DirectorEnterprise interface {
	odigosebpf.Director

	ApplyInstrumentationConfig(workload *workload.PodWorkload, instrumentationConfig *odigosv1.InstrumentationConfig) error
}

type DirectorsEnterpriseMap map[odigosebpf.DirectorKey]DirectorEnterprise

type EbpfDirectorEnterprise[T OtelEbpfSdkEnterprise] struct {
	*odigosebpf.EbpfDirector[T]
}

func NewEbpfDirectorEnterprise[T OtelEbpfSdkEnterprise](ctx context.Context, client client.Client, scheme *runtime.Scheme, language common.ProgrammingLanguage, instrumentationFactory InstrumentationFactoryEnterprise[T]) DirectorEnterprise {
	return &EbpfDirectorEnterprise[T]{
		EbpfDirector: odigosebpf.NewEbpfDirector[T](ctx, client, scheme, language, instrumentationFactory),
	}
}

func (d *EbpfDirectorEnterprise[T]) ApplyInstrumentationConfig(workload *workload.PodWorkload, instrumentationConfig *odigosv1.InstrumentationConfig) error {

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
