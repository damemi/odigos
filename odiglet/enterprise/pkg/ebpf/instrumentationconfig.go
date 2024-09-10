package ebpf

import (
	"context"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/instrumentationrules"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/otel/trace"
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

func findInstrumentationLibraryConfig(spec *odigosv1.SdkConfig, instrumentationLibraryId odigosv1.InstrumentationLibraryId) *odigosv1.InstrumentationLibraryConfig {
	for _, libConfig := range spec.InstrumentationLibraryConfigs {
		if libConfig.InstrumentationLibraryId == instrumentationLibraryId {
			return &libConfig
		}
	}
	return nil
}

func mergePayloadConfigs(defaultConfig *instrumentationrules.PayloadCollection, libConfig *instrumentationrules.PayloadCollection) instrumentationrules.PayloadCollection {
	mergedConfig := instrumentationrules.PayloadCollection{}
	if defaultConfig != nil {
		mergedConfig = *defaultConfig
	}

	// if there is a libConfig, we override the default config with the libConfig
	if libConfig != nil {
		if libConfig.HttpRequest != nil {
			mergedConfig.HttpRequest = libConfig.HttpRequest
		}
		if libConfig.HttpResponse != nil {
			mergedConfig.HttpResponse = libConfig.HttpResponse
		}
		if libConfig.DbQuery != nil {
			mergedConfig.DbQuery = libConfig.DbQuery
		}
	}
	return mergedConfig
}

func getPayloadCollectionConfig(spec *odigosv1.SdkConfig, instrumentationLibraryId odigosv1.InstrumentationLibraryId) instrumentationrules.PayloadCollection {

	// for each library, we use the content from the libraries config if there is any,
	// otherwise we take the default config from the SDK config.

	instrumentationLibraryConfig := findInstrumentationLibraryConfig(spec, instrumentationLibraryId)
	var instLibPayloadCollection *instrumentationrules.PayloadCollection
	if instrumentationLibraryConfig != nil {
		instLibPayloadCollection = instrumentationLibraryConfig.PayloadCollection
	}
	return mergePayloadConfigs(spec.DefaultPayloadCollection, instLibPayloadCollection)
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

	for _, sdkConfig := range spec.SdkConfigs {
		// this is supported
		if sdkConfig.Language != sdkLanguage || sdkConfig.Language != common.GoProgrammingLanguage {
			continue
		}

		// specific instructions for the go libraries:

		// net/http server
		payloadCollection := getPayloadCollectionConfig(&sdkConfig, odigosv1.InstrumentationLibraryId{
			InstrumentationLibraryName: "net/http",
			SpanKind:                   common.ServerSpanKind,
		})
		collectResponse := payloadCollection.HttpResponse != nil
		log.Logger.V(0).Info("Applying config to golang net/http server", "collectResponse", collectResponse)
		err := inst.ConfigBool("net/http", "http.response.body", trace.SpanKindServer, collectResponse)
		if err != nil {
			return err
		}
		err = inst.ConfigBool("net/http", "http.response.header.content-length", trace.SpanKindServer, collectResponse)
		if err != nil {
			return err
		}

		// net/http client
		payloadCollection = getPayloadCollectionConfig(&sdkConfig, odigosv1.InstrumentationLibraryId{
			InstrumentationLibraryName: "net/http",
			SpanKind:                   common.ClientSpanKind,
		})
		collectRequest := payloadCollection.HttpRequest != nil
		collectResponse = payloadCollection.HttpResponse != nil

		log.Logger.V(0).Info("Applying config to golang net/http client", "collectResponse", collectResponse, "collectRequest", collectRequest)
		err = inst.ConfigBool("net/http", "http.request.body", trace.SpanKindClient, collectRequest)
		if err != nil {
			return err
		}
		err = inst.ConfigBool("net/http", "http.response.header.content-length", trace.SpanKindClient, collectRequest)
		if err != nil {
			return err
		}

		// database/sql
		payloadCollection = getPayloadCollectionConfig(&sdkConfig, odigosv1.InstrumentationLibraryId{
			InstrumentationLibraryName: "database/sql",
			SpanKind:                   common.ClientSpanKind,
		})
		collectDbQuery := payloadCollection.DbQuery != nil
		log.Logger.V(0).Info("Applying config to golang database/sql", "collectDbQuery", collectDbQuery)
		err = inst.ConfigBool("database/sql", "db.query.text", trace.SpanKindClient, collectDbQuery)
		if err != nil {
			return err
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
