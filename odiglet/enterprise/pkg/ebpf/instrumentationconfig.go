package ebpf

import (
	"context"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Deprecated: This method will be removed once the migration to the generic config provider is complete.
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

func convertToInstrumentationLibraries(configOptions map[string][]ConfigOption) []odigosv1.InstrumentationLibraryOptions {
	instrumentationLibraries := make([]odigosv1.InstrumentationLibraryOptions, 0, len(configOptions))

	for instrumentationLibraryName, options := range configOptions {

		commonOptions := convertConfigOptions(options)
		instrumentationLibrary := odigosv1.InstrumentationLibraryOptions{
			LibraryName: instrumentationLibraryName,
			Options:     commonOptions,
		}
		instrumentationLibraries = append(instrumentationLibraries, instrumentationLibrary)
	}
	return instrumentationLibraries
}

func convertConfigOptions(options []ConfigOption) []odigosv1.ConfigOption {
	commonOptions := make([]odigosv1.ConfigOption, len(options))
	for i, option := range options {
		commonOptions[i] = odigosv1.ConfigOption{
			OptionKey: option.OptionKey,
			SpanKind:  common.ConvertSpanKindToString(option.SpanKind),
		}
	}
	return commonOptions
}

func findContainerIndex(options []odigosv1.OptionByContainer, containerName string) int {
	for i, option := range options {
		if option.ContainerName == containerName {
			return i
		}
	}
	return -1
}

func updateInstrumentedApplication(ctx context.Context, kubeclient client.Client, podWorkload *workload.PodWorkload, containerName string, configOptions map[string][]ConfigOption) error {
	// Check if the InstrumentedApplication already exists
	instrumentedApplicationCrd := &odigosv1.InstrumentedApplication{}
	instrumentedApplicationKey := client.ObjectKey{
		Namespace: podWorkload.Namespace,
		Name:      workload.CalculateWorkloadRuntimeObjectName(podWorkload.Name, podWorkload.Kind),
	}

	err := kubeclient.Get(ctx, instrumentedApplicationKey, instrumentedApplicationCrd)
	if err != nil {
		return err
	}

	if containerIndex := findContainerIndex(instrumentedApplicationCrd.Spec.Options, containerName); containerIndex >= 0 {
		instrumentedApplicationCrd.Spec.Options[containerIndex].InstrumentationLibraries = convertToInstrumentationLibraries(configOptions)

	} else {
		newOption := odigosv1.OptionByContainer{
			ContainerName:            containerName,
			InstrumentationLibraries: convertToInstrumentationLibraries(configOptions),
		}
		instrumentedApplicationCrd.Spec.Options = append(instrumentedApplicationCrd.Spec.Options, newOption)
	}

	if err := kubeclient.Update(ctx, instrumentedApplicationCrd); err != nil {
		log.Logger.Error(err, "failed to update InstrumentedApplication")
		return err
	}

	return nil
}
