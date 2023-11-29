package ebpf

import (
	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
)

func applyConfigToInst(spec *odigosv1.InstrumentationConfigSpec, inst OtelEbpfSdkEnterprise, sdkLanguage common.ProgrammingLanguage) error {

	if spec == nil {
		return unsetWorkloadInstrumentationConfig(inst)
	} else {
		return applyWorkloadInstrumentationConfig(inst, spec, sdkLanguage)
	}
}

func applyWorkloadInstrumentationConfig(inst OtelEbpfSdkEnterprise, spec *odigosv1.InstrumentationConfigSpec, sdkLanguage common.ProgrammingLanguage) error {

	// first we unset all the config options, then we set the ones that are specified in the CRD
	// this ensures that we don't have any stale config options.
	// notice that it might cause a short period of time where the instrumentation option is not set correctly.
	// we might want to consider a more sophisticated approach in the future.
	err := unsetWorkloadInstrumentationConfig(inst)
	if err != nil {
		return err
	}

	for _, option := range spec.Config {
		otelSpanKind := common.SpanKindOdigosToOtel(option.SpanKind)
		for _, lib := range option.InstrumentationLibraries {
			if lib.Language != sdkLanguage {
				continue
			}

			log.Logger.V(2).Info("Applying config to instrumentation", "optionKey", option.OptionKey, "instrumentationLibraryName", lib.InstrumentationLibraryName, "spanKind", otelSpanKind, "optionValue", option.OptionValueBoolean)
			err := inst.ConfigBool(lib.InstrumentationLibraryName, option.OptionKey, otelSpanKind, option.OptionValueBoolean)
			if err != nil {
				log.Logger.Error(err, "failed to apply config to instrumentation", "optionKey", option.OptionKey, "instrumentationLibraryName", lib.InstrumentationLibraryName)
				return err
			}
		}
	}
	return nil
}

func unsetWorkloadInstrumentationConfig(inst OtelEbpfSdkEnterprise) error {
	instrumentationLibraries := inst.GetConfigOptions()
	for instrumentationLibraryName, options := range instrumentationLibraries {
		for _, option := range options {
			defaultValue := false
			err := inst.ConfigBool(instrumentationLibraryName, option.OptionKey, option.SpanKind, defaultValue)
			if err != nil {
				log.Logger.Error(err, "failed to apply config to instrumentation", "optionKey", option.OptionKey, "instrumentationLibraryName", instrumentationLibraryName)
				return err
			}
		}
	}
	return nil
}
