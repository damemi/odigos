package ebpf

import (
	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
)

func applyConfigToInst(spec *odigosv1.InstrumentationConfigSpec, inst OtelEbpfSdkEnterprise, sdkLanguage common.ProgrammingLanguage) error {

	for _, option := range spec.Config {
		otelSpanKind := common.SpanKindOdigosToOtel(option.SpanKind)
		for _, lib := range option.InstrumentationLibraries {
			if lib.Language != sdkLanguage {
				continue
			}

			log.Logger.V(0).Info("Applying config to instrumentation", "optionKey", option.OptionKey, "instrumentationLibraryName", lib.InstrumentationLibraryName, "spanKind", otelSpanKind, "optionValue", option.OptionValueBoolean)
			err := inst.ConfigBool(lib.InstrumentationLibraryName, option.OptionKey, otelSpanKind, option.OptionValueBoolean)
			if err != nil {
				log.Logger.Error(err, "failed to apply config to instrumentation", "optionKey", option.OptionKey, "instrumentationLibraryName", lib.InstrumentationLibraryName)
				return err
			}
		}
	}
	return nil
}
